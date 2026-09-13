package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"paas-cli/internal/api"
	"paas-cli/internal/config"
)

// liveClient is an API client pointed at a stub that serves the given site
// list on GET /api/v1/projects/p1/sites and 404s everything else.
func liveClient(t *testing.T, sites string) *api.Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/p1/sites" {
			io.WriteString(w, sites)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(ts.Close)
	return api.NewClient(&config.Config{APIHost: ts.URL, Token: "t"})
}

func TestProjectHasNoSites(t *testing.T) {
	if none, err := projectHasNoSites(liveClient(t, `[]`), "p1"); err != nil || !none {
		t.Errorf("empty list: none=%v err=%v; want true, nil", none, err)
	}
	if none, err := projectHasNoSites(liveClient(t, `[{"id":"s1","name":"main","slug":"main"}]`), "p1"); err != nil || none {
		t.Errorf("one site: none=%v err=%v; want false, nil", none, err)
	}
	if _, err := projectHasNoSites(liveClient(t, `[]`), "p-missing"); err == nil {
		t.Error("a failed listing must return the error, not a verdict")
	}
}

func TestResolveSiteLess(t *testing.T) {
	if _, err := resolveSiteLess(liveClient(t, `[]`), "p1", ""); err != errNoSite {
		t.Errorf("no sites = %v; want errNoSite", err)
	}
	one := `[{"id":"s1","name":"main","slug":"main"}]`
	if s, err := resolveSiteLess(liveClient(t, one), "p1", ""); err != nil || s.ID != "s1" {
		t.Errorf("lone site: %v, %v", s, err)
	}
	two := `[{"id":"s1","name":"main","slug":"main"},{"id":"s2","name":"Admin","slug":"admin"}]`
	if s, err := resolveSiteLess(liveClient(t, two), "p1", "admin"); err != nil || s.ID != "s2" {
		t.Errorf("--site: %v, %v", s, err)
	}
	if _, err := resolveSiteLess(liveClient(t, two), "p1", ""); err == nil || !strings.Contains(err.Error(), "--site") {
		t.Errorf("several sites without --site must ask for it, got %v", err)
	}
	if _, err := resolveSiteLess(liveClient(t, one), "p-missing", ""); err == nil || !strings.Contains(err.Error(), "failed to list sites") {
		t.Errorf("a failed listing must say so, got %v", err)
	}
}

// legacyJSON is what init wrote before 2026-07-23: a project and nothing about
// a site. It must resolve to the project's live site, never to "no site yet".
const legacyJSON = `{"project_id":"p1","name":"taarefni","slug":"taarefni","framework":"nextjs"}`

// legacyStub serves the live site list plus whatever else a command needs,
// recording every request it answered. A recorded line carries the body's
// site_id when there is one: AddDomain names the site in the body, not in the
// path. A matched POST answers 201, the only status those creates accept.
func legacyStub(t *testing.T, routes map[string]string) (*httptest.Server, *[]string) {
	t.Helper()
	var paths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := r.Method + " " + r.URL.Path
		paths = append(paths, route+bodySiteID(r))

		if r.URL.Path == "/api/v1/projects/p1/sites" {
			io.WriteString(w, `[{"id":"s1","name":"main","slug":"main"}]`)
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
	return ts, &paths
}

// bodySiteID reports the request body's site_id as " site_id=<id>", or "".
func bodySiteID(r *http.Request) string {
	raw, err := io.ReadAll(r.Body)
	if err != nil || len(raw) == 0 {
		return ""
	}
	var body struct {
		SiteID string `json:"site_id"`
	}
	if json.Unmarshal(raw, &body) != nil || body.SiteID == "" {
		return ""
	}
	return " site_id=" + body.SiteID
}

func legacyDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{projectConfigName: legacyJSON})
	return dir
}

func TestEnvList_LegacyConfigResolvesTheLiveSite(t *testing.T) {
	ts, paths := legacyStub(t, map[string]string{
		"GET /api/v1/projects/p1/sites/s1/env": `{"env_vars":{"API_URL":"https://example.test"},"build_time_keys":[]}`,
	})
	cliHome(t, ts.URL)
	forceStdin(t, false)
	noPrompt(t)

	out := runCLI(t, legacyDir(t), "env", "list")
	if strings.Contains(out, noSiteMessage) {
		t.Fatalf("a legacy config on a project with a site must not be called site-less:\n%s", out)
	}
	if !strings.Contains(out, "API_URL=https://example.test") {
		t.Errorf("output %q; want the site's variables", out)
	}
	if lastExitCode != 0 {
		t.Errorf("exit code = %d; want 0", lastExitCode)
	}
	if !contains(*paths, "GET /api/v1/projects/p1/sites/s1/env") {
		t.Errorf("served %v; want the live site's env read", *paths)
	}
}

func TestDomainCreate_LegacyConfigResolvesTheLiveSite(t *testing.T) {
	ts, paths := legacyStub(t, map[string]string{
		"POST /api/v1/domains": `{"id":"d1"}`,
	})
	cliHome(t, ts.URL)
	forceStdin(t, false)
	noPrompt(t)

	out := runCLI(t, legacyDir(t), "domain", "create", "example.com")
	if strings.Contains(out, noSiteMessage) {
		t.Fatalf("legacy config must not be site-less:\n%s", out)
	}
	if !contains(*paths, "POST /api/v1/domains site_id=s1") {
		t.Errorf("served %v; want the domain attached to the live site", *paths)
	}
}

func contains(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}
