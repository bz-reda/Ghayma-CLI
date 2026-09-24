package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

var (
	tokenJSON          bool
	tokenCreateScopes  []string
	tokenCreateDays    int
	tokenCreateProject []string
	tokenListAll       bool
	tokenRevokeYes     bool
	tokenRotateDays    int
)

const tokenDefaultDays = 90

var tokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Manage account API tokens",
	Long: `Account API tokens act as you across the API (scoped, optionally restricted to projects).
A token can only create, rotate or revoke tokens no wider than itself.

A token is shown ONCE, at create and at rotate. A token is named by its id, its
prefix (gh_xxxxxxx) or its name.

Examples:
  ghayma token create ci --scope deploy,databases --project shop
  ghayma token list --all
  ghayma token rotate ci
  ghayma token revoke gh_1a2b3c4 --yes`,
}

var tokenCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create an API token (shown once)",
	Long: `Create an account API token. The token is printed ONCE: store it before it
scrolls away.

--scope takes a comma-separated list or repeats (default: deploy). --expires is
in days (default 90; 0 = never). --project restricts the token to a project, by
slug or id, and repeats; without it the token reaches every project you do.

Examples:
  ghayma token create ci
  ghayma token create deploy-bot --scope deploy,databases --project shop --project blog
  ghayma token create backup --expires 30 --json`,
	Args: argChecker("argument", "", 1, 1),
	Run:  runTokenCreate,
}

var tokenListCmd = &cobra.Command{
	Use:   "list",
	Short: "List your API tokens",
	Long: `List your account API tokens. Revoked tokens are included only with --all.

Examples:
  ghayma token list
  ghayma token list --all --json`,
	Args: argChecker("argument", "", 0, 0),
	Run:  runTokenList,
}

var tokenRevokeCmd = &cobra.Command{
	Use:   "revoke <id|prefix|name>",
	Short: "Revoke an API token",
	Long: `Revoke an API token: anything using it stops working immediately. This cannot
be undone.

Asks for confirmation unless --yes. Revoking the token this CLI is logged in with
logs the CLI out.

Examples:
  ghayma token revoke ci
  ghayma token revoke gh_1a2b3c4 --yes`,
	Args: argChecker("argument", "", 1, 1),
	Run:  runTokenRevoke,
}

var tokenRotateCmd = &cobra.Command{
	Use:   "rotate <id|prefix|name>",
	Short: "Replace an API token's secret (shown once)",
	Long: `Replace an API token's secret, keeping its name, scope and projects. The old
secret stops working immediately; the new one is printed ONCE.

By default the new token keeps the remaining lifetime of the old one; --expires N
sets a new one. Rotating the token this CLI is logged in with updates the CLI
config in place.

Examples:
  ghayma token rotate ci
  ghayma token rotate gh_1a2b3c4 --expires 30 --json`,
	Args: argChecker("argument", "", 1, 1),
	Run:  runTokenRotate,
}

// tokenClient is the login check every token command starts with. A nil
// client means the reason was already printed.
func tokenClient() (*api.Client, *config.Config) {
	cfg := config.Load()
	if !cfg.LoggedIn() {
		failf("Please login first: ghayma login")
		return nil, nil
	}
	return api.NewClient(cfg), cfg
}

// tokenFailure maps an API error onto the line the user sees. The server's
// own message is shown verbatim; a 404 means the token is out of reach.
func tokenFailure(err error, ref, action string) string {
	var apiErr *api.APIError
	if errors.As(err, &apiErr) {
		if apiErr.Status == http.StatusNotFound && ref != "" {
			return fmt.Sprintf("no token matches %q", ref)
		}
		if apiErr.Message != "" {
			return apiErr.Message
		}
	}
	return fmt.Sprintf("Failed to %s: %v", action, err)
}

