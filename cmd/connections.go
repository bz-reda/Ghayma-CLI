package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

var (
	connectionsSite string
	connectionsJSON bool
)

var connectionsCmd = &cobra.Command{
	Use:   "connections",
	Short: "List which apps may use which services",
	Long: `List the project's connections: which site (app) may use which database,
bucket or auth app, and at what level.

A connection is what hands an app its variables (DATABASE_URL, STORAGE_*,
the ESPACETECH_AUTH_* set, GHAYMA_API_KEY), opens the network path to a
database, and shapes the app's managed platform key. Change one with
'ghayma connect' and 'ghayma disconnect'.

Without --site every site of the linked project is listed. --json prints the
rows exactly as the API returns them.

Examples:
  ghayma connections                 # every site of this project
  ghayma connections --site admin    # one site
  ghayma connections --json`,
	Args: cobra.NoArgs,
	Run:  runConnections,
}

// connectionTarget is the resolved (project, site) a connections command acts
// on, plus the directory that holds the site's code.
type connectionTarget struct {
	ProjectID   string
	ProjectName string
	Site        api.Site
	AppDir      string
}

// failf prints the ❌ line every command uses and exits 1, so a script can
// rely on the exit code. Tests stub exitFn.
func failf(format string, args ...interface{}) {
	fmt.Printf("❌ "+format+"\n", args...)
	exitFn(1)
}

// resolveConnectionTarget resolves the site a command acts on through the
// ladder every site-scoped command uses — the linked site of this directory,
// the workspace entry, --site, or a picker at a workspace root — then maps it
// onto a LIVE site of the project (pickLiveSite). The site-less and cancelled
// cases become the errors reportSiteError already knows how to print.
func resolveConnectionTarget(client *api.Client, siteFlag, verb string) (*connectionTarget, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	ctx, err := resolveSiteContext(cwd, siteFlag, verb)
	switch {
	case errors.Is(err, errAttachCancelled):
		return nil, errors.New("Cancelled")
	case errors.Is(err, errNoProjectConfig):
		return nil, errors.New("no project config found — run 'ghayma init' first")
	case err != nil:
		return nil, err
	}
	if ctx.NoSite {
		return nil, errNoSite
	}
	sites, err := client.ListSites(ctx.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to list sites: %v", err)
	}
	site, err := pickLiveSite(sites, ctx.Site)
	if err != nil {
		return nil, err
	}
	return &connectionTarget{ProjectID: ctx.ProjectID, ProjectName: ctx.ProjectName, Site: *site, AppDir: appDirOf(ctx.SourceDir, ctx.RootDirectory)}, nil
}

func runConnections(cmd *cobra.Command, args []string) {
	cfg := config.Load()
	if !cfg.LoggedIn() {
		failf("Please login first: ghayma login")
		return
	}
	client := api.NewClient(cfg)

	var projectID, projectName, siteID string
	if connectionsSite != "" {
		target, err := resolveConnectionTarget(client, connectionsSite, "list connections for")
		if err != nil {
			reportSiteError(err)
			exitFn(1)
			return
		}
		projectID, projectName, siteID = target.ProjectID, target.ProjectName, target.Site.ID
	} else {
		// Project-wide: no site to pick, so the nearest config is enough — a
		// workspace manifest included.
		var err error
		projectID, _, projectName, err = localConfig()
		if err != nil {
			failf("%v", err)
			return
		}
	}

	rows, err := client.ListConnections(projectID, siteID)
	if err != nil {
		failf("Failed to list connections: %v", err)
		return
	}
	printConnections(projectName, rows, connectionsJSON)
}

// printConnections renders the listing: the API rows as JSON, or the table
// with the project's name above it.
func printConnections(projectName string, rows []api.Connection, asJSON bool) {
	if asJSON {
		if rows == nil {
			rows = []api.Connection{}
		}
		b, _ := json.MarshalIndent(rows, "", "  ")
		fmt.Println(string(b))
		return
	}
	if len(rows) == 0 {
		fmt.Println("No connections yet.")
		fmt.Println("   Connect a service with: ghayma connect <database|bucket|auth> <name>")
		return
	}
	fmt.Printf("🔗 Connections for %s:\n\n", projectName)
	fmt.Print(renderConnectionsTable(rows))
}

func init() {
	connectionsCmd.Flags().StringVar(&connectionsSite, "site", "", "Only this site (name or slug)")
	connectionsCmd.Flags().BoolVar(&connectionsJSON, "json", false, "Print the rows as JSON")
	rootCmd.AddCommand(connectionsCmd)
}
