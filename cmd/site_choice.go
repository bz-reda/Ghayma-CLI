package cmd

import (
	"errors"
	"fmt"
	"strings"

	"paas-cli/internal/api"

	"github.com/manifoldco/promptui"
)

// Ask before connecting (2026-10-02). The platform connects a new database,
// bucket or auth app to no site on its own: the create carries the sites the
// user chose — --site, --no-connect, or one question per site on a terminal —
// and its response says what became of each.

// promptConnectSiteFn is indirected so tests can answer the questions without
// a TTY.
var promptConnectSiteFn = promptConnectSite

// promptConnectSite asks one No/Yes question with the default answer first. A
// Select rather than a y/n confirm, so "No" is never mistaken for a cancel.
func promptConnectSite(label string, defaultYes bool) (bool, error) {
	items := yesNoItems(defaultYes)
	sel := promptui.Select{Label: label, Items: items}
	idx, _, err := sel.Run()
	if err != nil {
		return false, err
	}
	return items[idx] == "Yes", nil
}

// yesNoItems lists a No/Yes question's answers with the default first: the
// Select opens on it, so Enter alone takes the default.
func yesNoItems(defaultYes bool) []string {
	if defaultYes {
		return []string{"Yes", "No"}
	}
	return []string{"No", "Yes"}
}

// checkConnectFlags refuses the contradictory pair before anything reaches
// the API.
func checkConnectFlags(siteFlags []string, noConnect bool) error {
	if len(siteFlags) > 0 && noConnect {
		return errors.New("--site and --no-connect cannot be used together")
	}
	return nil
}

// resolveSiteChoice decides which sites a new service is connected to. It runs
// before the create, so a refused choice never leaves a half-made resource.
// kind is the API kind and name what `ghayma connect` calls the service. It
// returns the site ids to send, the project's sites when it listed them (to
// name the outcome) and a note to print after the create. A cancelled question
// is errAttachCancelled.
func resolveSiteChoice(client *api.Client, projectID string, siteFlags []string, noConnect bool, kind, name string) (ids []string, sites []api.Site, note string, err error) {
	if err = checkConnectFlags(siteFlags, noConnect); err != nil {
		return nil, nil, "", err
	}
	if noConnect {
		return []string{}, nil, "", nil
	}
	if len(siteFlags) == 0 && !stdinIsTerminalFn() {
		return []string{}, nil, "Not connected. Connect it with: " + connectCommand(kind, name, "<slug>"), nil
	}
	sites, err = client.ListSites(projectID)
	if err != nil {
		return nil, nil, "", err
	}
	if len(siteFlags) > 0 {
		ids, err = siteIDsByFlag(sites, siteFlags)
		return ids, sites, "", err
	}
	ids, note, err = askConnectSites(sites, kind, name)
	return ids, sites, note, err
}

// siteIDsByFlag maps each --site onto the project's sites by slug, name or id,
// once each and in flag order. An unknown one is refused with the site list.
func siteIDsByFlag(sites []api.Site, siteFlags []string) ([]string, error) {
	ids := make([]string, 0, len(siteFlags))
	seen := make(map[string]bool, len(siteFlags))
	for _, flag := range siteFlags {
		site, err := matchSite(sites, flag)
		if err != nil {
			return nil, err
		}
		if !seen[site.ID] {
			seen[site.ID] = true
			ids = append(ids, site.ID)
		}
	}
	return ids, nil
}

// matchSite finds the site a --site value names. Every site's slug is tried
// before any name, and names before ids, so a display name never wins over
// another site's slug. An unknown value is refused with the site list.
func matchSite(sites []api.Site, flag string) (*api.Site, error) {
	keys := []func(api.Site) string{
		func(s api.Site) string { return s.Slug },
		func(s api.Site) string { return s.Name },
		func(s api.Site) string { return s.ID },
	}
	for _, key := range keys {
		for i := range sites {
			if value := key(sites[i]); value != "" && strings.EqualFold(value, flag) {
				return &sites[i], nil
			}
		}
	}
	if len(sites) == 0 {
		return nil, fmt.Errorf("site %q not found — this project has no site yet", flag)
	}
	return nil, fmt.Errorf("site %q not found in this project (available: %s)", flag, strings.Join(siteSlugs(sites), ", "))
}

