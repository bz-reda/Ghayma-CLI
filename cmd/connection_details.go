package cmd

import (
	"fmt"
	"strings"

	"paas-cli/internal/api"
)

// No shared credentials, part 4 (2026-10-08): a database's own login and a
// bucket's own key are never handed out. `db credentials` and `storage
// credentials` show where the service is and which variables each connected
// site receives; every site holds its own credential.

// serviceConnections returns the project's connections to one service.
func serviceConnections(client *api.Client, projectID, kind, resourceID string) ([]api.Connection, error) {
	all, err := client.ListConnections(projectID, "")
	if err != nil {
		return nil, err
	}
	var rows []api.Connection
	for _, c := range all {
		if c.Kind == kind && c.ResourceID == resourceID {
			rows = append(rows, c)
		}
	}
	return rows, nil
}

// printConnectedSites lists each connected site with its level and variable
// names, or says how to connect one.
func printConnectedSites(rows []api.Connection, kind, name string) {
	if len(rows) == 0 {
		fmt.Printf("   Not connected to any site. Connect one with: ghayma connect %s %s --site <slug>\n", kind, name)
		return
	}
	slugWidth, levelWidth := 0, 0
	for _, r := range rows {
		slugWidth = max(slugWidth, len(r.SiteSlug))
		levelWidth = max(levelWidth, len(r.Level))
	}
	fmt.Println("   Connected sites and the variables they receive:")
	for _, r := range rows {
		line := fmt.Sprintf("     %-*s  %-*s  %s", slugWidth, r.SiteSlug, levelWidth, r.Level, strings.Join(r.EnvNames, ", "))
		fmt.Println(strings.TrimRight(line, " "))
	}
}

// nameOrPlaceholder is the service named on the command line, or "<name>".
func nameOrPlaceholder(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return "<name>"
}
