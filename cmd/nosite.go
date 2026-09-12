package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
// config naming no site — a project created without one. A workspace manifest
// answers false: it pins no single site by design, and the commands reading it
// that way are legitimately project-wide.
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