// selectToken finds the one live token a selector names: exact id, exact
// prefix or exact name.
func selectToken(tokens []api.APITokenInfo, ref string) (*api.APITokenInfo, error) {
	var matches []*api.APITokenInfo
	for i := range tokens {
		t := &tokens[i]
		if t.RevokedAt != nil {
			continue
		}
		if t.ID == ref || t.TokenPrefix == ref || t.Name == ref {
			matches = append(matches, t)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no token matches %q", ref)
	case 1:
		return matches[0], nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%q matches %d tokens — use the id", ref, len(matches))
	for _, m := range matches {
		fmt.Fprintf(&b, "\n   %s  %s  %s", m.ID, m.TokenPrefix, m.Name)
	}
	return nil, errors.New(b.String())
}

// resolveToken lists the live tokens and selects one; nil means it failed.
func resolveToken(client *api.Client, ref string) *api.APITokenInfo {
	tokens, err := client.ListAPITokens(false)
	if err != nil {
		failf("%s", tokenFailure(err, "", "list the tokens"))
		return nil
	}
	tok, err := selectToken(tokens, ref)
	if err != nil {
		failf("%v", err)
		return nil
	}
	return tok
}

// tokenProjectsColumn names the projects a token is restricted to: slugs when
// known, ids otherwise, * when unrestricted.
func tokenProjectsColumn(ids []string, projects []api.TokenProject) string {
	slugs := map[string]string{}
	for _, p := range projects {
		slugs[p.ID] = p.Slug
	}
	var names []string
	for _, id := range ids {
		if s := slugs[id]; s != "" {
			names = append(names, s)
		} else {
			names = append(names, id)
		}
	}
	if len(ids) == 0 {
		for _, p := range projects {
			names = append(names, p.Slug)
		}
	}
	if len(names) == 0 {
		return "*"
	}
	return strings.Join(names, ",")
}

func tokenDate(t *time.Time, none string) string {
	if t == nil {
		return none
	}
	return t.UTC().Format("2006-01-02")
}

// visibleTokens drops revoked rows unless --all.
func visibleTokens(rows []api.APITokenInfo, all bool) []api.APITokenInfo {
	out := []api.APITokenInfo{}
	for _, r := range rows {
		if all || r.RevokedAt == nil {
			out = append(out, r)
		}
	}
	return out
}

func renderTokenTable(rows []api.APITokenInfo) {
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "PREFIX\tNAME\tSCOPE\tPROJECTS\tEXPIRES\tLAST USED\tCREATED")
	for _, r := range rows {
		expires := tokenDate(r.ExpiresAt, "never")
		if r.RevokedAt != nil {
			expires = "REVOKED"
		}
		created := "—"
		if !r.CreatedAt.IsZero() {
			created = tokenDate(&r.CreatedAt, "—")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.TokenPrefix, r.Name, r.Scope,
			tokenProjectsColumn(r.ProjectIDs, r.Projects), expires, tokenDate(r.LastUsedAt, "never"), created)
	}
	w.Flush()
}

// renderTokenSecret prints a freshly minted token: the secret once, then what
// it grants.
func renderTokenSecret(t *api.CreatedAPIToken) {
	fmt.Printf("Token (shown once): %s\n", t.Token)
	fmt.Println("Save it now — it will not be shown again.")
	if t.Scope != "" {
		fmt.Printf("   Scope:    %s\n", t.Scope)
	}
	fmt.Printf("   Projects: %s\n", tokenProjectsColumn(t.ProjectIDs, t.Projects))
	fmt.Printf("   Expires:  %s\n", tokenDate(t.ExpiresAt, "never"))
}

func printTokenJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

func neverExpiresNote(days int) {
	if days == 0 {
		fmt.Fprintln(os.Stderr, "⚠️  This token never expires. Revoke it when it is no longer needed.")
	}
}

// normalizeScopes accepts repeated and comma-separated --scope values.
func normalizeScopes(in []string) string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		for _, s := range strings.Split(v, ",") {
			s = strings.ToLower(strings.TrimSpace(s))
			if s != "" && !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	return strings.Join(out, ",")
}

func runTokenCreate(cmd *cobra.Command, args []string) {
	if tokenCreateDays < 0 {
		failf("--expires takes a number of days (0 = never)")
		return
	}
	client, _ := tokenClient()
	if client == nil {
		return
	}
	scope := normalizeScopes(tokenCreateScopes)
	if scope == "" {
		scope = "deploy"
	}
	var projects []string
	for _, p := range tokenCreateProject {
		if p = strings.TrimSpace(p); p != "" {
			projects = append(projects, p)
		}
	}
	created, err := client.CreateAPIToken(api.CreateAPITokenInput{
		Name:          strings.TrimSpace(args[0]),
		Scope:         scope,
		ExpiresInDays: tokenCreateDays,
		ProjectIDs:    projects,
	})
	if err != nil {
		failf("%s", tokenFailure(err, "", "create the token"))
		return
	}
	if tokenJSON {
		printTokenJSON(created)
		neverExpiresNote(tokenCreateDays)
		return
	}
	fmt.Printf("✅ Created token %q\n", created.Name)
	renderTokenSecret(created)
	neverExpiresNote(tokenCreateDays)
}

func runTokenList(cmd *cobra.Command, args []string) {
	client, _ := tokenClient()
	if client == nil {
		return
	}
	tokens, err := client.ListAPITokens(tokenListAll)
	if err != nil {
		failf("%s", tokenFailure(err, "", "list the tokens"))
		return
	}
	rows := visibleTokens(tokens, tokenListAll)
	if tokenJSON {
		printTokenJSON(rows)
		return
	}
	if len(rows) == 0 {
		fmt.Println("No API tokens. Create one: ghayma token create <name>")
		return
	}
	renderTokenTable(rows)
}

func runTokenRevoke(cmd *cobra.Command, args []string) {
	client, cfg := tokenClient()
	if client == nil {
		return
	}
	tok := resolveToken(client, args[0])
	if tok == nil {
		return
	}
	revokeToken(client, cfg, tok, tokenRevokeYes, readAnswer)
}

const ownTokenRevokeNote = "This is the token this CLI is logged in with; you will have to run ghayma login again."

func confirmed(ask func() string) bool {
	a := strings.TrimSpace(ask())
	return a == "y" || a == "Y"
}

// revokeToken confirms and revokes a resolved token. Revoking the CLI's own
// token asks once more and clears it from the config.
func revokeToken(client *api.Client, cfg *config.Config, tok *api.APITokenInfo, yes bool, ask func() string) {
	own := cfg.APITokenID != "" && tok.ID == cfg.APITokenID
	if yes {
		if own {
			fmt.Println("⚠️  " + ownTokenRevokeNote)
		}
	} else {
		fmt.Printf("⚠️  This revokes token %q (%s) now: anything using it stops working immediately, and it cannot be undone. Continue? [y/N] ", tok.Name, tok.TokenPrefix)
		if !confirmed(ask) {
			fmt.Println("❌ Cancelled.")
			return
		}
		if own {
			fmt.Print("⚠️  " + ownTokenRevokeNote + " Continue? [y/N] ")
			if !confirmed(ask) {
				fmt.Println("❌ Cancelled.")
				return
			}
		}
	}
	if err := client.DeleteAPIToken(tok.ID); err != nil {
		failf("%s", tokenFailure(err, tok.Name, "revoke the token"))
		return
	}
	fmt.Printf("✅ Revoked token %q (%s)\n", tok.Name, tok.TokenPrefix)
	if own {
		cfg.APIToken = ""
		cfg.APITokenID = ""
		if err := cfg.Save(); err != nil {
			fmt.Printf("⚠️  Could not update the config: %v\n", err)
		}
	}
}

func runTokenRotate(cmd *cobra.Command, args []string) {
	if tokenRotateDays < 0 {
		failf("--expires takes a number of days (0 = never)")
		return
	}
	client, cfg := tokenClient()
	if client == nil {
		return
	}
	tok := resolveToken(client, args[0])
	if tok == nil {
		return
	}
	rotateToken(client, cfg, tok, tokenRotateDays, tokenJSON)
}

// rotateToken replaces a resolved token's secret and prints the new one once.
// days 0 keeps the remaining lifetime. When it is the CLI's own token, the
// config takes the new secret.
func rotateToken(client *api.Client, cfg *config.Config, tok *api.APITokenInfo, days int, asJSON bool) {
	rotated, err := client.RotateAPIToken(tok.ID, days)
	if err != nil {
		failf("%s", tokenFailure(err, tok.Name, "rotate the token"))
		return
	}
	own := cfg.APITokenID != "" && tok.ID == cfg.APITokenID
	if own {
		cfg.APIToken = rotated.Token
		cfg.APITokenID = rotated.ID
		if err := cfg.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️  Could not update the config: %v\n", err)
			own = false
		}
	}
	if asJSON {
		printTokenJSON(rotated)
	} else {
		fmt.Printf("✅ Rotated token %q\n", rotated.Name)
		renderTokenSecret(rotated)
	}
	if own {
		note := "This CLI's own token was rotated; the config was updated."
		if asJSON {
			fmt.Fprintln(os.Stderr, note)
		} else {
			fmt.Println(note)
		}
	}
}

