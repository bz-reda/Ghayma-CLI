package cmd

import (
	"fmt"
	"strings"

	"paas-cli/internal/api"
)

// Ask before connecting, at deploy (2026-10-02). A service no site holds is
// offered to the site being deployed, one question each on a terminal, before
// the upload starts; the answers ride on the upload, so the first build already
// has the variables. A script, --no-connect or a No is told how to connect it.

// promptDeployConnectFn is indirected so tests can answer the questions
// without a TTY.
var promptDeployConnectFn = promptDeployConnect

// promptDeployConnect asks the create commands' No/Yes question, Yes first.
func promptDeployConnect(label string) (bool, error) {
	return promptConnectSiteFn(label, true)
}

// deployTargetSite names the site this deploy lands on: the linked site, or
// main, which the first deploy of a site-less project creates.
func deployTargetSite(ctx *SiteContext) string {
	if ctx.NoSite {
		return "main"
	}
	return siteLabel(ctx.Site)
}

// chooseDeployConnections picks the unconnected services the upload connects
// to the deploy's site: those answered Yes on a terminal, none otherwise. A
// failed listing (project keys are refused it) costs a warning line, never the
// deploy. A cancelled question is errAttachCancelled.
func chooseDeployConnections(client *api.Client, ctx *SiteContext, interactive bool) ([]api.Unconnected, error) {
	services, err := client.ListUnconnected(ctx.ProjectID)
	if err != nil {
		fmt.Printf("⚠️  couldn't check for services no site uses: %v\n", err)
		return nil, nil
	}
	site := deployTargetSite(ctx)
	var chosen, declined []api.Unconnected
	for _, s := range services {
		yes := false
		if interactive {
			if yes, err = promptDeployConnectFn(deployConnectQuestion(s, site)); err != nil {
				return nil, errAttachCancelled
			}
		}
		if yes {
			chosen = append(chosen, s)
		} else {
			declined = append(declined, s)
		}
	}
	printNotConnected(declined, site)
	return chosen, nil
}

func deployConnectQuestion(s api.Unconnected, site string) string {
	return fmt.Sprintf("%s '%s' isn't connected to any site. Connect it to '%s'?", capitalize(kindLabel(s.Kind)), s.ResourceName, site)
}

// printNotConnected lists the services the deploy leaves unconnected, each
// with the command that connects it to the deploy's site.
func printNotConnected(services []api.Unconnected, site string) {
	if len(services) == 0 {
		return
	}
	named := make([]string, len(services))
	for i, s := range services {
		named[i] = fmt.Sprintf("%s '%s'", kindLabel(s.Kind), s.ResourceName)
	}
	fmt.Printf("ℹ️  Not connected to any site: %s.\n", strings.Join(named, ", "))
	fmt.Println("   Connect with:")
	for _, s := range services {
		fmt.Printf("     %s\n", connectCommand(s.Kind, s.ResourceName, site))
	}
}

// connectionItems is the upload's connect_resources. No level is named, so
// each service gets its kind's default.
func connectionItems(chosen []api.Unconnected) []api.ConnectionItem {
	items := make([]api.ConnectionItem, len(chosen))
	for i, s := range chosen {
		items[i] = api.ConnectionItem{Kind: s.Kind, ResourceID: s.ResourceID}
	}
	return items
}

// printDeployConnections says what the upload did with the chosen services,
// named as the listing named them (by id otherwise). An answer accounting for
// none of them — an older server — says where to look instead.
func printDeployConnections(outcome api.DeployConnections, chosen []api.Unconnected, site string) {
	if len(chosen) == 0 {
		return
	}
	names := make(map[string]string, len(chosen))
	for _, s := range chosen {
		names[s.ResourceID] = s.ResourceName
	}
	nameOf := func(id string) string {
		if name, ok := names[id]; ok {
			return name
		}
		return id
	}
	for _, c := range outcome.Connected {
		fmt.Printf("🔗 Connected %s '%s' to '%s'.\n", kindLabel(c.Kind), nameOf(c.ResourceID), site)
	}
	for _, f := range outcome.Failed {
		fmt.Printf("⚠️  Couldn't connect %s '%s': %s\n", kindLabel(f.Kind), nameOf(f.ResourceID), f.Error)
	}
	if !deployOutcomeMentions(outcome, names) {
		fmt.Println(unreadableConnectResult)
	}
}

// deployOutcomeMentions reports whether the outcome accounts for any chosen
// service (chosen is keyed by resource id).
func deployOutcomeMentions(outcome api.DeployConnections, chosen map[string]string) bool {
	for _, c := range outcome.Connected {
		if _, ok := chosen[c.ResourceID]; ok {
			return true
		}
	}
	for _, f := range outcome.Failed {
		if _, ok := chosen[f.ResourceID]; ok {
			return true
		}
	}
	return false
}
