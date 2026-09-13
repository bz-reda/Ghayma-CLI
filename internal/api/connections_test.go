package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListConnections_FiltersBySiteAndDecodesEnvelope(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/projects/p1/connections" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("site_id"); got != "s 1" {
			t.Errorf("site_id = %q; want the escaped site id", got)
		}
		io.WriteString(w, `{"connections":[{"site_id":"s 1","site_slug":"main","kind":"database","resource_id":"d1","resource_name":"pg","level":"connect","created_at":"2026-09-13T00:00:00Z"}]}`)
	}))
	defer ts.Close()

	rows, err := newTestClient(ts.URL).ListConnections("p1", "s 1")
	if err != nil {
		t.Fatalf("ListConnections: %v", err)
	}
	if len(rows) != 1 || rows[0].SiteSlug != "main" || rows[0].Kind != "database" || rows[0].ResourceName != "pg" || rows[0].Level != "connect" {
		t.Errorf("rows = %+v; want the one decoded row", rows)
	}
}

func TestListConnections_NoSiteSendsNoQuery(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Errorf("query = %q; want none", r.URL.RawQuery)
		}
		io.WriteString(w, `{"connections":[]}`)
	}))
	defer ts.Close()

	rows, err := newTestClient(ts.URL).ListConnections("p1", "")
	if err != nil || rows == nil || len(rows) != 0 {
		t.Errorf("rows, err = %v, %v; want an empty non-nil slice", rows, err)
	}
}

func TestGetSiteConnections_DecodesBothHalves(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/projects/p1/sites/s1/connections" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		io.WriteString(w, `{"connections":[{"site_id":"s1","site_slug":"main","kind":"auth_app","resource_id":"a1","resource_name":"my-app","level":"client"}],"available":[{"kind":"bucket","resource_id":"b1","resource_name":"uploads","levels":["read-write"]}]}`)
	}))
	defer ts.Close()

	view, err := newTestClient(ts.URL).GetSiteConnections("p1", "s1")
	if err != nil {
		t.Fatalf("GetSiteConnections: %v", err)
	}
	if len(view.Connections) != 1 || view.Connections[0].ResourceName != "my-app" {
		t.Errorf("connections = %+v", view.Connections)
	}
	if len(view.Available) != 1 || view.Available[0].ResourceName != "uploads" || len(view.Available[0].Levels) != 1 {
		t.Errorf("available = %+v", view.Available)
	}
}

func TestAddSiteConnection_PostsItemAndAccepts201(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/projects/p1/sites/s1/connections" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["kind"] != "auth_app" || body["resource_id"] != "a1" || body["level"] != "admin" {
			t.Errorf("body = %v", body)
		}
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"site_id":"s1","site_slug":"main","kind":"auth_app","resource_id":"a1","resource_name":"my-app","level":"admin"}`)
	}))
	defer ts.Close()

	row, err := newTestClient(ts.URL).AddSiteConnection("p1", "s1", ConnectionItem{Kind: "auth_app", ResourceID: "a1", Level: "admin"})
	if err != nil {
		t.Fatalf("AddSiteConnection: %v", err)
	}
	if row.Level != "admin" || row.SiteSlug != "main" {
		t.Errorf("row = %+v", row)
	}
}

func TestAddSiteConnection_OmitsEmptyLevelSoTheServerDefaults(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), `"level"`) {
			t.Errorf("body %s must not carry an empty level", raw)
		}
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"kind":"database","resource_id":"d1","level":"connect"}`)
	}))
	defer ts.Close()

	if _, err := newTestClient(ts.URL).AddSiteConnection("p1", "s1", ConnectionItem{Kind: "database", ResourceID: "d1"}); err != nil {
		t.Fatalf("AddSiteConnection: %v", err)
	}
}

func TestAddSiteConnection_SurfacesServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"resource does not belong to this project"}`)
	}))
	defer ts.Close()

	_, err := newTestClient(ts.URL).AddSiteConnection("p1", "s1", ConnectionItem{Kind: "database", ResourceID: "x"})
	if err == nil || err.Error() != "resource does not belong to this project" {
		t.Errorf("err = %v; want the server's message", err)
	}
}

func TestRemoveSiteConnection_ReportsRemoved(t *testing.T) {
	for _, removed := range []bool{true, false} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete || r.URL.Path != "/api/v1/projects/p1/sites/s1/connections/database/d1" {
				t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			}
			if removed {
				io.WriteString(w, `{"removed":true}`)
			} else {
				io.WriteString(w, `{"removed":false}`)
			}
		}))
		got, err := newTestClient(ts.URL).RemoveSiteConnection("p1", "s1", "database", "d1")
		ts.Close()
		if err != nil || got != removed {
			t.Errorf("removed=%v: got %v, %v", removed, got, err)
		}
	}
}

func TestGetEffectiveSiteEnv_DecodesEnvAndErrors(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/projects/p1/sites/s1/env/effective" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		io.WriteString(w, `{"env":{"DATABASE_URL":"postgres://u:p@h/db","GHAYMA_API_KEY":"gsk_x"}}`)
	}))
	defer ts.Close()

	env, err := newTestClient(ts.URL).GetEffectiveSiteEnv("p1", "s1")
	if err != nil {
		t.Fatalf("GetEffectiveSiteEnv: %v", err)
	}
	if env["DATABASE_URL"] != "postgres://u:p@h/db" || env["GHAYMA_API_KEY"] != "gsk_x" {
		t.Errorf("env = %v", env)
	}

	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"error":"insufficient role"}`)
	}))
	defer forbidden.Close()
	if _, err := newTestClient(forbidden.URL).GetEffectiveSiteEnv("p1", "s1"); err == nil || err.Error() != "insufficient role" {
		t.Errorf("403 err = %v; want \"insufficient role\"", err)
	}
}

func TestGetEffectiveSiteEnv_EmptyEnvIsNonNil(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"env":{}}`)
	}))
	defer ts.Close()
	env, err := newTestClient(ts.URL).GetEffectiveSiteEnv("p1", "s1")
	if err != nil || env == nil {
		t.Errorf("env, err = %v, %v; want an empty non-nil map", env, err)
	}
}
