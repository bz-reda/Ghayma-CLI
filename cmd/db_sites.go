package cmd

import (
	"fmt"
	"strings"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

var (
	dbSitesAdd    string
	dbSitesRemove string
	dbSitesSet    string
)

var dbSitesCmd = &cobra.Command{
	Use:   "sites <db-name>",
	Short: "View or set which sites may reach a database",
	Long: `View or set which of a project's sites may reach a managed database.

Each managed database enforces a per-database network policy: only the granted
sites can open a connection to it. A newly linked database grants its project's
main site by default; secondary sites must be granted explicitly.

With no flag, prints the current access table (SITE = slug, NAME, ACCESS where
✓ = allowed, - = blocked). The three mutation flags are mutually exclusive:

  --add     grant the given site(s), keeping the rest
  --remove  revoke the given site(s), keeping the rest
  --set     replace the whole set with exactly the given site(s)

Sites are named by slug (the SITE column), comma-separated. Adding a site that
already has access, or removing one that already doesn't, is a no-op — reported,
not an error. --set needs at least one site; to revoke ALL access, --remove the
currently-granted sites (an empty set is deny-all).

Examples:
  ghayma db sites mydb                     # show the access table
  ghayma db sites mydb --add admin         # also let the 'admin' site reach it
  ghayma db sites mydb --add admin,api     # grant two sites at once
  ghayma db sites mydb --remove admin      # stop the 'admin' site reaching it
  ghayma db sites mydb --set main,admin    # exactly main + admin, nothing else`,
	Args: requireOneArg("db-name", "db list"),
	Run:  runDBSites,
}

func runDBSites(cmd *cobra.Command, args []string) {
	cfg := config.Load()
	if cfg.Token == "" {
		fmt.Println("❌ Please login first: ghayma login")
		return
	}

	// The three mutation flags are mutually exclusive; detect which (if any)
	// was passed via Changed so an explicit empty value still registers.
	op, raw, changed := "", "", 0
	if cmd.Flags().Changed("add") {
		op, raw, changed = "add", dbSitesAdd, changed+1
	}
	if cmd.Flags().Changed("remove") {
		op, raw, changed = "remove", dbSitesRemove, changed+1
	}
	if cmd.Flags().Changed("set") {
		op, raw, changed = "set", dbSitesSet, changed+1
	}
	if changed > 1 {
		fmt.Println("❌ --add, --remove, and --set are mutually exclusive; pass only one.")
		return
	}

	client := api.NewClient(cfg)
	db, err := findDatabaseByName(client, args[0])
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		return
	}

	current, err := client.ListDatabaseSites(db.ID)
	if err != nil {
		fmt.Printf("❌ Failed to load site access: %v\n", err)
		return
	}

	// An unlinked database has no reachable sites (the server returns an empty
	// list). Neither viewing nor changing access is meaningful until it's linked.
	if len(current) == 0 {
		fmt.Printf("🗄️  '%s' is not linked to a project — no site can reach it.\n", args[0])
		fmt.Printf("   Link it to a project first: ghayma db link %s\n", args[0])
		return
	}

	// No flag → list mode.
	if op == "" {
		fmt.Printf("🗄️  Site access for '%s':\n\n", args[0])
		printDBSitesTable(current)
		fmt.Println("\n   ✓ = allowed   - = blocked")
		return
	}

	desired, noops, err := computeDesiredSites(current, op, splitSlugs(raw))
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		return
	}

	// Idempotent-friendly: name each slug that was already in the target state.
	for _, sl := range noops {
		if op == "add" {
			fmt.Printf("ℹ️  '%s' already has access — no change.\n", sl)
		} else {
			fmt.Printf("ℹ️  '%s' already has no access — no change.\n", sl)
		}
	}

	// Nothing to send when the desired set already matches — skip the PUT and
	// the convergence hint (no policy actually changes).
	if sameStringSet(desired, grantedIDs(current)) {
		fmt.Printf("🗄️  Site access for '%s' (unchanged):\n\n", args[0])
		printDBSitesTable(current)
		return
	}

	updated, err := client.SetDatabaseSites(db.ID, desired)
	if err != nil {
		fmt.Printf("❌ Failed to update site access: %v\n", err)
		return
	}

	fmt.Printf("✅ Updated site access for '%s':\n\n", args[0])
	printDBSitesTable(updated)
	fmt.Println("\n⏳ Network policy converges in ~2 minutes.")
}

// printDBSitesTable renders the SITE / NAME / ACCESS table. ACCESS is ✓ when the
// site may reach the database, - when it's blocked.
func printDBSitesTable(sites []api.DatabaseSiteAccess) {
	fmt.Printf("   %-20s  %-20s  %s\n", "SITE", "NAME", "ACCESS")
	for _, s := range sites {
		access := "-"
		if s.HasAccess {
			access = "✓"
		}
		fmt.Printf("   %-20s  %-20s  %s\n", s.Slug, s.Name, access)
	}
}

