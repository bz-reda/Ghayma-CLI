package cmd

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"paas-cli/internal/api"
	"paas-cli/internal/config"
)

// A site's display name may equal another site's slug (2026-10-09): main is
// named "api" while a second site's slug is api. The offline guard matched
// `--site api` against main's own site_name and acted on main, although
// --site follows matchSite everywhere else — slugs first — so the user meant
// the site whose slug is api.

// collisionSites is p1's live list: main's display name is the other site's slug.
const collisionSites = `[{"id":"s1","name":"api","slug":"main"},{"id":"s2","name":"API","slug":"api"}]`

// collisionJSON is main's directory as link writes it: id, slug and name.
const collisionJSON = `{"project_id":"p1","name":"shop","slug":"shop","site_id":"s1","site_slug":"main","site_name":"api"}`

const sitesRoute = "GET /api/v1/projects/p1/sites"

func listedSites(seen []string) bool {
	return containsPath(seen, sitesRoute)
}

func configDir(t *testing.T, cfg string) string {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{projectConfigName: cfg})
	return dir
}

func TestResolveSiteContextLive_ConfirmsAnOfflinePass(t *testing.T) {
	ownNameSites := `[{"id":"s1","name":"main","slug":"main"},{"id":"s2","name":"Admin","slug":"admin"}]`
	renamedSites := `[{"id":"s1","name":"shop","slug":"shop"},{"id":"s3","name":"web","slug":"web"}]`
	cases := []struct {
		name     string
		cfg      string
		sites    string
		flag     string
		wantSlug string // "" means refused with the mismatch error
		wantID   string
		wantList bool
	}{
		{"another site's slug", collisionJSON, collisionSites, "api", "", "", true},
		{"another site's slug, any case", collisionJSON, collisionSites, "API", "", "", true},
		{"the linked slug", collisionJSON, collisionSites, "main", "main", "s1", true},
		{"the linked id", collisionJSON, collisionSites, "s1", "main", "s1", false},
		{"linked by slug", `{"project_id":"p1","site_slug":"main"}`, collisionSites, "main", "main", "", false},
		{"linked by slug and name", `{"project_id":"p1","site_slug":"main","site_name":"api"}`, collisionSites, "main", "main", "", false},
		{"linked by id alone", linkedByIDJSON, linkedAndOther, "main", "main", "s1", true},
		{"own name equals own slug", `{"project_id":"p1","site_id":"s1","site_name":"main"}`, ownNameSites, "main", "main", "s1", true},
		{"legacy name-only main", `{"project_id":"p1","site_name":"main"}`, ownNameSites, "main", "main", "s1", true},
		// Today's pass stands when the flag names no live site at all.
		{"name naming no live site", `{"project_id":"p1","site_id":"s1","site_slug":"main","site_name":"Old Shop"}`, collisionSites, "old shop", "main", "s1", true},
		// A slug-changing rename leaves an id-linked config's slug stale, and
		// another site may take it back.
		{"stale slug of an id link", `{"project_id":"p1","site_id":"s1","site_slug":"web"}`, renamedSites, "web", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, seen := linkedStub(t, http.StatusOK, map[string]string{sitesRoute: tc.sites})
			client := api.NewClient(&config.Config{APIHost: ts.URL, Token: "t"})

			ctx, err := resolveSiteContextLive(client, configDir(t, tc.cfg), tc.flag, "deploy")

			if tc.wantSlug == "" {
				var mismatch *siteFlagMismatchError
				if !errors.As(err, &mismatch) {
					t.Fatalf("err = %v (ctx %+v); want the mismatch refusal", err, ctx)
				}
			} else {
				if err != nil {
					t.Fatalf("err = %v; want site %s", err, tc.wantSlug)
				}
				if ctx.Site.SiteSlug != tc.wantSlug || ctx.Site.SiteID != tc.wantID {
					t.Errorf("site = %+v; want slug %q id %q", ctx.Site, tc.wantSlug, tc.wantID)
				}
			}
			if got := listedSites(*seen); got != tc.wantList {
				t.Errorf("listed sites = %v; want %v (requests %v)", got, tc.wantList, *seen)
			}
		})
	}
}

