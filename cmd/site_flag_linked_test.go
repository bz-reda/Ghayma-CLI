package cmd

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"paas-cli/internal/api"
)

// A directory linked by site_id alone (2026-10-08, staging): `--site main`
// was refused as "another site" although main IS the linked site, because the
// guard compared the flag with the config's own keys and the config carries
// no slug or name. The flag has to be resolved against the live list first.

// linkedByIDJSON links project p1's site s1 by id only.
const linkedByIDJSON = `{"project_id":"p1","name":"shop","slug":"shop","site_id":"s1"}`

// linkedAndOther is p1's live list: s1 is the linked site, s2 another one. Each
// name differs from its slug so the two lookups are told apart.
const linkedAndOther = `[{"id":"s1","name":"storefront","slug":"main"},{"id":"s2","name":"dashboard","slug":"admin"}]`

// linkedRefusal is today's refusal of a --site naming another site.
func linkedRefusal(verb string) string {
	return fmt.Sprintf(`this directory is linked to site "s1"; run from the workspace root (or without --site) to %s another site`, verb)
}

func linkedByIDDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{projectConfigName: linkedByIDJSON})
	return dir
}

// linkedStub serves the site list (or fails it with sitesStatus) plus the
// given routes, recording each request with its body's site_id (bodySiteID).
// A matched POST answers 201.
func linkedStub(t *testing.T, sitesStatus int, routes map[string]string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := r.Method + " " + r.URL.Path
		seen = append(seen, route+bodySiteID(r))
		w.Header().Set("Content-Type", "application/json")

		if route == "GET /api/v1/projects/p1/sites" {
			w.WriteHeader(sitesStatus)
			if sitesStatus == http.StatusOK {
				io.WriteString(w, linkedAndOther)
			}
			return
		}
		if body, ok := routes[route]; ok {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
			}
			io.WriteString(w, body)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":"unexpected `+route+`"}`)
	}))
	t.Cleanup(ts.Close)
	return ts, &seen
}

const rotateS1 = "POST /api/v1/projects/p1/sites/s1/connections/database/d1/rotate"

func rotateRoutes() map[string]string {
	return map[string]string{
		"GET /api/v1/projects/p1/sites/s1/connections": `{"connections":[{"site_id":"s1","site_slug":"main","kind":"database","resource_id":"d1","resource_name":"pg-main","level":"connect"}],"available":[]}`,
		rotateS1: ``,
	}
}

func rotated(seen []string) bool {
	for _, s := range seen {
		if strings.HasSuffix(s, "/rotate") {
			return true
		}
	}
	return false
}

func TestConnectionsRotate_SiteFlagNamingTheLinkedSiteRotatesIt(t *testing.T) {
	for _, flag := range []string{"main", "storefront", "s1", "MAIN"} {
		t.Run(flag, func(t *testing.T) {
			ts, seen := linkedStub(t, http.StatusOK, rotateRoutes())
			cliHome(t, ts.URL)
			noPrompt(t)

			out := runCLI(t, linkedByIDDir(t), "connections", "rotate", "database", "pg-main", "--site", flag, "--yes")

			if !containsPath(*seen, rotateS1) {
				t.Fatalf("requests = %v; want the linked site's rotate\n%s", *seen, out)
			}
			if !strings.Contains(out, "✅ Rotated the credential of database 'pg-main' for 'main'") {
				t.Errorf("output = %q; want the success line", out)
			}
			if lastExitCode != 0 {
				t.Errorf("exit code = %d; want 0", lastExitCode)
			}
		})
	}
}

func TestConnectionsRotate_SiteFlagNamingAnotherSiteIsStillRefused(t *testing.T) {
	for _, flag := range []string{"admin", "dashboard", "s2"} {
		t.Run(flag, func(t *testing.T) {
			ts, seen := linkedStub(t, http.StatusOK, rotateRoutes())
			cliHome(t, ts.URL)
			noPrompt(t)

			out := runCLI(t, linkedByIDDir(t), "connections", "rotate", "database", "pg-main", "--site", flag, "--yes")

			if rotated(*seen) {
				t.Errorf("requests = %v; another site's --site must never rotate the linked site", *seen)
			}
			want := "❌ " + linkedRefusal("rotate a credential for")
			if !strings.Contains(out, want) {
				t.Errorf("output = %q; want today's refusal %q", out, want)
			}
			if lastExitCode != 1 {
				t.Errorf("exit code = %d; want 1", lastExitCode)
			}
		})
	}
}

// An unknown --site keeps today's answer in a linked directory: the refusal,
// and nothing rotated.
func TestConnectionsRotate_UnknownSiteFlagKeepsTodaysRefusal(t *testing.T) {
	ts, seen := linkedStub(t, http.StatusOK, rotateRoutes())
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, linkedByIDDir(t), "connections", "rotate", "database", "pg-main", "--site", "nope", "--yes")

	if rotated(*seen) {
		t.Errorf("requests = %v; an unknown --site must not rotate", *seen)
	}
	if want := linkedRefusal("rotate a credential for"); !strings.Contains(out, want) {
		t.Errorf("output = %q; want today's refusal %q", out, want)
	}
	if lastExitCode != 1 {
		t.Errorf("exit code = %d; want 1", lastExitCode)
	}
}

