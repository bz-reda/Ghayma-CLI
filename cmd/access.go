package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

var (
	accessJSON      bool
	accessAddName   string
	accessAddLevel  string
	accessAddAllow  string
	accessAddDays   int
	accessRevokeYes bool
	accessRotateYes bool
	accessAllowSet  string
)

var accessCmd = &cobra.Command{
	Use:   "access <database|bucket> <name>",
	Short: "Show and manage who outside Ghayma may reach a service",
	Long: `List who may reach one of the project's services: the apps inside Ghayma that
are connected to it, and the named principals outside Ghayma that hold their
own credential for it.

An external principal is a connection minus the app: a name, a level, its own
engine credential, an optional source-IP allowlist and an optional expiry. It
is rotated, allowlisted and revoked alone, and its credential is shown once —
at 'ghayma access add' and at 'ghayma access rotate'.

Only databases and buckets have external principals. An auth app's external
access is its restricted project keys, which are minted in the console.

Examples:
  ghayma access database pg-main
  ghayma access bucket uploads --json
  ghayma access add database pg-main --name metabase --level read-only
  ghayma access allow database pg-main metabase --set 203.0.113.4
  ghayma access rotate database pg-main metabase
  ghayma access revoke database pg-main metabase --yes`,
	Args: argChecker("argument", "", 2, 2),
	Run:  runAccessList,
}

var accessAddCmd = &cobra.Command{
	Use:   "add <database|bucket> <name> --name <principal>",
	Short: "Grant an outside principal its own credential for a service",
	Long: `Grant a named principal outside Ghayma its own credential for one database or
bucket.

The credential is printed ONCE and cannot be retrieved again: store it before
it scrolls away. Nothing reads it back — a lost secret is replaced with
'ghayma access rotate', which mints a new one on the same identity.

--level takes the levels a connection takes (database: connect|read-only;
bucket: read-write|read) and defaults to the full one. --allow narrows the
sources that may reach the resource, and --expires revokes the principal on its
own after that many days.

Examples:
  ghayma access add database pg-main --name metabase
  ghayma access add database pg-main --name "partner-x CI" --level read-only --allow 203.0.113.0/24
  ghayma access add bucket uploads --name backups --expires 30`,
	Args: argChecker("argument", "", 2, 2),
	Run:  runAccessAdd,
}

var accessRevokeCmd = &cobra.Command{
	Use:   "revoke <database|bucket> <name> <principal>",
	Short: "Drop a principal's credential and close it",
	Long: `Drop the credential one principal holds and close its row.

The engine credential is deleted: anything still connecting with it stops at
once, and it cannot be restored — the replacement is a new principal with a new
secret. The row itself is kept, marked revoked, so "who reached this database,
and when did it stop" still has an answer.

Asks for confirmation unless --yes.

Examples:
  ghayma access revoke database pg-main metabase
  ghayma access revoke bucket uploads backups --yes`,
	Args: argChecker("argument", "", 3, 3),
	Run:  runAccessRevoke,
}

var accessRotateCmd = &cobra.Command{
	Use:   "rotate <database|bucket> <name> <principal>",
	Short: "Replace a principal's secret, keeping its identity",
	Long: `Replace a principal's secret without moving anything else: the name, the level
and the allowlist stay as they are, so the consumer swaps one string and
carries on. The old secret stops working immediately.

The new credential is printed ONCE, exactly as 'ghayma access add' prints it.

Asks for confirmation unless --yes.

Examples:
  ghayma access rotate database pg-main metabase
  ghayma access rotate bucket uploads backups --yes`,
	Args: argChecker("argument", "", 3, 3),
	Run:  runAccessRotate,
}

var accessAllowCmd = &cobra.Command{
	Use:   "allow <database|bucket> <name> <principal> --set <cidr,...>",
	Short: "Replace the sources a principal may connect from",
	Long: `Replace the source addresses one principal may connect from. The list given is
the whole truth: it replaces whatever was there, and an empty list (--set "")
clears it, which means any source may use that credential.

The filter lives at the front door and runs BEFORE any authentication, so it
cannot tell which principal a connection belongs to. That gives one rule with
no exception: while any principal of this resource is unrestricted, the
platform enforces no IP filter at all — a narrower filter would lock that
principal out. An allowlist starts biting once every principal in force has
one.

Bare addresses are accepted and widened (203.0.113.4 becomes 203.0.113.4/32).

Examples:
  ghayma access allow database pg-main metabase --set 203.0.113.0/24,198.51.100.7
  ghayma access allow bucket uploads backups --set ""`,
	Args: argChecker("argument", "", 3, 3),
	Run:  runAccessAllow,
}