// computeDesiredSites computes the FULL desired set of site IDs to PUT for a
// `db sites` mutation. current is every project site with its has_access flag
// (the GET response, which carries the whole project so slugs resolve to IDs
// without another endpoint). op is "add", "remove", or "set". slugs are the
// user-supplied site slugs (already split + trimmed). It returns:
//   - desired: the site IDs to send, ordered by project order (deterministic)
//   - noops:   slugs that changed nothing (already granted for add / already
//     ungranted for remove); always nil for set
//   - err:     names an unknown slug plus the valid slugs, or (for set) an empty
//     slug list
//
// It is a pure function (no network) so the add/remove/set logic is unit-tested
// directly.
func computeDesiredSites(current []api.DatabaseSiteAccess, op string, slugs []string) (desired []string, noops []string, err error) {
	bySlug := make(map[string]api.DatabaseSiteAccess, len(current))
	for _, s := range current {
		bySlug[s.Slug] = s
	}

	// Reject unknown slugs up front, naming them and the valid choices.
	var unknown []string
	for _, sl := range slugs {
		if _, ok := bySlug[sl]; !ok {
			unknown = append(unknown, sl)
		}
	}
	if len(unknown) > 0 {
		return nil, nil, fmt.Errorf("unknown site(s): %s; valid sites: %s",
			strings.Join(unknown, ", "), strings.Join(dbSiteSlugs(current), ", "))
	}

	granted := make(map[string]bool)
	for _, s := range current {
		if s.HasAccess {
			granted[s.SiteID] = true
		}
	}

	switch op {
	case "set":
		if len(slugs) == 0 {
			return nil, nil, fmt.Errorf("--set needs at least one site (to revoke all access, --remove the granted sites)")
		}
		want := make(map[string]bool, len(slugs))
		for _, sl := range slugs {
			want[bySlug[sl].SiteID] = true
		}
		return idsInProjectOrder(current, want), nil, nil

	case "add":
		want := copySet(granted)
		for _, sl := range slugs {
			id := bySlug[sl].SiteID
			if granted[id] {
				noops = append(noops, sl)
				continue
			}
			want[id] = true
		}
		return idsInProjectOrder(current, want), noops, nil

	case "remove":
		want := copySet(granted)
		for _, sl := range slugs {
			id := bySlug[sl].SiteID
			if !granted[id] {
				noops = append(noops, sl)
				continue
			}
			delete(want, id)
		}
		return idsInProjectOrder(current, want), noops, nil

	default:
		return nil, nil, fmt.Errorf("internal: unknown op %q", op)
	}
}

// idsInProjectOrder returns the IDs present in want, ordered by their position
// in current (project order) — a deterministic PUT body and stable test output.
func idsInProjectOrder(current []api.DatabaseSiteAccess, want map[string]bool) []string {
	out := make([]string, 0, len(want))
	for _, s := range current {
		if want[s.SiteID] {
			out = append(out, s.SiteID)
		}
	}
	return out
}

// grantedIDs returns the site IDs whose has_access flag is set, in project order.
func grantedIDs(current []api.DatabaseSiteAccess) []string {
	out := make([]string, 0, len(current))
	for _, s := range current {
		if s.HasAccess {
			out = append(out, s.SiteID)
		}
	}
	return out
}

// dbSiteSlugs lists the slugs of every project site, for the unknown-slug hint.
func dbSiteSlugs(current []api.DatabaseSiteAccess) []string {
	out := make([]string, len(current))
	for i, s := range current {
		out[i] = s.Slug
	}
	return out
}

// splitSlugs splits a comma-separated flag value into trimmed, non-empty slugs.
func splitSlugs(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// sameStringSet reports order-insensitive set equality, used to detect a no-op
// PUT (desired already matches the currently-granted set).
func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]bool, len(a))
	for _, x := range a {
		seen[x] = true
	}
	for _, x := range b {
		if !seen[x] {
			return false
		}
	}
	return true
}

func copySet(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func init() {
	dbSitesCmd.Flags().StringVar(&dbSitesAdd, "add", "", "Grant the given site slug(s) (comma-separated), keeping the rest")
	dbSitesCmd.Flags().StringVar(&dbSitesRemove, "remove", "", "Revoke the given site slug(s) (comma-separated), keeping the rest")
	dbSitesCmd.Flags().StringVar(&dbSitesSet, "set", "", "Replace the whole set with exactly the given site slug(s) (comma-separated)")
	dbCmd.AddCommand(dbSitesCmd)
}
