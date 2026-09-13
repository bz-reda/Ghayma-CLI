package cmd

import (
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