// accessClient is the login check plus the client every access command starts
// with. nil means the command already printed the reason and exited.
func accessClient() *api.Client {
	cfg := config.Load()
	if !cfg.LoggedIn() {
		failf("Please login first: ghayma login")
		return nil
	}
	return api.NewClient(cfg)
}

// resolveAccessTarget maps the kind and name the user typed onto the project
// and the resource UUID the API addresses. The project comes from the nearest
// config: external access is PROJECT-scoped, never site-scoped, so no site has
// to be resolved (and none could be — a principal belongs to no app).
func resolveAccessTarget(client *api.Client, kindArg, name string) (*accessTarget, error) {
	kind, err := parseAccessKind(kindArg)
	if err != nil {
		return nil, err
	}
	projectID, _, projectName, err := localConfig()
	if err != nil {
		return nil, err
	}
	ids, names, err := projectResources(client, projectID, kind)
	if err != nil {
		return nil, err
	}
	id, resolved, err := pickResource(kind, name, ids, names)
	if err != nil {
		return nil, err
	}
	return &accessTarget{ProjectID: projectID, ProjectName: projectName, Kind: kind, ResourceID: id, ResourceName: resolved}, nil
}

// projectResources lists one kind's resources IN THIS PROJECT. The listings are
// account-wide, so they are filtered by project here: a name is only unique
// inside a project, and acting on a same-named resource of another one would be
// the worst possible way to be wrong.
func projectResources(client *api.Client, projectID, kind string) ([]string, []string, error) {
	var ids, names []string
	if kind == "bucket" {
		buckets, err := client.ListBuckets()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to list buckets: %v", err)
		}
		for _, b := range buckets {
			if b.ProjectID == projectID {
				ids, names = append(ids, b.ID), append(names, b.Name)
			}
		}
		return ids, names, nil
	}
	databases, err := client.ListDatabases()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list databases: %v", err)
	}
	for _, db := range databases {
		if db.ProjectID == projectID {
			ids, names = append(ids, db.ID), append(names, db.Name)
		}
	}
	return ids, names, nil
}

// resolvePrincipal is the second resolution every mutating command does: the
// resource, then the principal named on it. The rows come back too, because
// the collapse rule is about ALL of them.
func resolvePrincipal(client *api.Client, kindArg, resource, principal string) (*accessTarget, *api.ExternalAccess, []api.ExternalAccess, bool) {
	target, err := resolveAccessTarget(client, kindArg, resource)
	if err != nil {
		failf("%v", err)
		return nil, nil, nil, false
	}
	rows, err := client.ListExternalAccess(target.ProjectID, target.Kind, target.ResourceID)
	if err != nil {
		failf("%s", accessFailure(err, "read the external access"))
		return nil, nil, nil, false
	}
	row, err := findPrincipal(rows, principal)
	if err != nil {
		failf("%v", err)
		return nil, nil, nil, false
	}
	return target, row, rows, true
}

func runAccessList(cmd *cobra.Command, args []string) {
	client := accessClient()
	if client == nil {
		return
	}
	target, err := resolveAccessTarget(client, args[0], args[1])
	if err != nil {
		failf("%v", err)
		return
	}
	rows, err := client.ListExternalAccess(target.ProjectID, target.Kind, target.ResourceID)
	if err != nil {
		failf("%s", accessFailure(err, "list the external access"))
		return
	}
	// The apps half comes from the connections listing, which is the one place
	// that knows a site's slug and level — this endpoint is externals only.
	conns, err := client.ListConnections(target.ProjectID, "")
	if err != nil {
		failf("Failed to list connections: %v", err)
		return
	}
	var mine []api.Connection
	for _, c := range conns {
		if c.Kind == target.Kind && c.ResourceID == target.ResourceID {
			mine = append(mine, c)
		}
	}

	if accessJSON {
		payload := map[string]interface{}{"connections": mine, "external": rows}
		if mine == nil {
			payload["connections"] = []api.Connection{}
		}
		b, _ := json.MarshalIndent(payload, "", "  ")
		fmt.Println(string(b))
		return
	}
	printAccessListing(target, mine, rows)
}

