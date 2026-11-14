package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"paas-cli/internal/api"

	"github.com/spf13/cobra"
)

// Site protection (SITE-PROTECTION-DESIGN-2026-11-14 §7.13): a staging or
// development site can be made private. A production site is always public.

var (
	siteProtectionYes    bool
	siteProtectionJSON   bool
	siteVisitorsJSON     bool
	siteBypassListJSON   bool
	siteBypassCreateJSON bool
	siteBypassRevokeID   string
	siteBypassRevokeYes  bool
)

// protectionAsk reads the answer to the confirmation; tests replace it.
var protectionAsk = readAnswer

// protectionFailure is the one place a protection API error becomes a sentence.
func protectionFailure(err error, siteSlug string) string {
	var apiErr *api.APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Code == api.CodeProtectionUnavailable:
			return "Site protection is not switched on for this platform yet."
		case apiErr.Code == api.CodeForbidden || apiErr.Status == 403:
			return fmt.Sprintf("You need the write role on '%s' to change its protection.", siteSlug)
		case apiErr.Status == 404 && apiErr.Code == "":
			return "This platform does not serve site protection yet — the update that adds it is not deployed."
		case apiErr.Code == api.CodeNotFound:
			return "That site, email or token was not found."
		}
	}
	return fmt.Sprintf("%v", err)
}

func isProductionEnvironment(env string) bool {
	return env == "" || env == api.EnvironmentProduction
}

// protectionStatusLines describes a site's protection as the show command
// prints it.
func protectionStatusLines(siteSlug string, v *api.Protection) []string {
	switch {
	case !v.Available:
		return []string{"ℹ️  Site protection is not switched on for this platform yet."}
	case isProductionEnvironment(v.Environment):
		return []string{
			fmt.Sprintf("🌐 '%s' is a production site, so it is public.", siteSlug),
			"   Protection applies when the site is staging or development.",
		}
	case v.Effective:
		return []string{
			fmt.Sprintf("🔒 '%s' is private — only project members and allowed emails can open it.", siteSlug),
			fmt.Sprintf("   Allowed emails: %d of %d   Bypass tokens: %d of %d", len(v.Emails), v.Limits.Emails, len(v.Tokens), v.Limits.Tokens),
			fmt.Sprintf("   Manage them: ghayma site visitors list %s · ghayma site bypass list %s", siteSlug, siteSlug),
		}
	default:
		return []string{
			fmt.Sprintf("🌐 '%s' is public — anyone with the URL can open it.", siteSlug),
			fmt.Sprintf("   Make it private: ghayma site protection %s on", siteSlug),
		}
	}
}

var siteProtectionCmd = &cobra.Command{
	Use:   "protection <site> [on|off]",
	Short: "Show or switch a site's protection",
	Long: `Show whether a site is private, or switch its protection on or off.

A private site opens only for project members and the emails you allow
(ghayma site visitors); automation passes with a bypass token (ghayma site
bypass). Protection applies to staging and development sites; a production
site is always public.

Examples:
  ghayma site protection staging
  ghayma site protection staging on
  ghayma site protection staging off --yes`,
	Args: argChecker("argument", "site list", 1, 2),
	Run:  runSiteProtection,
}

func runSiteProtection(cmd *cobra.Command, args []string) {
	var enable bool
	toggle := len(args) == 2
	if toggle {
		v, err := parseOnOff(args[1])
		if err != nil {
			failf("%v", err)
			return
		}
		enable = v
	}
	client, projectID, site, err := resolveNamedSite(args[0])
	if err != nil {
		reportSiteError(err)
		exitFn(1)
		return
	}
	if !toggle {
		showProtection(client, projectID, site)
		return
	}
	if enable {
		protectionOn(client, projectID, site)
	} else {
		protectionOff(client, projectID, site)
	}
}

