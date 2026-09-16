package cmd

import (
	"errors"
	"fmt"
	"strings"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

var (
	connectSite       string
	connectLevel      string
	connectLocal      bool
	connectLocalOut   string
	connectLocalForce bool
	disconnectSite    string
	disconnectYes     bool
)

var connectCmd = &cobra.Command{
	Use:   "connect <database|bucket|auth> <name>",
	Short: "Let this app use a database, bucket or auth app",
	Long: `Connect a site (app) to one of the project's services. The app then
receives the service's variables, the network path to a database opens, and
the app's managed platform key gains what the connection implies — on the
running deployment and at every deploy.

Names: a database's or bucket's name, an auth app's app id. The site is the
one this directory is linked to, or --site. The default level is the kind's
full level (database: connect, bucket: read-write, auth: client);
'database ... --level read-only' and 'bucket ... --level read' narrow the app
to reading, and 'auth ... --level admin' additionally lets the app manage all
of that auth app's users. Running it again with another --level changes the
level.

--local connects the other way round: it tunnels the app's databases to this
machine and writes .env.local (or --out) with the effective variables, their
hosts pointing at the tunnel. Leave it running while you develop; Ctrl-C
closes it. It takes no arguments — pick the app with --site.

Examples:
  ghayma connect database my-postgres
  ghayma connect database my-postgres --level read-only
  ghayma connect bucket uploads --site admin
  ghayma connect auth shop --level admin
  ghayma connect --local`,
	Args: connectArgs,
	Run:  runConnect,
}

// connectArgs enforces the two shapes of the command: --local acts on the site
// as a whole and takes nothing, while the connecting form still needs its kind
// and name.
func connectArgs(cmd *cobra.Command, args []string) error {
	if connectLocal {
		if len(args) != 0 {
			cmd.SilenceUsage, cmd.SilenceErrors = true, true
			return errors.New("ghayma connect --local takes no arguments; use --site <slug> to choose the app")
		}
		return nil
	}
	return argChecker("argument", "connections", 2, 2)(cmd, args)
}

var disconnectCmd = &cobra.Command{
	Use:   "disconnect <database|bucket|auth> <name>",
	Short: "Stop this app using a database, bucket or auth app",
	Long: `Disconnect a site (app) from one of the project's services. Its variables
leave the app, the network path to a database closes, and the app's managed
platform key loses what the connection implied. Asks for confirmation unless
--yes. Disconnecting something already disconnected is not an error.

Examples:
  ghayma disconnect database my-postgres
  ghayma disconnect auth shop --site admin --yes`,
	Args: argChecker("argument", "connections", 2, 2),
	Run:  runDisconnect,
}

// connectPlan is what runConnect will do once the site view is loaded: the
// item to POST, the connection it replaces (a level change), or nothing.
type connectPlan struct {
	Item     api.ConnectionItem
	Held     *api.Connection
	NoChange bool
	Note     string
}

// planConnect turns (kind, name, --level) into a request against the site's
// current view — pure, so the rules are testable without a server.
func planConnect(view *api.SiteConnections, kind, name, levelFlag string) (connectPlan, error) {
	held, avail, err := findConnectable(view, kind, name)
	if err != nil {
		return connectPlan{}, err
	}
	var accepted []string
	if avail != nil {
		accepted = avail.Levels
	}
	level, err := resolveLevel(kind, levelFlag, accepted)
	if err != nil {
		return connectPlan{}, err
	}

	plan := connectPlan{Held: held}
	if held != nil {
		if level == "" || level == held.Level {
			plan.NoChange = true
			return plan, nil
		}
		plan.Item = api.ConnectionItem{Kind: kind, ResourceID: held.ResourceID, Level: level}
	} else {
		plan.Item = api.ConnectionItem{Kind: kind, ResourceID: avail.ResourceID, Level: level}
	}
	if plan.Item.Level == "admin" {
		plan.Note = adminNote
	}
	return plan, nil
}

// capitalize upper-cases the first letter of a label for the start of a line.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// connectedLine is the success line: a new connection, or a level change.
func connectedLine(row, held *api.Connection) string {
	if held != nil {
		return fmt.Sprintf("✅ %s '%s' on '%s': level %s → %s", capitalize(kindLabel(row.Kind)), row.ResourceName, row.SiteSlug, held.Level, row.Level)
	}
	return fmt.Sprintf("✅ Connected %s '%s' to '%s' (%s)", kindLabel(row.Kind), row.ResourceName, row.SiteSlug, row.Level)
}

// injectsLine names the variables the new connection puts into the app, or
// says they are still on their way when the server listed none (a service that
// is still provisioning, or a backend that does not send env_names).
func injectsLine(siteSlug string, names []string) string {
	if len(names) == 0 {
		return "   No variables yet — the platform injects them once the service is ready."
	}
	return fmt.Sprintf("   The platform now injects into '%s': %s", siteSlug, strings.Join(names, ", "))
}

func runConnect(cmd *cobra.Command, args []string) {
	if connectLocal {
		runConnectLocal(cmd, args)
		return
	}
	cfg := config.Load()
	if !cfg.LoggedIn() {
		failf("Please login first: ghayma login")
		return
	}
	kind, err := parseConnectionKind(args[0])
	if err != nil {
		failf("%v", err)
		return
	}
	client := api.NewClient(cfg)
	target, err := resolveConnectionTarget(client, connectSite, "connect a service to")
	if err != nil {
		reportSiteError(err)
		exitFn(1)
		return
	}

	view, err := client.GetSiteConnections(target.ProjectID, target.Site.ID)
	if err != nil {
		failf("Failed to load connections: %v", err)
		return
	}
	plan, err := planConnect(view, kind, args[1], connectLevel)
	if err != nil {
		failf("%v", err)
		return
	}
	if plan.NoChange {
		fmt.Printf("ℹ️  %s '%s' is already connected to '%s' (%s) — no change.\n", kindLabel(kind), plan.Held.ResourceName, target.Site.Slug, plan.Held.Level)
		return
	}
	if plan.Note != "" {
		fmt.Printf("ℹ️  %s\n", plan.Note)
	}

	row, err := client.AddSiteConnection(target.ProjectID, target.Site.ID, plan.Item)
	if err != nil {
		failf("Failed to connect: %v", err)
		return
	}
	if row.SiteSlug == "" {
		row.SiteSlug = target.Site.Slug
	}
	fmt.Println(connectedLine(row, plan.Held))
	if plan.Held == nil {
		fmt.Println(injectsLine(row.SiteSlug, row.EnvNames))
	}
	fmt.Println("   Variables are live on the running app; pull them locally with: ghayma env pull")
}

// confirmDisconnect is the two-step confirm, skipped by --yes. It names the
// variables that leave when the server listed them, and falls back to the
// generic sentence when it did not. ask reads one token from the terminal;
// tests inject it.
func confirmDisconnect(kind, name, siteSlug string, envNames []string, yes bool, ask func() string) bool {
	if yes {
		return true
	}
	if len(envNames) > 0 {
		network := ""
		if kind == "database" {
			network = ", and the network path closes"
		}
		fmt.Printf("⚠️  This stops '%s' using %s '%s': %s leave the app%s. Continue? [y/N] ", siteSlug, kindLabel(kind), name, strings.Join(envNames, ", "), network)
	} else {
		fmt.Printf("⚠️  This stops '%s' using %s '%s': its variables leave the app and, for a database, the network path closes. Continue? [y/N] ", siteSlug, kindLabel(kind), name)
	}
	answer := strings.TrimSpace(ask())
	return answer == "y" || answer == "Y"
}

func readAnswer() string {
	var s string
	fmt.Scanln(&s)
	return s
}

func runDisconnect(cmd *cobra.Command, args []string) {
	cfg := config.Load()
	if !cfg.LoggedIn() {
		failf("Please login first: ghayma login")
		return
	}
	kind, err := parseConnectionKind(args[0])
	if err != nil {
		failf("%v", err)
		return
	}
	client := api.NewClient(cfg)
	target, err := resolveConnectionTarget(client, disconnectSite, "disconnect a service from")
	if err != nil {
		reportSiteError(err)
		exitFn(1)
		return
	}

	view, err := client.GetSiteConnections(target.ProjectID, target.Site.ID)
	if err != nil {
		failf("Failed to load connections: %v", err)
		return
	}
	held, _, err := findConnectable(view, kind, args[1])
	if err != nil {
		failf("%v", err)
		return
	}
	if held == nil {
		fmt.Printf("ℹ️  %s '%s' is not connected to '%s' — no change.\n", kindLabel(kind), args[1], target.Site.Slug)
		return
	}
	if !confirmDisconnect(kind, held.ResourceName, target.Site.Slug, held.EnvNames, disconnectYes, readAnswer) {
		fmt.Println("❌ Cancelled.")
		return
	}

	removed, err := client.RemoveSiteConnection(target.ProjectID, target.Site.ID, kind, held.ResourceID)
	if err != nil {
		failf("Failed to disconnect: %v", err)
		return
	}
	if !removed {
		fmt.Printf("ℹ️  %s '%s' was already disconnected from '%s' — no change.\n", kindLabel(kind), held.ResourceName, target.Site.Slug)
		return
	}
	fmt.Printf("✅ Disconnected %s '%s' from '%s'\n", kindLabel(kind), held.ResourceName, target.Site.Slug)
}

func init() {
	connectCmd.Flags().StringVar(&connectSite, "site", "", "Site (app) to connect, by name or slug")
	connectCmd.Flags().StringVar(&connectLevel, "level", "", "Access level (database: connect|read-only; bucket: read-write|read; auth: client|admin)")
	connectCmd.Flags().BoolVar(&connectLocal, "local", false, "Tunnel the app's databases to this machine and write a dotenv file pointing at them")
	connectCmd.Flags().StringVar(&connectLocalOut, "out", "", "Where --local writes the dotenv file (default: .env.local next to the app)")
	connectCmd.Flags().BoolVar(&connectLocalForce, "force", false, "Let --local write the dotenv file even when git does not ignore it")
	disconnectCmd.Flags().StringVar(&disconnectSite, "site", "", "Site (app) to disconnect, by name or slug")
	disconnectCmd.Flags().BoolVar(&disconnectYes, "yes", false, "Skip the confirmation")
	rootCmd.AddCommand(connectCmd)
	rootCmd.AddCommand(disconnectCmd)
}