func init() {
	tokenCreateCmd.Flags().StringSliceVar(&tokenCreateScopes, "scope", []string{"deploy"}, "Scopes, comma-separated or repeated")
	tokenCreateCmd.Flags().IntVar(&tokenCreateDays, "expires", tokenDefaultDays, "Expiry in days (0 = never)")
	tokenCreateCmd.Flags().StringArrayVar(&tokenCreateProject, "project", nil, "Restrict to a project (slug or id); repeat for several")
	tokenCreateCmd.Flags().BoolVar(&tokenJSON, "json", false, "Print the created token as JSON")

	tokenListCmd.Flags().BoolVar(&tokenListAll, "all", false, "Include revoked tokens")
	tokenListCmd.Flags().BoolVar(&tokenJSON, "json", false, "Print the tokens as JSON")

	tokenRevokeCmd.Flags().BoolVar(&tokenRevokeYes, "yes", false, "Skip the confirmation")

	tokenRotateCmd.Flags().IntVar(&tokenRotateDays, "expires", 0, "New expiry in days (default: keep the token's remaining lifetime)")
	tokenRotateCmd.Flags().BoolVar(&tokenJSON, "json", false, "Print the rotated token as JSON")

	tokenCmd.AddCommand(tokenCreateCmd, tokenListCmd, tokenRevokeCmd, tokenRotateCmd)
	rootCmd.AddCommand(tokenCmd)
}