func showProtection(client *api.Client, projectID string, site *api.Site) {
	v, err := client.GetProtection(projectID, site.ID)
	if err != nil {
		failf("%s", protectionFailure(err, site.Slug))
		return
	}
	if siteProtectionJSON {
		raw, _ := json.MarshalIndent(v, "", "  ")
		fmt.Println(string(raw))
		return
	}
	for _, line := range protectionStatusLines(site.Slug, v) {
		fmt.Println(line)
	}
}

// protectionOn always sends the PUT: a repeated `on` is how a user retries a
// route the backend failed to apply.
func protectionOn(client *api.Client, projectID string, site *api.Site) {
	v, err := client.SetProtection(projectID, site.ID, true)
	if err != nil {
		var apiErr *api.APIError
		if errors.As(err, &apiErr) && apiErr.Code == api.CodeRouteApplyFailed {
			fmt.Println("⚠️  Saved, but the route is not updated yet. It is retried automatically within a few minutes; run the command again to retry now.")
			exitFn(1)
			return
		}
		failf("%s", protectionFailure(err, site.Slug))
		return
	}
	if !v.Effective && isProductionEnvironment(v.Environment) {
		fmt.Printf("ℹ️  Saved. '%s' is a production site, so it stays public; protection applies when it is staging or development.\n", site.Slug)
		return
	}
	fmt.Printf("✅ '%s' is now private.\n", site.Slug)
	if v.Effective && len(v.Emails) == 0 {
		fmt.Printf("   Project members can open it. Allow someone else: ghayma site visitors add %s <email>\n", site.Slug)
	}
}

func protectionOff(client *api.Client, projectID string, site *api.Site) {
	if !siteProtectionYes {
		fmt.Printf("⚠️  This makes '%s' public: anyone with the URL can open it. Continue? [y/N] ", site.Slug)
		if !confirmed(protectionAsk) {
			fmt.Println("❌ Cancelled.")
			exitFn(1)
			return
		}
	}
	if _, err := client.SetProtection(projectID, site.ID, false); err != nil {
		failf("%s", protectionFailure(err, site.Slug))
		return
	}
	fmt.Printf("✅ '%s' is now public.\n", site.Slug)
}

var siteVisitorsCmd = &cobra.Command{
	Use:   "visitors",
	Short: "Manage the people outside the project allowed to open a private site",
	Long: `Manage the emails of people outside the project who may open a private
site. Project members can always open it and do not need to be listed.

Examples:
  ghayma site visitors list staging
  ghayma site visitors add staging ana@example.com
  ghayma site visitors remove staging ana@example.com`,
}

var siteVisitorsListCmd = &cobra.Command{
	Use:   "list <site>",
	Short: "List the emails allowed on a private site",
	Args:  argChecker("site", "site list", 1, 1),
	Run:   runSiteVisitorsList,
}

var siteVisitorsAddCmd = &cobra.Command{
	Use:   "add <site> <email>",
	Short: "Allow an email to open a private site",
	Args:  argChecker("argument", "site list", 2, 2),
	Run:   runSiteVisitorsAdd,
}