func runAccessAdd(cmd *cobra.Command, args []string) {
	client := accessClient()
	if client == nil {
		return
	}
	principal := strings.TrimSpace(accessAddName)
	if principal == "" {
		failf("A principal needs a name: ghayma access add %s %s --name <principal>", args[0], args[1])
		return
	}
	target, err := resolveAccessTarget(client, args[0], args[1])
	if err != nil {
		failf("%v", err)
		return
	}
	level, err := resolveLevel(target.Kind, accessAddLevel, nil)
	if err != nil {
		failf("%v", err)
		return
	}
	expires, err := expiryFromDays(accessAddDays, time.Now())
	if err != nil {
		failf("%v", err)
		return
	}

	out, err := client.CreateExternalAccess(target.ProjectID, target.Kind, target.ResourceID, api.ExternalAccessRequest{
		Name:       principal,
		Level:      level,
		AllowCIDRs: parseCIDRList(accessAddAllow),
		ExpiresAt:  expires,
	})
	if err != nil {
		failf("%s", accessFailure(err, "add the external access"))
		return
	}

	created := out.Access
	shownLevel := level
	if created != nil && created.Level != "" {
		shownLevel = created.Level
	}
	fmt.Printf("✅ Added '%s' to %s '%s' (%s)\n", principal, target.Kind, target.ResourceName, shownLevel)
	if created != nil {
		fmt.Printf("   Allowed sources: %s\n", allowSummary(created.AllowCIDRs))
		if created.ExpiresAt != "" {
			fmt.Printf("   Expires: %s\n", shortDate(created.ExpiresAt))
		}
	}
	printCredential(target, principal, out)
}

func runAccessRevoke(cmd *cobra.Command, args []string) {
	client := accessClient()
	if client == nil {
		return
	}
	target, row, _, ok := resolvePrincipal(client, args[0], args[1], args[2])
	if !ok {
		return
	}
	revokeAccess(client, target, row, accessRevokeYes, readAnswer)
}

// revokeAccess is the acting half, on an already-resolved principal. ask reads
// one token from the terminal; tests inject it.
func revokeAccess(client *api.Client, target *accessTarget, row *api.ExternalAccess, yes bool, ask func() string) {
	if row.RevokedAt != "" {
		fmt.Printf("ℹ️  '%s' is already revoked on %s '%s' — no change.\n", row.Name, target.Kind, target.ResourceName)
		return
	}
	if !confirmAccessRevoke(target, row.Name, yes, ask) {
		fmt.Println("❌ Cancelled.")
		return
	}
	if err := client.RevokeExternalAccess(target.ProjectID, target.Kind, target.ResourceID, row.ID); err != nil {
		failf("%s", accessFailure(err, "revoke the external access"))
		return
	}
	fmt.Printf("✅ Revoked '%s' on %s '%s' — its credential no longer works.\n", row.Name, target.Kind, target.ResourceName)
}

// confirmAccessRevoke is the arm/confirm before a credential is destroyed. The
// prompt says what cannot be undone, because nothing after it can.
func confirmAccessRevoke(target *accessTarget, principal string, yes bool, ask func() string) bool {
	if yes {
		return true
	}
	fmt.Printf("⚠️  This deletes the credential '%s' uses for %s '%s' now: anything still connecting with it stops immediately, and it cannot be restored. Continue? [y/N] ",
		principal, target.Kind, target.ResourceName)
	answer := strings.TrimSpace(ask())
	return answer == "y" || answer == "Y"
}

func runAccessRotate(cmd *cobra.Command, args []string) {
	client := accessClient()
	if client == nil {
		return
	}
	target, row, _, ok := resolvePrincipal(client, args[0], args[1], args[2])
	if !ok {
		return
	}
	rotateAccess(client, target, row, accessRotateYes, readAnswer)
}