// A live list that cannot be read proves nothing, so the refusal stands.
func TestConnectionsRotate_UnreadableSiteListKeepsTheRefusal(t *testing.T) {
	ts, seen := linkedStub(t, http.StatusInternalServerError, rotateRoutes())
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, linkedByIDDir(t), "connections", "rotate", "database", "pg-main", "--site", "main", "--yes")

	if rotated(*seen) {
		t.Errorf("requests = %v; an unverified --site must not rotate", *seen)
	}
	if want := linkedRefusal("rotate a credential for"); !strings.Contains(out, want) {
		t.Errorf("output = %q; want today's refusal %q", out, want)
	}
}

// --- the other commands behind the same guard --------------------------------

func TestEnvList_SiteFlagNamingTheLinkedSite(t *testing.T) {
	routes := map[string]string{
		"GET /api/v1/projects/p1/sites/s1/env": `{"env_vars":{"API_URL":"https://example.test"},"build_time_keys":[]}`,
	}
	for _, flag := range []string{"main", "storefront", "s1"} {
		t.Run(flag, func(t *testing.T) {
			ts, seen := linkedStub(t, http.StatusOK, routes)
			cliHome(t, ts.URL)
			noPrompt(t)

			out := runCLI(t, linkedByIDDir(t), "env", "list", "--site", flag)

			if !containsPath(*seen, "GET /api/v1/projects/p1/sites/s1/env") {
				t.Errorf("requests = %v; want the linked site's env\n%s", *seen, out)
			}
			if !strings.Contains(out, "API_URL") {
				t.Errorf("output = %q; want the variables listed", out)
			}
		})
	}

	ts, seen := linkedStub(t, http.StatusOK, routes)
	cliHome(t, ts.URL)
	noPrompt(t)
	out := runCLI(t, linkedByIDDir(t), "env", "list", "--site", "admin")
	if want := linkedRefusal("list env vars for"); !strings.Contains(out, want) {
		t.Errorf("output = %q; want today's refusal %q", out, want)
	}
	for _, s := range *seen {
		if strings.HasSuffix(s, "/env") {
			t.Errorf("requests = %v; another site's --site must not read env", *seen)
		}
	}
}

func TestDomainCreate_SiteFlagNamingTheLinkedSite(t *testing.T) {
	routes := map[string]string{"POST /api/v1/domains": `{}`}

	ts, seen := linkedStub(t, http.StatusOK, routes)
	cliHome(t, ts.URL)
	noPrompt(t)
	out := runCLI(t, linkedByIDDir(t), "domain", "create", "shop.example.com", "--site", "main")
	if !containsPath(*seen, "POST /api/v1/domains site_id=s1") {
		t.Errorf("requests = %v; want the domain attached to the linked site\n%s", *seen, out)
	}

	ts, seen = linkedStub(t, http.StatusOK, routes)
	cliHome(t, ts.URL)
	out = runCLI(t, linkedByIDDir(t), "domain", "create", "shop.example.com", "--site", "admin")
	if want := linkedRefusal("attach the domain to"); !strings.Contains(out, want) {
		t.Errorf("output = %q; want today's refusal %q", out, want)
	}
	for _, s := range *seen {
		if strings.HasPrefix(s, "POST /api/v1/domains") {
			t.Errorf("requests = %v; another site's --site must not attach a domain", *seen)
		}
	}
}

func TestDeploy_SiteFlagNamingTheLinkedSiteDeploysIt(t *testing.T) {
	ts, seen, siteID := uploadStub(t, linkedAndOther)
	cliHome(t, ts.URL)
	noPrompt(t)
	fastPolls(t)

	out := runCLI(t, linkedByIDDir(t), "deploy", "--site", "main")

	if !uploaded(*seen) {
		t.Fatalf("requests = %v; want the upload\n%s", *seen, out)
	}
	if *siteID != "s1" {
		t.Errorf("upload site_id = %q; want s1, the linked site", *siteID)
	}
}

func TestDeploy_SiteFlagNamingAnotherSiteUploadsNothing(t *testing.T) {
	ts, seen, _ := uploadStub(t, linkedAndOther)
	cliHome(t, ts.URL)
	noPrompt(t)
	fastPolls(t)

	out := runCLI(t, linkedByIDDir(t), "deploy", "--site", "admin")

	if uploaded(*seen) {
		t.Errorf("requests = %v; another site's --site must stop before the upload", *seen)
	}
	if want := linkedRefusal("deploy"); !strings.Contains(out, want) {
		t.Errorf("output = %q; want today's refusal %q", out, want)
	}
}