var siteVisitorsRemoveCmd = &cobra.Command{
	Use:   "remove <site> <email>",
	Short: "Stop an email from opening a private site",
	Args:  argChecker("argument", "site list", 2, 2),
	Run:   runSiteVisitorsRemove,
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func findProtectionEmail(v *api.Protection, email string) *api.ProtectionEmail {
	want := normalizeEmail(email)
	for i := range v.Emails {
		if normalizeEmail(v.Emails[i].Email) == want {
			return &v.Emails[i]
		}
	}
	return nil
}

func printNotPrivateNote(siteSlug string, v *api.Protection) {
	if !v.Effective {
		fmt.Printf("   Note: '%s' is not private right now (ghayma site protection %s).\n", siteSlug, siteSlug)
	}
}

func runSiteVisitorsList(cmd *cobra.Command, args []string) {
	client, projectID, site, err := resolveNamedSite(args[0])
	if err != nil {
		reportSiteError(err)
		exitFn(1)
		return
	}
	v, err := client.GetProtection(projectID, site.ID)
	if err != nil {
		failf("%s", protectionFailure(err, site.Slug))
		return
	}
	if siteVisitorsJSON {
		emails := v.Emails
		if emails == nil {
			emails = []api.ProtectionEmail{}
		}
		raw, _ := json.MarshalIndent(emails, "", "  ")
		fmt.Println(string(raw))
		return
	}
	if !v.Available {
		fmt.Println("ℹ️  Site protection is not switched on for this platform yet.")
		return
	}
	if len(v.Emails) == 0 {
		fmt.Printf("ℹ️  No extra emails are allowed on '%s'.\n", site.Slug)
		fmt.Printf("   Allow one: ghayma site visitors add %s <email>\n", site.Slug)
	} else {
		fmt.Println("Project members can always open the site; they do not need to be listed.")
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "EMAIL\tACCOUNT\tADDED")
		for _, e := range v.Emails {
			account := "no account yet"
			if e.HasAccount {
				account = "yes"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", e.Email, account, e.CreatedAt.UTC().Format("2006-01-02"))
		}
		w.Flush()
	}
	printNotPrivateNote(site.Slug, v)
}

func runSiteVisitorsAdd(cmd *cobra.Command, args []string) {
	client, projectID, site, err := resolveNamedSite(args[0])
	if err != nil {
		reportSiteError(err)
		exitFn(1)
		return
	}
	email := strings.TrimSpace(args[1])
	v, err := client.AddProtectionEmail(projectID, site.ID, email)
	if err != nil {
		var apiErr *api.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.Code {
			case api.CodeEmailExists:
				fmt.Printf("ℹ️  %s is already allowed on '%s'.\n", email, site.Slug)
				return
			case api.CodeInvalidEmail:
				failf("That is not a valid email address.")
				return
			case api.CodeEmailLimitReached:
				if current, getErr := client.GetProtection(projectID, site.ID); getErr == nil {
					failf("'%s' already has the maximum of %d allowed emails. Remove one first.", site.Slug, current.Limits.Emails)
				} else {
					failf("'%s' already has the maximum number of allowed emails. Remove one first.", site.Slug)
				}
				return
			}
		}
		failf("%s", protectionFailure(err, site.Slug))
		return
	}
	added := findProtectionEmail(v, email)
	if added != nil {
		email = added.Email
	}
	fmt.Printf("✅ %s can now open '%s'.\n", email, site.Slug)
	if added != nil && !added.HasAccount {
		fmt.Println("   They need a Ghayma account with this email before they can sign in.")
	}
	printNotPrivateNote(site.Slug, v)
}

// runSiteVisitorsRemove resolves the email to its ID first: the API deletes by
// ID, and an email the user typed may differ in case or spacing.
func runSiteVisitorsRemove(cmd *cobra.Command, args []string) {
	client, projectID, site, err := resolveNamedSite(args[0])
	if err != nil {
		reportSiteError(err)
		exitFn(1)
		return
	}
	v, err := client.GetProtection(projectID, site.ID)
	if err != nil {
		failf("%s", protectionFailure(err, site.Slug))
		return
	}
	entry := findProtectionEmail(v, args[1])
	if entry == nil {
		failf("%s is not on the list for '%s'.", strings.TrimSpace(args[1]), site.Slug)
		return
	}
	if _, err := client.RemoveProtectionEmail(projectID, site.ID, entry.ID); err != nil {
		failf("%s", protectionFailure(err, site.Slug))
		return
	}
	fmt.Printf("✅ %s can no longer open '%s'.\n", entry.Email, site.Slug)
	fmt.Println("   It takes effect within a minute.")
}

var siteBypassCmd = &cobra.Command{
	Use:   "bypass",
	Short: "Manage the tokens that let scripts, mobile apps and webhooks through a private site",
	Long: `Manage a private site's bypass tokens. A script, mobile app or webhook that
cannot sign in sends a token as the header ` + api.BypassHeader + `
(or the query parameter ` + api.BypassQueryParam + `) to get through.

Examples:
  ghayma site bypass list staging
  ghayma site bypass create staging CI
  ghayma site bypass revoke staging CI`,
}