// An expired session is said as such, not as "another site".
func TestResolveSiteContextLive_NamePassWithExpiredSession(t *testing.T) {
	ts, _ := linkedStub(t, http.StatusUnauthorized, nil)
	client := api.NewClient(&config.Config{APIHost: ts.URL, Token: "t"})

	_, err := resolveSiteContextLive(client, configDir(t, collisionJSON), "api", "deploy")

	if !errors.Is(err, api.ErrUnauthorized) {
		t.Errorf("err = %v; want api.ErrUnauthorized", err)
	}
}

// A list that cannot be read proves nothing, and the flag path can deploy to
// production: refuse rather than act on the linked site.
func TestResolveSiteContextLive_NamePassWithUnreadableListIsRefused(t *testing.T) {
	ts, _ := linkedStub(t, http.StatusInternalServerError, nil)
	clients := map[string]*api.Client{
		"500":     api.NewClient(&config.Config{APIHost: ts.URL, Token: "t"}),
		"network": api.NewClient(&config.Config{APIHost: "http://127.0.0.1:1", Token: "t"}),
	}
	for name, client := range clients {
		t.Run(name, func(t *testing.T) {
			ctx, err := resolveSiteContextLive(client, configDir(t, collisionJSON), "api", "deploy")

			var mismatch *siteFlagMismatchError
			if !errors.As(err, &mismatch) {
				t.Errorf("err = %v (ctx %+v); want the mismatch refusal", err, ctx)
			}
		})
	}
}

func TestDeploy_SiteFlagNamingAnotherSitesSlugUploadsNothing(t *testing.T) {
	ts, seen, _ := uploadStub(t, collisionSites)
	cliHome(t, ts.URL)
	noPrompt(t)
	fastPolls(t)

	out := runCLI(t, configDir(t, collisionJSON), "deploy", "--site", "api")

	if uploaded(*seen) {
		t.Errorf("requests = %v; --site api must never deploy main", *seen)
	}
	want := `this directory is linked to site "main"; run from the workspace root (or without --site) to deploy another site`
	if !strings.Contains(out, want) {
		t.Errorf("output = %q; want the refusal %q", out, want)
	}
}

func TestDeploy_SiteFlagNamingTheLinkedSlugDeploysIt(t *testing.T) {
	ts, seen, siteID := uploadStub(t, collisionSites)
	cliHome(t, ts.URL)
	noPrompt(t)
	fastPolls(t)

	out := runCLI(t, configDir(t, collisionJSON), "deploy", "--site", "main")

	if !uploaded(*seen) {
		t.Fatalf("requests = %v; want the upload\n%s", *seen, out)
	}
	if *siteID != "s1" {
		t.Errorf("upload site_id = %q; want s1", *siteID)
	}
}

func TestEnvList_SiteFlagNamingAnotherSitesSlugIsRefused(t *testing.T) {
	ts, seen := linkedStub(t, http.StatusOK, map[string]string{sitesRoute: collisionSites})
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, configDir(t, collisionJSON), "env", "list", "--site", "api")

	want := `this directory is linked to site "main"; run from the workspace root (or without --site) to list env vars for another site`
	if !strings.Contains(out, want) {
		t.Errorf("output = %q; want the refusal %q", out, want)
	}
	for _, s := range *seen {
		if strings.HasSuffix(s, "/env") {
			t.Errorf("requests = %v; --site api must not read main's env", *seen)
		}
	}
}

// An app directory of a workspace manifest is guarded the same way.
func TestEnvList_ManifestEntrySiteFlagNamingAnotherSitesSlugIsRefused(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"pnpm-workspace.yaml":   realPnpmWorkspaceYAML,
		"apps/web/package.json": `{}`,
		"apps/api/package.json": `{}`,
		projectConfigName: `{"project_id":"p1","name":"shop","slug":"shop","sites":[
			{"site_id":"s1","site_slug":"main","site_name":"api","root_directory":"apps/web","upload":"app"},
			{"site_id":"s2","site_slug":"api","site_name":"API","root_directory":"apps/api","upload":"app"}]}`,
	})
	ts, seen := linkedStub(t, http.StatusOK, map[string]string{sitesRoute: collisionSites})
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, filepath.Join(root, "apps", "web"), "env", "list", "--site", "api")

	if want := `this directory is linked to site "main"`; !strings.Contains(out, want) {
		t.Errorf("output = %q; want the refusal %q", out, want)
	}
	for _, s := range *seen {
		if strings.HasSuffix(s, "/env") {
			t.Errorf("requests = %v; --site api must not read main's env", *seen)
		}
	}
}