// An app directory of a workspace manifest is pinned the same way: its entry
// names the site by id only, and --site by slug must reach it.
func TestEnvList_ManifestEntrySiteFlagNamingTheLinkedSite(t *testing.T) {
	routes := map[string]string{
		"GET /api/v1/projects/p1/sites/s1/env": `{"env_vars":{"API_URL":"https://example.test"},"build_time_keys":[]}`,
	}
	ts, seen := linkedStub(t, http.StatusOK, routes)
	cliHome(t, ts.URL)
	noPrompt(t)
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"pnpm-workspace.yaml":     realPnpmWorkspaceYAML,
		"apps/web/package.json":   `{}`,
		"apps/admin/package.json": `{}`,
		projectConfigName: `{"project_id":"p1","name":"shop","slug":"shop","sites":[
			{"site_id":"s1","root_directory":"apps/web","upload":"app"},
			{"site_id":"s2","root_directory":"apps/admin","upload":"app"}]}`,
	})

	out := runCLI(t, filepath.Join(root, "apps", "web"), "env", "list", "--site", "main")

	if !containsPath(*seen, "GET /api/v1/projects/p1/sites/s1/env") {
		t.Errorf("requests = %v; want the entry's site env\n%s", *seen, out)
	}
}

func TestDockerPush_SiteFlagNamingTheLinkedSite(t *testing.T) {
	stubDocker(t, func(args []string) (string, error) {
		if args[0] == "push" {
			return pushOutput, nil
		}
		return "", nil
	})
	ts, _, mint := dockerStub(t, linkedAndOther)
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, linkedByIDDir(t), "docker", "push", "my-app:dev", "--tag", "v1", "--site", "main")

	if (*mint)["site"] != "s1" {
		t.Errorf("mint body = %v; want the linked site\n%s", *mint, out)
	}
}

// A legacy config names main by name alone; --site by id is settled live and
// the upload carries that id rather than leaving the site to the server.
func TestDeploy_SiteFlagIDOnLegacyMainConfigCarriesTheID(t *testing.T) {
	ts, seen, siteID := uploadStub(t, linkedAndOther)
	cliHome(t, ts.URL)
	noPrompt(t)
	fastPolls(t)
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{projectConfigName: `{"project_id":"p1","name":"shop","slug":"shop","site_name":"storefront"}`})

	out := runCLI(t, dir, "deploy", "--site", "s1")

	if !uploaded(*seen) {
		t.Fatalf("requests = %v; want the upload\n%s", *seen, out)
	}
	if *siteID != "s1" {
		t.Errorf("upload site_id = %q; want s1, the site --site named", *siteID)
	}
}

func TestLinkedSiteNamed(t *testing.T) {
	sites := []api.Site{
		{ID: "s1", Name: "admin", Slug: "main"},
		{ID: "s2", Name: "dashboard", Slug: "admin"},
	}
	cases := []struct {
		name  string
		entry SiteEntry
		flag  string
		want  string
	}{
		{"slug of the linked site", SiteEntry{SiteID: "s1"}, "main", "s1"},
		{"id of the linked site", SiteEntry{SiteID: "s1"}, "s1", "s1"},
		{"another site's slug", SiteEntry{SiteID: "s1"}, "dashboard", ""},
		// "admin" is s1's name but s2's slug: slugs win, as everywhere.
		{"linked name shadowed by another slug", SiteEntry{SiteID: "s1"}, "admin", ""},
		{"unknown", SiteEntry{SiteID: "s1"}, "nope", ""},
		{"linked site deleted", SiteEntry{SiteID: "gone"}, "main", ""},
		{"linked by slug only", SiteEntry{SiteSlug: "main"}, "s1", "s1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := linkedSiteNamed(sites, tc.entry, tc.flag)
			switch {
			case tc.want == "" && got != nil:
				t.Errorf("linkedSiteNamed = %s; want none", got.ID)
			case tc.want != "" && (got == nil || got.ID != tc.want):
				t.Errorf("linkedSiteNamed = %v; want %s", got, tc.want)
			}
		})
	}
}

// Every command taking --site goes through the live resolver, or a config
// linking its site by id refuses `--site <its own slug>` again. Only the
// resolvers themselves and `site use` (which passes no --site) call the
// offline one.
func TestSiteFlagCommandsUseTheLiveResolver(t *testing.T) {
	offlineAllowed := map[string]bool{"manifest.go": true, "nosite.go": true, "site.go": true}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || offlineAllowed[name] {
			continue
		}
		if strings.Contains(readCmdSource(t, name), "resolveSiteContext(") {
			t.Errorf("%s calls the offline resolveSiteContext — use resolveSiteContextLive", name)
		}
	}
	if !strings.Contains(readCmdSource(t, "site.go"), `resolveSiteContext(cwd, "", `) {
		t.Error("site.go may call the offline resolver only without a --site")
	}
}