var siteBypassListCmd = &cobra.Command{
	Use:   "list <site>",
	Short: "List a site's bypass tokens",
	Args:  argChecker("site", "site list", 1, 1),
	Run:   runSiteBypassList,
}

var siteBypassCreateCmd = &cobra.Command{
	Use:   "create <site> <name>",
	Short: "Create a bypass token; its secret is shown once",
	Args:  argChecker("argument", "site list", 2, 2),
	Run:   runSiteBypassCreate,
}

var siteBypassRevokeCmd = &cobra.Command{
	Use:   "revoke <site> [name]",
	Short: "Revoke a bypass token by name, or by ID with --id",
	Args:  argChecker("argument", "site list", 1, 2),
	Run:   runSiteBypassRevoke,
}

func runSiteBypassList(cmd *cobra.Command, args []string) {
	client, projectID, site, err := resolveNamedSite(args[0])
	if err != nil {
		reportSiteError(err)
		exitFn(1)
		return
	}
	v, err := client.GetProtection(projectID, site.ID)
	if err != nil {
		failf("%s", protectionFailure(err, site.Slug))
		return
	}
	if siteBypassListJSON {
		tokens := v.Tokens
		if tokens == nil {
			tokens = []api.ProtectionToken{}
		}
		raw, _ := json.MarshalIndent(tokens, "", "  ")
		fmt.Println(string(raw))
		return
	}
	if !v.Available {
		fmt.Println("ℹ️  Site protection is not switched on for this platform yet.")
		return
	}
	if len(v.Tokens) == 0 {
		fmt.Printf("ℹ️  '%s' has no bypass tokens.\n", site.Slug)
		fmt.Printf("   Create one for scripts, mobile apps or webhooks: ghayma site bypass create %s <name>\n", site.Slug)
	} else {
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tTOKEN\tCREATED\tID")
		for _, tok := range v.Tokens {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", tok.Name, tok.Hint, tok.CreatedAt.UTC().Format("2006-01-02"), tok.ID)
		}
		w.Flush()
	}
	printNotPrivateNote(site.Slug, v)
}

func runSiteBypassCreate(cmd *cobra.Command, args []string) {
	client, projectID, site, err := resolveNamedSite(args[0])
	if err != nil {
		reportSiteError(err)
		exitFn(1)
		return
	}
	name := args[1]
	created, err := client.CreateBypassToken(projectID, site.ID, name)
	if err != nil {
		var apiErr *api.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.Code {
			case api.CodeInvalidTokenName:
				failf("A token name must be 1 to 50 characters.")
				return
			case api.CodeTokenLimitReached:
				if current, getErr := client.GetProtection(projectID, site.ID); getErr == nil {
					failf("'%s' already has the maximum of %d bypass tokens. Revoke one first: ghayma site bypass list %s", site.Slug, current.Limits.Tokens, site.Slug)
				} else {
					failf("'%s' already has the maximum number of bypass tokens. Revoke one first: ghayma site bypass list %s", site.Slug, site.Slug)
				}
				return
			}
		}
		failf("%s", protectionFailure(err, site.Slug))
		return
	}
	if siteBypassCreateJSON {
		raw, _ := json.MarshalIndent(created, "", "  ")
		fmt.Println(string(raw))
		return
	}
	fmt.Printf("✅ Created bypass token %q for '%s'\n", name, site.Slug)
	fmt.Printf("Token (shown once): %s\n", created.Token)
	fmt.Println("Save it now — it will not be shown again.")
	fmt.Printf("   Send it as a header:   %s: %s\n", api.BypassHeader, created.Token)
	fmt.Printf("   Or in a URL:           https://<your-site>/path?%s=%s\n", api.BypassQueryParam, created.Token)
	fmt.Println("   A token in a URL can end up in logs; prefer the header.")
	// A null view means the server could not read the site back; the token
	// still exists, so only the note is skipped.
	if created.View != nil {
		printNotPrivateNote(site.Slug, created.View)
	}
}

