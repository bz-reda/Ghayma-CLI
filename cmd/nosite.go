package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

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

// liveSiteOf maps an already-resolved context onto a LIVE site of the project:
// the linked site, or --site / the only site when the config names none. The
// live list is the authority because a config may name a site by NAME alone
// (init writes `"site_name": "main"` before the backend materializes it) or
// name one that has since been deleted. errNoSite means the project has no
// site at all — a state each caller explains in its own words.
func liveSiteOf(client *api.Client, ctx *SiteContext, siteFlag string) (*api.Site, error) {
	sites, err := client.ListSites(ctx.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to list sites: %v", err)
	}
	if ctx.NoSite {
		// A config naming no site is either a site-less project or one written
		// before configs carried site keys, so the live list decides — and
		// --site applies here, since the offline resolver had nothing to match
		// it against.
		return liveSiteFor(sites, siteFlag)
	}
	return pickLiveSite(sites, ctx.Site)
}

// resolveSiteContextLive is resolveSiteContext for a command holding a client.
// The offline guard only sees the linked site's config keys, so a config that
// links its site by id alone refused `--site main` in main's own directory
// (2026-10-08). That refusal is settled against the live list: a --site naming
// the linked site resolves to it, carrying the live id. An expired session is
// reported as such; anything else, or a list that cannot be read, keeps the
// refusal. An offline pass the live list could contradict is confirmed the
// same way (confirmSiteFlag).
func resolveSiteContextLive(client *api.Client, cwd, siteFlag, verb string) (*SiteContext, error) {
	ctx, err := resolveSiteContext(cwd, siteFlag, verb)
	if err == nil && ctx.siteFlagUnconfirmed {
		return confirmSiteFlag(client, ctx, siteFlag, verb)
	}
	var mismatch *siteFlagMismatchError
	if !errors.As(err, &mismatch) {
		return ctx, err
	}
	linked, linkedErr := resolveSiteContext(cwd, "", verb)
	if linkedErr != nil {
		return nil, err
	}
	sites, listErr := client.ListSites(linked.ProjectID)
	if errors.Is(listErr, api.ErrUnauthorized) {
		return nil, listErr
	}
	if listErr != nil {
		return nil, err
	}
	site := linkedSiteNamed(sites, linked.Site, siteFlag)
	if site == nil {
		return nil, err
	}
	linked.Site.SiteID, linked.Site.SiteSlug, linked.Site.SiteName = site.ID, site.Slug, site.Name
	return linked, nil
}

// confirmSiteFlag settles an offline --site pass against the live list
// (2026-10-09): main displayed as "api" passed `--site api` and deployed main,
// although --site resolves slugs first and another site's slug is api. The
// matched site stands only when the flag names it live, and carries its live
// id; a flag naming no live site keeps today's pass. An expired session is
// reported as such; any other list failure refuses with its cause, since the
// flag path can deploy to production.
func confirmSiteFlag(client *api.Client, ctx *SiteContext, siteFlag, verb string) (*SiteContext, error) {
	sites, err := client.ListSites(ctx.ProjectID)
	if errors.Is(err, api.ErrUnauthorized) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("could not confirm which site --site %s names: %v", siteFlag, err)
	}
	named, err := matchSite(sites, siteFlag)
	if err != nil {
		return ctx, nil
	}
	linked := configuredLiveSite(sites, ctx.Site)
	switch {
	case ctx.workspaceSites != nil && (linked == nil || linked.ID != named.ID):
		return nil, workspaceSiteFlagError(ctx.workspaceSites, ctx.Site, sites, siteFlag, named, linked)
	case linked == nil:
		return nil, errLinkedSiteGone
	case linked.ID != named.ID:
		return nil, &siteFlagMismatchError{entry: ctx.Site, verb: verb}
	}
	ctx.Site.SiteID, ctx.Site.SiteSlug, ctx.Site.SiteName = named.ID, named.Slug, named.Name
	return ctx, nil
}

// workspaceSiteFlagError refuses a workspace-root --site that matched one
// manifest entry offline but names another live site. A site the manifest
// does not list is said plainly, with the slug that picks the entry and how
// to add the named site. When no
// slug would pick it (the entry's slug is missing or stale), or the site is
// listed under an entry without its slug, the manifest is out of date.
func workspaceSiteFlagError(entries []SiteEntry, matched SiteEntry, sites []api.Site, siteFlag string, named, linked *api.Site) error {
	listed := false
	for _, entry := range entries {
		if site := configuredLiveSite(sites, entry); site != nil && site.ID == named.ID {
			listed = true
			break
		}
	}
	if !listed && linked != nil && strings.EqualFold(matched.SiteSlug, linked.Slug) {
		return fmt.Errorf("--site %s names site %q, which this workspace's manifest does not list; the manifest entry named %q is site %q — pass --site %s for it; to use site %q, run 'ghayma link' from the workspace root to add it", siteFlag, named.Slug, siteFlag, linked.Slug, linked.Slug, named.Slug)
	}
	return fmt.Errorf("--site %s names site %q, but this workspace's manifest matches %q to another of its sites — run 'ghayma link' from the workspace root to refresh it", siteFlag, named.Slug, siteFlag)
}
