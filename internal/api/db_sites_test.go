package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestListDatabaseSites_ParsesEnvelope pins the GET /:id/sites round-trip:
// correct path + method, Bearer header, and the {"sites":[...]} envelope parsed
// with each row's has_access flag.
func TestListDatabaseSites_ParsesEnvelope(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/databases/d1/sites" || r.Method != http.MethodGet {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("auth header = %q; want Bearer test-token", got)
		}
		io.WriteString(w, `{"sites":[{"site_id":"s1","slug":"main","name":"main","has_access":true},{"site_id":"s2","slug":"admin","name":"Admin","has_access":false}]}`)
	}))
	defer ts.Close()

	sites, err := newTestClient(ts.URL).ListDatabaseSites("d1")
	if err != nil {
		t.Fatalf("ListDatabaseSites: %v", err)
	}
	if len(sites) != 2 {
		t.Fatalf("got %d sites; want 2", len(sites))
	}
	if sites[0].SiteID != "s1" || sites[0].Slug != "main" || !sites[0].HasAccess {
		t.Errorf("sites[0] = %+v; want main granted", sites[0])
	}
	if sites[1].Slug != "admin" || sites[1].HasAccess {
		t.Errorf("sites[1] = %+v; want admin blocked", sites[1])
	}
}

// An unlinked database returns {"sites":[]} → an empty slice, not an error.
func TestListDatabaseSites_UnlinkedEmpty(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"sites":[]}`)
	}))
	defer ts.Close()

	sites, err := newTestClient(ts.URL).ListDatabaseSites("d1")
	if err != nil {
		t.Fatalf("ListDatabaseSites: %v", err)
	}
	if len(sites) != 0 {
		t.Errorf("got %d sites; want 0 for an unlinked db", len(sites))
	}
}

// A non-200 surfaces the server's {"error":...} message via decodeAPIError.
func TestListDatabaseSites_ErrorPassthrough(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"database is not linked to a project"}`)
	}))
	defer ts.Close()

	_, err := newTestClient(ts.URL).ListDatabaseSites("d1")
	if err == nil || !strings.Contains(err.Error(), "not linked to a project") {
		t.Fatalf("err = %v; want the decoded server message", err)
	}
}

// TestSetDatabaseSites_SendsBodyAndParses pins the PUT /:id/sites round-trip:
// the full desired site_ids set is sent and the returned table is parsed.
func TestSetDatabaseSites_SendsBodyAndParses(t *testing.T) {
	var captured struct {
		SiteIDs []string `json:"site_ids"`
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/databases/d1/sites" || r.Method != http.MethodPut {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &captured)
		io.WriteString(w, `{"sites":[{"site_id":"s1","slug":"main","name":"main","has_access":true},{"site_id":"s2","slug":"admin","name":"Admin","has_access":true}]}`)
	}))
	defer ts.Close()

	sites, err := newTestClient(ts.URL).SetDatabaseSites("d1", []string{"s1", "s2"})
	if err != nil {
		t.Fatalf("SetDatabaseSites: %v", err)
	}
	if len(captured.SiteIDs) != 2 || captured.SiteIDs[0] != "s1" || captured.SiteIDs[1] != "s2" {
		t.Errorf("body site_ids = %v; want [s1 s2]", captured.SiteIDs)
	}
	if len(sites) != 2 || !sites[1].HasAccess {
		t.Errorf("parsed sites = %+v; want admin now granted", sites)
	}
}

// An empty (or nil) desired set must serialize as {"site_ids":[]} — an explicit
// deny-all — never {"site_ids":null}, which the backend wouldn't read as
// "revoke all".
func TestSetDatabaseSites_EmptyDenyAll(t *testing.T) {
	var rawBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		rawBody = string(raw)
		io.WriteString(w, `{"sites":[{"site_id":"s1","slug":"main","name":"main","has_access":false}]}`)
	}))
	defer ts.Close()

	if _, err := newTestClient(ts.URL).SetDatabaseSites("d1", nil); err != nil {
		t.Fatalf("SetDatabaseSites: %v", err)
	}
	if !strings.Contains(rawBody, `"site_ids":[]`) {
		t.Errorf("deny-all body = %s; want site_ids:[] (not null)", rawBody)
	}
}

// A non-2xx PUT surfaces the server's {"error":...} message via decodeAPIError.
func TestSetDatabaseSites_ErrorPassthrough(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"site 'x' does not belong to this database's project"}`)
	}))
	defer ts.Close()

	_, err := newTestClient(ts.URL).SetDatabaseSites("d1", []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "does not belong to this database's project") {
		t.Fatalf("err = %v; want the decoded server message", err)
	}
}