// runSiteBypassRevoke resolves the token from the current list: the API revokes
// by ID, and names are labels that need not be unique.
func runSiteBypassRevoke(cmd *cobra.Command, args []string) {
	if siteBypassRevokeID == "" && len(args) < 2 {
		failf("Give the token's name, or pick one with --id.")
		return
	}
	client, projectID, site, err := resolveNamedSite(args[0])
	if err != nil {
		reportSiteError(err)
		exitFn(1)
		return
	}
	v, err := client.GetProtection(projectID, site.ID)
	if err != nil {
		failf("%s", protectionFailure(err, site.Slug))
		return
	}
	var target *api.ProtectionToken
	if siteBypassRevokeID != "" {
		for i := range v.Tokens {
			if v.Tokens[i].ID == siteBypassRevokeID {
				target = &v.Tokens[i]
				break
			}
		}
		if target == nil {
			failf("No bypass token with ID %q on '%s'.", siteBypassRevokeID, site.Slug)
			return
		}
	} else {
		name := args[1]
		var matches []api.ProtectionToken
		for _, tok := range v.Tokens {
			if tok.Name == name {
				matches = append(matches, tok)
			}
		}
		switch len(matches) {
		case 0:
			failf("No bypass token named %q on '%s'.", name, site.Slug)
			return
		case 1:
			target = &matches[0]
		default:
			fmt.Printf("❌ %d bypass tokens are named %q. Pick one with --id:\n", len(matches), name)
			for _, tok := range matches {
				fmt.Printf("   %s  %s  created %s\n", tok.ID, tok.Hint, tok.CreatedAt.UTC().Format("2006-01-02"))
			}
			exitFn(1)
			return
		}
	}
	if !siteBypassRevokeYes {
		fmt.Printf("⚠️  Anything using this token will be refused by '%s' within a minute. Continue? [y/N] ", site.Slug)
		if !confirmed(protectionAsk) {
			fmt.Println("❌ Cancelled.")
			exitFn(1)
			return
		}
	}
	if _, err := client.RevokeBypassToken(projectID, site.ID, target.ID); err != nil {
		failf("%s", protectionFailure(err, site.Slug))
		return
	}
	fmt.Printf("✅ Revoked bypass token %q.\n", target.Name)
}

func init() {
	siteProtectionCmd.Flags().BoolVar(&siteProtectionYes, "yes", false, "Turn protection off without asking")
	siteProtectionCmd.Flags().BoolVar(&siteProtectionJSON, "json", false, "Print the protection as JSON")
	siteCmd.AddCommand(siteProtectionCmd)

	siteVisitorsListCmd.Flags().BoolVar(&siteVisitorsJSON, "json", false, "Print the allowed emails as JSON")
	siteVisitorsCmd.AddCommand(siteVisitorsListCmd, siteVisitorsAddCmd, siteVisitorsRemoveCmd)
	siteCmd.AddCommand(siteVisitorsCmd)

	siteBypassListCmd.Flags().BoolVar(&siteBypassListJSON, "json", false, "Print the bypass tokens as JSON")
	siteBypassCreateCmd.Flags().BoolVar(&siteBypassCreateJSON, "json", false, "Print the created token as JSON")
	siteBypassRevokeCmd.Flags().StringVar(&siteBypassRevokeID, "id", "", "Revoke the token with this ID")
	siteBypassRevokeCmd.Flags().BoolVar(&siteBypassRevokeYes, "yes", false, "Revoke without asking")
	siteBypassCmd.AddCommand(siteBypassListCmd, siteBypassCreateCmd, siteBypassRevokeCmd)
	siteCmd.AddCommand(siteBypassCmd)
}