// rotateAccess is the acting half: confirm, rotate, print the new secret once.
func rotateAccess(client *api.Client, target *accessTarget, row *api.ExternalAccess, yes bool, ask func() string) {
	if !confirmAccessRotate(target, row.Name, yes, ask) {
		fmt.Println("❌ Cancelled.")
		return
	}
	out, err := client.RotateExternalAccess(target.ProjectID, target.Kind, target.ResourceID, row.ID)
	if err != nil {
		failf("%s", accessFailure(err, "rotate the credential"))
		return
	}
	fmt.Printf("✅ Rotated '%s' on %s '%s'\n", row.Name, target.Kind, target.ResourceName)
	printCredential(target, row.Name, out)
}

func confirmAccessRotate(target *accessTarget, principal string, yes bool, ask func() string) bool {
	if yes {
		return true
	}
	fmt.Printf("⚠️  This replaces the secret '%s' uses for %s '%s' now: the old one stops working immediately and whatever holds it has to be updated. Continue? [y/N] ",
		principal, target.Kind, target.ResourceName)
	answer := strings.TrimSpace(ask())
	return answer == "y" || answer == "Y"
}

func runAccessAllow(cmd *cobra.Command, args []string) {
	if !cmd.Flags().Changed("set") {
		failf("Pass the whole list with --set (an empty --set \"\" clears it): ghayma access allow %s %s %s --set <cidr,...>", args[0], args[1], args[2])
		return
	}
	client := accessClient()
	if client == nil {
		return
	}
	target, row, rows, ok := resolvePrincipal(client, args[0], args[1], args[2])
	if !ok {
		return
	}
	cidrs := parseCIDRList(accessAllowSet)

	updated, err := client.SetExternalAccessAllowlist(target.ProjectID, target.Kind, target.ResourceID, row.ID, cidrs)
	if err != nil {
		failf("%s", accessFailure(err, "set the allowlist"))
		return
	}
	if len(updated.AllowCIDRs) == 0 {
		fmt.Printf("✅ Allowlist cleared for '%s' on %s '%s' — any source may use its credential.\n", row.Name, target.Kind, target.ResourceName)
	} else {
		fmt.Printf("✅ Allowlist for '%s' on %s '%s': %s\n", row.Name, target.Kind, target.ResourceName, strings.Join(updated.AllowCIDRs, ", "))
	}
	// The collapse rule is about every principal at once, so it is re-stated
	// from the rows as they now stand — the changed one substituted in.
	for i := range rows {
		if rows[i].ID == updated.ID {
			rows[i] = *updated
		}
	}
	if note := collapseNote(rows); note != "" {
		fmt.Println("   " + note)
	}
}

func init() {
	accessCmd.Flags().BoolVar(&accessJSON, "json", false, "Print the rows as JSON")

	accessAddCmd.Flags().StringVar(&accessAddName, "name", "", "Name of the principal receiving the credential (required)")
	accessAddCmd.Flags().StringVar(&accessAddLevel, "level", "", "Access level (database: connect|read-only; bucket: read-write|read). Default: the kind's full level")
	accessAddCmd.Flags().StringVar(&accessAddAllow, "allow", "", "Comma-separated source CIDRs or addresses. Default: any source")
	accessAddCmd.Flags().IntVar(&accessAddDays, "expires", 0, "Revoke the principal automatically after this many days. Default: no expiry")

	accessRevokeCmd.Flags().BoolVar(&accessRevokeYes, "yes", false, "Skip the confirmation")
	accessRotateCmd.Flags().BoolVar(&accessRotateYes, "yes", false, "Skip the confirmation")
	accessAllowCmd.Flags().StringVar(&accessAllowSet, "set", "", "The whole allowlist, comma-separated. An empty --set \"\" clears it")

	accessCmd.AddCommand(accessAddCmd)
	accessCmd.AddCommand(accessRevokeCmd)
	accessCmd.AddCommand(accessRotateCmd)
	accessCmd.AddCommand(accessAllowCmd)
	rootCmd.AddCommand(accessCmd)
}