// askConnectSites puts the question on a terminal, one site at a time. A
// project without a site has nothing to ask yet.
func askConnectSites(sites []api.Site, kind, name string) ([]string, string, error) {
	if len(sites) == 0 {
		return []string{}, "No site yet — `ghayma deploy` will offer to connect it.", nil
	}
	ids := []string{}
	for _, s := range sites {
		label, defaultYes := connectQuestion(s, len(sites), kind, name)
		yes, err := promptConnectSiteFn(label, defaultYes)
		if err != nil {
			return nil, "", errAttachCancelled
		}
		if yes {
			ids = append(ids, s.ID)
		}
	}
	return ids, "", nil
}

// connectQuestion is the question for one site and its default answer: a
// lone site is named with the service and defaults to Yes; among several, each
// carries its environment and defaults to No.
func connectQuestion(s api.Site, siteCount int, kind, name string) (string, bool) {
	if siteCount == 1 {
		return fmt.Sprintf("Connect %s '%s' to site '%s'?", kindLabel(kind), name, s.Slug), true
	}
	if s.Environment == "" {
		return fmt.Sprintf("Connect to '%s'?", s.Slug), false
	}
	return fmt.Sprintf("Connect to '%s' (%s)?", s.Slug, s.Environment), false
}

// connectCommand is the `ghayma connect` line for one service, in connect's
// own syntax: the auth_app kind is typed "auth".
func connectCommand(kind, name, site string) string {
	if kind == "auth_app" {
		kind = "auth"
	}
	return fmt.Sprintf("ghayma connect %s %s --site %s", kind, name, site)
}

// reportSiteChoiceError prints why a create stopped before anything was made.
// A cancelled question reads like the pricing pickers' cancel.
func reportSiteChoiceError(err error) {
	if errors.Is(err, errAttachCancelled) {
		fmt.Println("❌ Cancelled.")
		return
	}
	failf("%v", err)
}

// printConnectOutcome says what the create did with the chosen sites, naming
// each by slug (by id when the listing lacks it), then the note. When the
// response accounts for none of the chosen sites — an older server, or a shape
// this CLI cannot read — it says where to look instead of saying nothing.
func printConnectOutcome(outcome api.ConnectChoice, chosen []string, sites []api.Site, note string) {
	for _, id := range outcome.Connected {
		fmt.Printf("🔗 Connected to '%s'\n", siteSlugByID(sites, id))
	}
	for _, id := range outcome.Pending {
		fmt.Printf("⏳ Connecting to '%s' once the database accepts connections\n", siteSlugByID(sites, id))
	}
	for _, f := range outcome.Failed {
		fmt.Printf("⚠️  Not connected to '%s': %s\n", siteSlugByID(sites, f.SiteID), f.Error)
	}
	if note != "" {
		fmt.Printf("ℹ️  %s\n", note)
	}
	if len(chosen) > 0 && !outcomeMentions(outcome, chosen) {
		fmt.Println(unreadableConnectResult)
	}
}

// unreadableConnectResult is said when a response accounts for none of the
// chosen connections.
const unreadableConnectResult = "⚠️  couldn't read the connection result — check with: ghayma connections"

func siteSlugByID(sites []api.Site, id string) string {
	for _, s := range sites {
		if s.ID == id {
			return s.Slug
		}
	}
	return id
}

// outcomeMentions reports whether the outcome accounts for any chosen site.
func outcomeMentions(outcome api.ConnectChoice, chosen []string) bool {
	mentioned := make(map[string]bool)
	for _, id := range outcome.Connected {
		mentioned[id] = true
	}
	for _, id := range outcome.Pending {
		mentioned[id] = true
	}
	for _, f := range outcome.Failed {
		mentioned[f.SiteID] = true
	}
	for _, id := range chosen {
		if mentioned[id] {
			return true
		}
	}
	return false
}
