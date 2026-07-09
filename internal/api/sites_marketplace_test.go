package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSetAppTier_SendsBodyAndParses pins the PUT /:id/sites/:siteId/tier
// round-trip: both app_tier_slug and replicas are always sent (app_tier_slug is
// required on the wire; the caller defaults an unset flag to the site's current
// value) and the returned Site — including its new tier/replicas — is parsed.
func TestSetAppTier_SendsBodyAndParses(t *testing.T) {
	var captured map[string]interface{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/projects/proj-1/sites/s1/tier" || r.Method != http.MethodPut {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &captured)
		io.WriteString(w, `{"id":"s1","project_id":"proj-1","name":"Admin","slug":"admin","status":"active","app_tier_slug":"b","replicas":2}`)
	}))
	defer ts.Close()

	site, err := newTestClient(ts.URL).SetAppTier("proj-1", "s1", "b", 2)
	if err != nil {
		t.Fatalf("SetAppTier: %v", err)
	}
	if captured["app_tier_slug"] != "b" {
		t.Errorf("app_tier_slug sent = %v; want b", captured["app_tier_slug"])
	}
	if captured["replicas"] != float64(2) {
		t.Errorf("replicas sent = %v; want 2", captured["replicas"])
	}
	if site.AppTierSlug != "b" || site.Replicas != 2 {
		t.Errorf("parsed site = %+v; want tier b, replicas 2", site)
	}
}

// A non-2xx response routes through classifyAPIError so the marketplace classes
// render. A 409 max-tier rejection must surface the maxtier class.
func TestSetAppTier_ClassifiesMaxTier(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, `{"error":"requested app tier exceeds the plan's maximum: tier \"d\" exceeds plan max app tier \"c\""}`)
	}))
	defer ts.Close()

	_, err := newTestClient(ts.URL).SetAppTier("proj-1", "s1", "d", 1)
	var me *MarketplaceError
	if !errors.As(err, &me) || me.Kind != "maxtier" {
		t.Fatalf("SetAppTier error = %v; want *MarketplaceError kind maxtier", err)
	}
}

// A 503 capacity rejection must surface the capacity class.
func TestSetAppTier_ClassifiesCapacity(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, `{"error":"platform capacity is temporarily exhausted; please try again later"}`)
	}))
	defer ts.Close()

	_, err := newTestClient(ts.URL).SetAppTier("proj-1", "s1", "b", 2)
	var me *MarketplaceError
	if !errors.As(err, &me) || me.Kind != "capacity" {
		t.Fatalf("SetAppTier error = %v; want *MarketplaceError kind capacity", err)
	}
}
