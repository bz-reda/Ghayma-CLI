package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
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

// deployTargetSite names the site this deploy lands on, by its live slug. An
// upload naming a site lands on it; one naming none lands where the server
// puts it (serverPickedSite), and a project with no site gets main. ok is
// false when the server has no site for it — several sites with neither a
// default nor main, or a linked site since deleted — as it refuses that
// upload. A list that could not be read (listed false) leaves a linked
// config's slug — its site id when it has no slug — and main otherwise.
func deployTargetSite(ctx *SiteContext, sites []api.Site, listed bool) (string, bool) {
	if id := ctx.Site.SiteID; id != "" {
		if !listed {
			// The id, not a display name: a name is no safe --site value.
			if ctx.Site.SiteSlug != "" {
				return ctx.Site.SiteSlug, true
			}
			return id, true
		}
		for _, s := range sites {
			if s.ID == id {
				return s.Slug, true
			}
		}
		return "", false
	}
	if !listed || len(sites) == 0 {
		return "main", true
	}
	if site := serverPickedSite(sites); site != nil {
		return site.Slug, true
	}
	return "", false
}

// serverPickedSite is where the server deploys an upload naming no site, as
// paas-api's siteresolve.Pick decides: the default site, else main, else the
// only site. nil means several sites and none of them marked.
func serverPickedSite(sites []api.Site) *api.Site {
	for i := range sites {
		if sites[i].IsDefault {
			return &sites[i]
		}
	}
	for i := range sites {
		if sites[i].Slug == "main" {
			return &sites[i]
		}
	}
	if len(sites) == 1 {
		return &sites[0]
	}
	return nil
}

// deployedSiteSlug names the site the server says the upload landed on, by
// its slug in the list read before the upload. One that list lacks — main,
// just created — keeps the name the question used.
func deployedSiteSlug(sites []api.Site, siteID, target string) string {
	for _, s := range sites {
		if s.ID == siteID {
			return s.Slug
		}
	}
	return target
}

// chooseDeployConnections picks the unconnected services the upload connects
// to site: those answered Yes on a terminal, none otherwise or with
// --no-connect. A failed listing never costs the deploy: it is one line
// (printListingFailure), and none with --no-connect. A cancelled question is
// errAttachCancelled.
func chooseDeployConnections(client *api.Client, projectID, site string, interactive, noConnect bool) ([]api.Unconnected, error) {
	services, err := client.ListUnconnected(projectID)
	if err != nil {
		if !noConnect {
			printListingFailure(err)
		}
		return nil, nil
	}
	var chosen, declined []api.Unconnected
	for _, s := range services {
		yes := false
		if interactive && !noConnect {
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

// printListingFailure says why nothing was offered. A project key is refused
// the listing by design and an older server has no such route: neither is
// anything to act on, so both are said as facts; any other failure warns.
func printListingFailure(err error) {
	var apiErr *api.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case http.StatusForbidden:
			fmt.Println("ℹ️  deploying with a project key — connect services from the console or after `ghayma login`")
			return
		case http.StatusNotFound:
			fmt.Println("ℹ️  this server doesn't list unconnected services yet")
			return
		}
	}
	fmt.Printf("⚠️  couldn't check for services no site uses: %v\n", shortReason(err))
}

// shortReason is why a request failed: a transport error drops the method and
// URL it leads with.
func shortReason(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
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
// named as the listing named them (by id otherwise), each failure followed by
// the command that retries it. An answer accounting for none of them — an
// older server — says where to look instead.
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
		fmt.Printf("🔗 Connected %s '%s' to '%s'\n", kindLabel(c.Kind), nameOf(c.ResourceID), site)
	}
	for _, f := range outcome.Failed {
		fmt.Printf("⚠️  Couldn't connect %s '%s': %s\n", kindLabel(f.Kind), nameOf(f.ResourceID), f.Error)
		fmt.Printf("   Retry with: %s\n", connectCommand(f.Kind, nameOf(f.ResourceID), site))
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
