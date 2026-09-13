package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"paas-cli/internal/api"
)

// Projects without a site (2026-09-12). A Ghayma project is born site-less and
// the backend materializes `main` lazily, so a customer who only uses
// databases, storage and auth — a mobile app, an external backend — never needs
// one. init/link can now say so, which makes "no site" a first-class config
// state every site-scoped command has to explain rather than trip over.

// noSiteMessage is the one line every site-scoped command prints on a site-less
// project, so the advice never drifts between commands.
const noSiteMessage = "ℹ️  This project has no site yet. Create one with: ghayma site create <name>"

// errNoSite marks a site-less project. A sentinel, so callers tell it apart
// from a real failure and print noSiteMessage instead of the "❌ <err>" form.
var errNoSite = errors.New("this project has no site yet")

// exitFn is indirected so tests can assert the non-zero exit without killing
// the test binary.
var exitFn = os.Exit

// hasSite reports whether a config entry names a site at all. site_name alone
// counts: init has written `"site_name": "main"` with an empty site_id ever
// since the backend started creating main lazily (2026-07-23), and that config
// means "the main site". Only the fully empty triple means no site.
func hasSite(entry SiteEntry) bool {
	return entry.SiteID != "" || entry.SiteName != "" || entry.SiteSlug != ""
}

// failNoSite prints the site-less line and exits 1. Callers return immediately
// after so the stubbed exit in tests cannot run on past it.
func failNoSite() {
	fmt.Println(noSiteMessage)
	exitFn(1)
}

// reportSiteError prints a site-resolution failure: a site-less project gets
// its own informational line and a non-zero exit, everything else keeps the
// ❌ form these commands have always used.
func reportSiteError(err error) {
	if errors.Is(err, errNoSite) {
		failNoSite()
		return
	}
	fmt.Printf("❌ %v\n", err)
}

// printNoSiteNextSteps is what init/link print after writing a site-less
// config: the three things such a project is for, and how to add a site later.
func printNoSiteNextSteps() {
	fmt.Println("\nNext: ghayma db create <name>   ghayma storage create <name>   ghayma auth create <name>")
	fmt.Println("      Add a site later with: ghayma site create <name>")
}

// localConfigIsSiteLess reports whether the nearest project config is a per-app
// config naming no site. That is NOT proof of a site-less project: configs
// written before 2026-07-23 carry no site keys either, so a command must
// confirm with projectHasNoSites / resolveSiteLess before telling the user.
// A workspace manifest answers false: it pins no single site by design, and
// the commands reading it that way are legitimately project-wide.
func localConfigIsSiteLess() bool {
	data, err := readProjectConfigUp(".")
	if err != nil || isManifest(data) {
		return false
	}
	var cfg perAppConfig
	if json.Unmarshal(data, &cfg) != nil {
		return false
	}
	return !hasSite(cfg.SiteEntry)
}

// projectHasNoSites is the live half of the site-less check. A config naming
// no site is either a project created without one or a config written before
// configs carried site keys (init before 2026-07-23), and only the project's
// site list tells them apart: no sites at all is the site-less project.
func projectHasNoSites(client *api.Client, projectID string) (bool, error) {
	sites, err := client.ListSites(projectID)
	if err != nil {
		return false, err
	}
	return len(sites) == 0, nil
}

// resolveSiteLess resolves a config that names no site through the live list:
// errNoSite when the project has none, else --site, the only site, or an
// error asking for --site.
func resolveSiteLess(client *api.Client, projectID, siteFlag string) (*api.Site, error) {
	sites, err := client.ListSites(projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to list sites: %v", err)
	}
	return liveSiteFor(sites, siteFlag)
}
