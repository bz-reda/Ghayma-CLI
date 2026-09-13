# Connections commands Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the terminal half of Connections: `ghayma connections [--site]`, `ghayma connect <database|bucket|auth> <name> [--site] [--level]`, `ghayma disconnect … [--site] [--yes]` and `ghayma env pull [--site] [--out] [--force]`.

**Architecture:** One new client file (`internal/api/connections.go`) wraps the five backend routes. The commands reuse the CLI's existing site ladder (`resolveSiteContext`: the linked site of this directory, the workspace entry, `--site`, or a picker at a workspace root) and then map the config's site onto the project's LIVE site list, because a config may name a site the backend has not materialised yet. Resources are resolved against the site's own connections view (`connections` + `available`), so a name can never match another project's resource. `env pull` writes the site's **effective** environment (the new backend route `GET …/env/effective`, project admin role) to a git-ignored dotenv file and never prints a value. All pure logic (kind parsing, live-site pick, resource match, table rendering, dotenv quoting, the git-safety decision) is unit-tested; the network layer is tested with `httptest`.

**Tech Stack:** Go 1.24, Cobra, promptui (not needed here), `net/http/httptest`, `os/exec` for the git checks. CI runs `gofmt -l`, `go vet`, `go test ./...` on Linux, Windows and macOS — every test must be path- and TTY-independent.

## Global Constraints

- Never put AI attribution in commits, PR text or files.
- Never print a variable VALUE from `env pull`; only key names and counts. Never print the effective env anywhere else either.
- The four new commands print `❌ <message>` and exit 1 on failure (through `exitFn`, so tests can stub it); a site-less project prints `noSiteMessage` and exits 1 (`reportSiteError`). Success paths return normally.
- Names a user types are matched case-insensitively (`strings.EqualFold`), as `matchSiteEntry` already does.
- The backend is the authority on levels; the client only mirrors the level sets for early, friendlier errors (`kindLevels`).
- Keep `ghayma db sites` working untouched (it is an adapter over the same records); only its help text gains a pointer.
- Comments minimal, explain why; match the style of `cmd/db_sites.go`.
- Commit after every task with the message given; no other trailers. Do not push.

## Backend contract this plan targets

Bearer = the CLI's account token. `:id` = project id or slug.

| Route | Response |
|---|---|
| `GET /api/v1/projects/:id/connections?site_id=` | `{"connections":[{site_id, site_slug, kind, resource_id, resource_name, level, created_at}]}` |
| `GET /api/v1/projects/:id/sites/:siteId/connections` | `{"connections":[…], "available":[{kind, resource_id, resource_name, levels:[…]}]}` |
| `POST /api/v1/projects/:id/sites/:siteId/connections` body `{kind, resource_id, level?}` | `201` one connection row; a POST on an already-connected resource changes its level |
| `DELETE /api/v1/projects/:id/sites/:siteId/connections/:kind/:resourceId` | `200 {"removed": true\|false}` |
| `GET /api/v1/projects/:id/sites/:siteId/env/effective` | `200 {"env":{…}}`; `403 {"error":"insufficient role"}` below admin; `503` while the worker starts |

Kinds: `database` (levels `connect`), `bucket` (`read-write`), `auth_app` (`client`, `admin`). `resource_name` is a database's or bucket's name and an auth app's `app_id`. Errors: `400 {"error"}` (foreign resource, bad level), `403 {"error":"insufficient role"}`, `404` site not in project.

---

### Task 1: API client for the connections routes

**Files:**
- Create: `internal/api/connections.go`
- Test: `internal/api/connections_test.go`

**Interfaces:**
- Consumes: `(*Client).authRequest(method, path string, body io.Reader) (*http.Response, error)`, `decodeAPIError(resp) error`, `newTestClient(url string) *Client` (already in `client_test.go`).
- Produces:
  - `type Connection struct { SiteID, SiteSlug, Kind, ResourceID, ResourceName, Level, CreatedAt string }`
  - `type AvailableConnection struct { Kind, ResourceID, ResourceName string; Levels []string }`
  - `type SiteConnections struct { Connections []Connection; Available []AvailableConnection }`
  - `type ConnectionItem struct { Kind, ResourceID, Level string }`
  - `func (c *Client) ListConnections(projectID, siteID string) ([]Connection, error)`
  - `func (c *Client) GetSiteConnections(projectID, siteID string) (*SiteConnections, error)`
  - `func (c *Client) AddSiteConnection(projectID, siteID string, item ConnectionItem) (*Connection, error)`
  - `func (c *Client) RemoveSiteConnection(projectID, siteID, kind, resourceID string) (bool, error)`
  - `func (c *Client) GetEffectiveSiteEnv(projectID, siteID string) (map[string]string, error)`

- [ ] **Step 1: Write the failing tests**

Create `internal/api/connections_test.go`:

```go
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
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/api/ -run 'Connections|EffectiveSiteEnv'`
Expected: compile errors — `undefined: ConnectionItem`, `c.ListConnections undefined`, …

- [ ] **Step 3: Write `internal/api/connections.go`**

```go
package api

import (
	"bytes"
	"encoding/json"
	"net/url"
)

// Connections (Ghayma-backend, 2026-09-08). A connection says one SITE may use
// one SERVICE at one LEVEL; it is the single record behind the variables a
// site's pods receive, the per-database network policy and the site's managed
// platform key. The routes live on the project scope (`:id` is a project id or
// slug); reads need the project `read` role, mutations `write`.

// Connection is one row of every connections response. ResourceName is a
// database's or bucket's name and an auth app's app_id.
type Connection struct {
	SiteID       string `json:"site_id"`
	SiteSlug     string `json:"site_slug"`
	Kind         string `json:"kind"`
	ResourceID   string `json:"resource_id"`
	ResourceName string `json:"resource_name"`
	Level        string `json:"level"`
	CreatedAt    string `json:"created_at"`
}

// AvailableConnection is a project resource the site is NOT connected to, with
// the levels it accepts — the "connect something" half of a site's view.
type AvailableConnection struct {
	Kind         string   `json:"kind"`
	ResourceID   string   `json:"resource_id"`
	ResourceName string   `json:"resource_name"`
	Levels       []string `json:"levels"`
}

// SiteConnections is one site's view: what it holds and what it could add.
type SiteConnections struct {
	Connections []Connection          `json:"connections"`
	Available   []AvailableConnection `json:"available"`
}

// ConnectionItem is the request shape for connecting one resource. An empty
// Level is omitted so the server applies the kind's weakest level.
type ConnectionItem struct {
	Kind       string `json:"kind"`
	ResourceID string `json:"resource_id"`
	Level      string `json:"level,omitempty"`
}

// ListConnections returns the project's connections, narrowed to one site when
// siteID is set (GET /api/v1/projects/:id/connections[?site_id=]). Never nil on
// success.
func (c *Client) ListConnections(projectID, siteID string) ([]Connection, error) {
	path := "/api/v1/projects/" + projectID + "/connections"
	if siteID != "" {
		path += "?site_id=" + url.QueryEscape(siteID)
	}
	resp, err := c.authRequest("GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, decodeAPIError(resp)
	}
	var out struct {
		Connections []Connection `json:"connections"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Connections == nil {
		out.Connections = []Connection{}
	}
	return out.Connections, nil
}

// GetSiteConnections returns one site's connections plus the project resources
// it could still be connected to (GET …/sites/:siteId/connections).
func (c *Client) GetSiteConnections(projectID, siteID string) (*SiteConnections, error) {
	resp, err := c.authRequest("GET", "/api/v1/projects/"+projectID+"/sites/"+siteID+"/connections", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, decodeAPIError(resp)
	}
	var out SiteConnections
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AddSiteConnection connects one resource, or changes its level when it is
// already connected (POST …/sites/:siteId/connections → 201 with the row).
func (c *Client) AddSiteConnection(projectID, siteID string, item ConnectionItem) (*Connection, error) {
	body, _ := json.Marshal(item)
	resp, err := c.authRequest("POST", "/api/v1/projects/"+projectID+"/sites/"+siteID+"/connections", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, decodeAPIError(resp)
	}
	var row Connection
	if err := json.NewDecoder(resp.Body).Decode(&row); err != nil {
		return nil, err
	}
	return &row, nil
}

// RemoveSiteConnection disconnects one resource (DELETE …/connections/:kind/
// :resourceId). The server answers 200 either way; removed says whether a row
// was actually there, so a retry after a lost response is not an error.
func (c *Client) RemoveSiteConnection(projectID, siteID, kind, resourceID string) (bool, error) {
	resp, err := c.authRequest("DELETE", "/api/v1/projects/"+projectID+"/sites/"+siteID+"/connections/"+kind+"/"+resourceID, nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false, decodeAPIError(resp)
	}
	var out struct {
		Removed bool `json:"removed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, err
	}
	return out.Removed, nil
}

// GetEffectiveSiteEnv returns the exact environment a pod of the site receives:
// the stored variables plus every connection-derived value (GET …/env/effective
// → {"env":{…}}). Project admin role; the server audits every read. Never nil
// on success.
func (c *Client) GetEffectiveSiteEnv(projectID, siteID string) (map[string]string, error) {
	resp, err := c.authRequest("GET", "/api/v1/projects/"+projectID+"/sites/"+siteID+"/env/effective", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, decodeAPIError(resp)
	}
	var out struct {
		Env map[string]string `json:"env"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Env == nil {
		out.Env = map[string]string{}
	}
	return out.Env, nil
}
```

- [ ] **Step 4: Run the tests**

Run: `gofmt -l internal/api/ && go vet ./internal/api/ && go test ./internal/api/`
Expected: gofmt lists nothing; vet clean; `ok  	paas-cli/internal/api`

- [ ] **Step 5: Commit**

```bash
git add internal/api/connections.go internal/api/connections_test.go
git commit -m "feat(api): connections and effective-env client calls"
```

---

### Task 2: Pure helpers — kinds, live-site pick, resource match, levels, table

**Files:**
- Create: `cmd/connections_resolve.go`
- Test: `cmd/connections_resolve_test.go`

**Interfaces:**
- Consumes: `api.Site{ID, Name, Slug}`, `SiteEntry{SiteID, SiteName, SiteSlug}` (cmd/manifest.go), `errNoSite` (cmd/nosite.go), and — in Task 3 — the existing `appDirOf(sourceDir, rootDirectory string) string` from cmd/deploy.go:437 (do NOT redefine it), `api.SiteConnections`, `api.Connection`, `api.AvailableConnection`.
- Produces (used by Tasks 3–5):
  - `func parseConnectionKind(arg string) (string, error)` → `"database" | "bucket" | "auth_app"`
  - `func kindLabel(kind string) string` → `"database"`, `"bucket"`, `"auth app"`
  - `func createHint(kind string) string`
  - `var kindLevels = map[string][]string{…}` and `func defaultLevel(kind string) string`
  - `const adminNote = "admin lets this app manage all users of this auth app."`
  - `func pickLiveSite(sites []api.Site, entry SiteEntry) (*api.Site, error)`
  - `func siteSlugs(sites []api.Site) []string`
  - `func findConnectable(view *api.SiteConnections, kind, name string) (held *api.Connection, avail *api.AvailableConnection, err error)`
  - `func resolveLevel(kind, flag string, accepted []string) (string, error)`
  - `func renderConnectionsTable(rows []api.Connection) string`

- [ ] **Step 1: Write the failing tests**

Create `cmd/connections_resolve_test.go`:

```go
package cmd

import (
	"strings"
	"testing"

	"paas-cli/internal/api"
)

func TestParseConnectionKind(t *testing.T) {
	cases := map[string]string{
		"database": "database", "db": "database", "DB": "database",
		"bucket": "bucket", "storage": "bucket",
		"auth": "auth_app", "auth_app": "auth_app", "auth-app": "auth_app",
	}
	for in, want := range cases {
		got, err := parseConnectionKind(in)
		if err != nil || got != want {
			t.Errorf("parseConnectionKind(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := parseConnectionKind("queue"); err == nil || !strings.Contains(err.Error(), "database, bucket or auth") {
		t.Errorf("unknown kind must name the accepted words; got %v", err)
	}
}

func TestKindLabelsAndDefaults(t *testing.T) {
	if kindLabel("auth_app") != "auth app" || kindLabel("database") != "database" || kindLabel("bucket") != "bucket" {
		t.Error("kindLabel must render the human words")
	}
	if defaultLevel("database") != "connect" || defaultLevel("bucket") != "read-write" || defaultLevel("auth_app") != "client" {
		t.Error("defaultLevel must be the weakest level of each kind")
	}
	for _, kind := range []string{"database", "bucket", "auth_app"} {
		if !strings.HasPrefix(createHint(kind), "ghayma ") {
			t.Errorf("createHint(%s) = %q; want a ghayma command", kind, createHint(kind))
		}
	}
}

func liveSites() []api.Site {
	return []api.Site{
		{ID: "id-main", Name: "main", Slug: "main"},
		{ID: "id-admin", Name: "Admin Console", Slug: "admin"},
	}
}

func TestPickLiveSite_ByIDThenSlugThenName(t *testing.T) {
	if s, err := pickLiveSite(liveSites(), SiteEntry{SiteID: "id-admin"}); err != nil || s.Slug != "admin" {
		t.Errorf("by id: %v, %v", s, err)
	}
	if s, err := pickLiveSite(liveSites(), SiteEntry{SiteSlug: "ADMIN"}); err != nil || s.ID != "id-admin" {
		t.Errorf("by slug (case-insensitive): %v, %v", s, err)
	}
	// init writes site_name "main" with no id for the lazily created main site.
	if s, err := pickLiveSite(liveSites(), SiteEntry{SiteName: "main"}); err != nil || s.ID != "id-main" {
		t.Errorf("by name: %v, %v", s, err)
	}
	if s, err := pickLiveSite(liveSites(), SiteEntry{SiteName: "Admin Console"}); err != nil || s.ID != "id-admin" {
		t.Errorf("by display name: %v, %v", s, err)
	}
}

func TestPickLiveSite_StaleIDErrors(t *testing.T) {
	_, err := pickLiveSite(liveSites(), SiteEntry{SiteID: "id-gone", SiteSlug: "main"})
	if err == nil || !strings.Contains(err.Error(), "ghayma link") {
		t.Errorf("a stale site_id must ask to relink, got %v", err)
	}
}

func TestPickLiveSite_NoEntryFallsBackToTheOnlySite(t *testing.T) {
	one := liveSites()[:1]
	if s, err := pickLiveSite(one, SiteEntry{}); err != nil || s.ID != "id-main" {
		t.Errorf("lone site: %v, %v", s, err)
	}
	_, err := pickLiveSite(liveSites(), SiteEntry{})
	if err == nil || !strings.Contains(err.Error(), "--site") || !strings.Contains(err.Error(), "main, admin") {
		t.Errorf("several sites must ask for --site and list them, got %v", err)
	}
	if _, err := pickLiveSite(nil, SiteEntry{}); err != errNoSite {
		t.Errorf("no sites must be errNoSite, got %v", err)
	}
}

func siteView() *api.SiteConnections {
	return &api.SiteConnections{
		Connections: []api.Connection{
			{Kind: "database", ResourceID: "d1", ResourceName: "pg-main", Level: "connect"},
			{Kind: "auth_app", ResourceID: "a1", ResourceName: "shop", Level: "client"},
		},
		Available: []api.AvailableConnection{
			{Kind: "database", ResourceID: "d2", ResourceName: "pg-analytics", Levels: []string{"connect"}},
			{Kind: "bucket", ResourceID: "b1", ResourceName: "uploads", Levels: []string{"read-write"}},
		},
	}
}

func TestFindConnectable(t *testing.T) {
	held, avail, err := findConnectable(siteView(), "database", "PG-MAIN")
	if err != nil || held == nil || avail != nil || held.ResourceID != "d1" {
		t.Errorf("held: %v %v %v", held, avail, err)
	}
	held, avail, err = findConnectable(siteView(), "database", "pg-analytics")
	if err != nil || held != nil || avail == nil || avail.ResourceID != "d2" {
		t.Errorf("available: %v %v %v", held, avail, err)
	}
	// The same name under another kind is not a match.
	if _, _, err := findConnectable(siteView(), "bucket", "pg-main"); err == nil {
		t.Error("a database name must not match as a bucket")
	}
	_, _, err = findConnectable(siteView(), "database", "nope")
	if err == nil || !strings.Contains(err.Error(), "pg-analytics, pg-main") {
		t.Errorf("a miss must list the project's databases sorted, got %v", err)
	}
	_, _, err = findConnectable(siteView(), "auth_app", "nope")
	if err == nil || !strings.Contains(err.Error(), "shop") {
		t.Errorf("a miss must list held names too, got %v", err)
	}
	empty := &api.SiteConnections{}
	_, _, err = findConnectable(empty, "bucket", "x")
	if err == nil || !strings.Contains(err.Error(), "ghayma storage create") {
		t.Errorf("no buckets at all must point at the create command, got %v", err)
	}
}

func TestResolveLevel(t *testing.T) {
	if lvl, err := resolveLevel("auth_app", "", []string{"client", "admin"}); err != nil || lvl != "" {
		t.Errorf("no flag must leave the level to the server, got %q %v", lvl, err)
	}
	if lvl, err := resolveLevel("auth_app", "ADMIN", []string{"client", "admin"}); err != nil || lvl != "admin" {
		t.Errorf("flag is normalised, got %q %v", lvl, err)
	}
	_, err := resolveLevel("database", "admin", []string{"connect"})
	if err == nil || !strings.Contains(err.Error(), "connect") {
		t.Errorf("a level outside the accepted set must name the set, got %v", err)
	}
	// An empty accepted list (an already-connected resource) falls back to the
	// kind's known levels.
	if lvl, err := resolveLevel("bucket", "read-write", nil); err != nil || lvl != "read-write" {
		t.Errorf("fallback to kindLevels, got %q %v", lvl, err)
	}
}

func TestRenderConnectionsTable_OrderAndColumns(t *testing.T) {
	rows := []api.Connection{
		{SiteSlug: "main", Kind: "auth_app", ResourceName: "shop", Level: "client"},
		{SiteSlug: "admin", Kind: "bucket", ResourceName: "uploads", Level: "read-write"},
		{SiteSlug: "main", Kind: "database", ResourceName: "pg-main", Level: "connect"},
		{SiteSlug: "main", Kind: "bucket", ResourceName: "uploads", Level: "read-write"},
	}
	out := renderConnectionsTable(rows)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("want header + 4 rows, got %d lines:\n%s", len(lines), out)
	}
	if !strings.HasPrefix(strings.TrimSpace(lines[0]), "SITE") || !strings.Contains(lines[0], "LEVEL") {
		t.Errorf("header = %q", lines[0])
	}
	// Sites alphabetical, then kinds in the documented order (database,
	// bucket, auth app), then names.
	want := []string{"admin", "main", "main", "main"}
	kinds := []string{"bucket", "database", "bucket", "auth app"}
	for i, line := range lines[1:] {
		fields := strings.Fields(line)
		if fields[0] != want[i] {
			t.Errorf("row %d site = %q; want %q", i, fields[0], want[i])
		}
		if !strings.Contains(line, kinds[i]) {
			t.Errorf("row %d = %q; want kind %q", i, line, kinds[i])
		}
	}
	// Columns line up: every row's LEVEL starts at the same offset as the header's.
	off := strings.Index(lines[0], "LEVEL")
	for _, line := range lines[1:] {
		if len(line) <= off || line[off] == ' ' {
			t.Errorf("row %q does not align LEVEL at column %d", line, off)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/ -run 'ConnectionKind|KindLabels|PickLiveSite|FindConnectable|ResolveLevel|RenderConnectionsTable'`
Expected: compile errors (`undefined: parseConnectionKind`, …)

- [ ] **Step 3: Write `cmd/connections_resolve.go`**

```go
package cmd

import (
	"fmt"
	"sort"
	"strings"

	"paas-cli/internal/api"
)

// Connections (2026-09-13). A connection says one SITE may use one SERVICE at
// one LEVEL — the single record behind the variables an app receives, the
// per-database network policy and the site's managed platform key. This file
// holds the pure halves of the commands: naming kinds, mapping the linked
// site onto a live one, matching a resource by name, and rendering.

// connectionKinds maps what a user types to the API's kind names.
var connectionKinds = map[string]string{
	"database": "database", "db": "database",
	"bucket": "bucket", "storage": "bucket",
	"auth": "auth_app", "auth_app": "auth_app", "auth-app": "auth_app",
}

// kindOrder is the documented listing order, database first.
var kindOrder = []string{"database", "bucket", "auth_app"}

// kindLevels mirrors the server's per-kind level sets, weakest first. The
// server stays the authority; this only makes a wrong --level fail before
// the request.
var kindLevels = map[string][]string{
	"database": {"connect"},
	"bucket":   {"read-write"},
	"auth_app": {"client", "admin"},
}

// adminNote is printed whenever a connection is made at the admin level —
// the same sentence the console shows next to that choice.
const adminNote = "admin lets this app manage all users of this auth app."

func parseConnectionKind(arg string) (string, error) {
	if kind, ok := connectionKinds[strings.ToLower(arg)]; ok {
		return kind, nil
	}
	return "", fmt.Errorf("unknown kind %q — use database, bucket or auth", arg)
}

func kindLabel(kind string) string {
	if kind == "auth_app" {
		return "auth app"
	}
	return kind
}

func defaultLevel(kind string) string {
	if levels := kindLevels[kind]; len(levels) > 0 {
		return levels[0]
	}
	return ""
}

// createHint names the command that creates a resource of the kind, for the
// "nothing to connect yet" message.
func createHint(kind string) string {
	switch kind {
	case "database":
		return "ghayma db create <name>"
	case "bucket":
		return "ghayma storage create <name>"
	default:
		return "ghayma auth create <name> --app-id <id>"
	}
}

func kindRank(kind string) int {
	for i, k := range kindOrder {
		if k == kind {
			return i
		}
	}
	return len(kindOrder)
}

// pickLiveSite maps the linked site (what the config names) onto the project's
// live site list: by id, then slug, then name, then "the only site". The
// config may carry a name with no id — init writes `site_name: main` before
// the backend materialises main — so the live list decides.
func pickLiveSite(sites []api.Site, entry SiteEntry) (*api.Site, error) {
	if entry.SiteID != "" {
		for i := range sites {
			if sites[i].ID == entry.SiteID {
				return &sites[i], nil
			}
		}
		return nil, fmt.Errorf("the linked site is no longer in this project — run 'ghayma link' to relink")
	}
	for _, want := range []string{entry.SiteSlug, entry.SiteName} {
		if want == "" {
			continue
		}
		for i := range sites {
			if strings.EqualFold(sites[i].Slug, want) || strings.EqualFold(sites[i].Name, want) {
				return &sites[i], nil
			}
		}
	}
	switch len(sites) {
	case 0:
		return nil, errNoSite
	case 1:
		return &sites[0], nil
	}
	return nil, fmt.Errorf("several sites in this project — pass --site <slug> (available: %s)", strings.Join(siteSlugs(sites), ", "))
}

func siteSlugs(sites []api.Site) []string {
	out := make([]string, len(sites))
	for i, s := range sites {
		out[i] = s.Slug
	}
	return out
}

// findConnectable locates the resource a user named within one kind, among the
// site's current connections and the project's still-available resources.
// Exactly one of held/avail is non-nil on success. Names are what the API
// lists: a database's or bucket's name, an auth app's app_id.
func findConnectable(view *api.SiteConnections, kind, name string) (*api.Connection, *api.AvailableConnection, error) {
	for i := range view.Connections {
		c := &view.Connections[i]
		if c.Kind == kind && strings.EqualFold(c.ResourceName, name) {
			return c, nil, nil
		}
	}
	for i := range view.Available {
		a := &view.Available[i]
		if a.Kind == kind && strings.EqualFold(a.ResourceName, name) {
			return nil, a, nil
		}
	}
	names := connectableNames(view, kind)
	if len(names) == 0 {
		return nil, nil, fmt.Errorf("this project has no %s yet — create one with: %s", kindLabel(kind), createHint(kind))
	}
	return nil, nil, fmt.Errorf("no %s named %q in this project (available: %s)", kindLabel(kind), name, strings.Join(names, ", "))
}

// connectableNames lists every resource of the kind the project has, held or
// not, sorted — the hint for a name that matched nothing.
func connectableNames(view *api.SiteConnections, kind string) []string {
	var names []string
	for _, c := range view.Connections {
		if c.Kind == kind {
			names = append(names, c.ResourceName)
		}
	}
	for _, a := range view.Available {
		if a.Kind == kind {
			names = append(names, a.ResourceName)
		}
	}
	sort.Strings(names)
	return names
}

// resolveLevel picks the level to send: "" when no flag was given (the server
// applies the kind's weakest), else the flag, normalised and checked against
// the levels the server listed for the resource — or, for an already-connected
// resource (the server lists levels only for unconnected ones), the kind's
// known set.
func resolveLevel(kind, flag string, accepted []string) (string, error) {
	if flag == "" {
		return "", nil
	}
	if len(accepted) == 0 {
		accepted = kindLevels[kind]
	}
	want := strings.ToLower(flag)
	for _, l := range accepted {
		if l == want {
			return l, nil
		}
	}
	return "", fmt.Errorf("level %q is not valid for a %s (accepted: %s)", flag, kindLabel(kind), strings.Join(accepted, ", "))
}

// renderConnectionsTable renders SITE / KIND / SERVICE / LEVEL, sites
// alphabetical, kinds in the documented order, names alphabetical. Widths fit
// the content so a long slug does not break the columns.
func renderConnectionsTable(rows []api.Connection) string {
	sorted := append([]api.Connection(nil), rows...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.SiteSlug != b.SiteSlug {
			return a.SiteSlug < b.SiteSlug
		}
		if kindRank(a.Kind) != kindRank(b.Kind) {
			return kindRank(a.Kind) < kindRank(b.Kind)
		}
		return a.ResourceName < b.ResourceName
	})

	header := []string{"SITE", "KIND", "SERVICE", "LEVEL"}
	widths := []int{len(header[0]), len(header[1]), len(header[2])}
	cells := make([][]string, 0, len(sorted))
	for _, r := range sorted {
		row := []string{r.SiteSlug, kindLabel(r.Kind), r.ResourceName, r.Level}
		for i := 0; i < 3; i++ {
			if len(row[i]) > widths[i] {
				widths[i] = len(row[i])
			}
		}
		cells = append(cells, row)
	}

	var b strings.Builder
	line := func(row []string) {
		fmt.Fprintf(&b, "   %-*s  %-*s  %-*s  %s\n", widths[0], row[0], widths[1], row[1], widths[2], row[2], row[3])
	}
	line(header)
	for _, row := range cells {
		line(row)
	}
	return b.String()
}
```

- [ ] **Step 4: Run the tests**

Run: `gofmt -l cmd/ && go vet ./cmd/ && go test ./cmd/ -run 'ConnectionKind|KindLabels|PickLiveSite|FindConnectable|ResolveLevel|RenderConnectionsTable'`
Expected: gofmt lists nothing; vet clean; `ok  	paas-cli/cmd`

- [ ] **Step 5: Commit**

```bash
git add cmd/connections_resolve.go cmd/connections_resolve_test.go
git commit -m "feat(connections): pure resolution and rendering helpers"
```

---

### Task 3: `ghayma connections` and the shared target resolver

**Files:**
- Create: `cmd/connections.go`
- Test: `cmd/connections_test.go`

**Interfaces:**
- Consumes: Task 1 client; Task 2 helpers; `resolveSiteContext(cwd, siteFlag, verb string) (*SiteContext, error)`, `errAttachCancelled`, `errNoProjectConfig`, `localConfig()`, `localConfigIsSiteLess()`, `reportSiteError(err)`, `noSiteMessage`, `exitFn`, `config.Load()`, `api.NewClient`.
- Produces (used by Tasks 4–5):
  - `type connectionTarget struct { ProjectID, ProjectName string; Site api.Site; AppDir string }`
  - `func resolveConnectionTarget(client *api.Client, siteFlag, verb string) (*connectionTarget, error)`
  - `func failf(format string, args ...interface{})` — prints `❌ …` and `exitFn(1)`
  - `var connectionsCmd`

- [ ] **Step 1: Write the failing tests**

Create `cmd/connections_test.go`:

```go
package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"paas-cli/internal/api"
)

// captureStdout runs fn with os.Stdout redirected and returns what it printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

// stubExit swaps exitFn for one that records the code without exiting.
func stubExit(t *testing.T) *int {
	t.Helper()
	code := -1
	old := exitFn
	exitFn = func(c int) { code = c }
	t.Cleanup(func() { exitFn = old })
	return &code
}

func TestFailfPrintsAndExits1(t *testing.T) {
	code := stubExit(t)
	out := captureStdout(t, func() { failf("boom %d", 7) })
	if out != "❌ boom 7\n" || *code != 1 {
		t.Errorf("failf printed %q and exited %d; want the ❌ line and exit 1", out, *code)
	}
}

func TestPrintConnections_EmptyAndJSON(t *testing.T) {
	out := captureStdout(t, func() { printConnections("shop", nil, false) })
	if !strings.Contains(out, "No connections yet") || !strings.Contains(out, "ghayma connect") {
		t.Errorf("empty listing must say so and hint at connect, got %q", out)
	}

	rows := []api.Connection{{SiteID: "s1", SiteSlug: "main", Kind: "database", ResourceID: "d1", ResourceName: "pg", Level: "connect", CreatedAt: "2026-09-13T00:00:00Z"}}
	out = captureStdout(t, func() { printConnections("shop", rows, true) })
	var decoded []map[string]string
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("--json must print a JSON array, got %q: %v", out, err)
	}
	if decoded[0]["site_slug"] != "main" || decoded[0]["resource_name"] != "pg" || decoded[0]["created_at"] == "" {
		t.Errorf("--json must mirror the API rows, got %v", decoded)
	}

	out = captureStdout(t, func() { printConnections("shop", nil, true) })
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("--json with no rows must print [], got %q", out)
	}

	out = captureStdout(t, func() { printConnections("shop", rows, false) })
	if !strings.Contains(out, "shop") || !strings.Contains(out, "SITE") || !strings.Contains(out, "pg") {
		t.Errorf("table listing must name the project and render the table, got %q", out)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/ -run 'Failf|PrintConnections'`
Expected: compile errors (`undefined: failf`, `undefined: printConnections`)

- [ ] **Step 3: Write `cmd/connections.go`**

```go
package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

var (
	connectionsSite string
	connectionsJSON bool
)

var connectionsCmd = &cobra.Command{
	Use:   "connections",
	Short: "List which apps may use which services",
	Long: `List the project's connections: which site (app) may use which database,
bucket or auth app, and at what level.

A connection is what hands an app its variables (DATABASE_URL, STORAGE_*,
the ESPACETECH_AUTH_* set, GHAYMA_API_KEY), opens the network path to a
database, and shapes the app's managed platform key. Change one with
'ghayma connect' and 'ghayma disconnect'.

Without --site every site of the linked project is listed. --json prints the
rows exactly as the API returns them.

Examples:
  ghayma connections                 # every site of this project
  ghayma connections --site admin    # one site
  ghayma connections --json`,
	Args: cobra.NoArgs,
	Run:  runConnections,
}

// connectionTarget is the resolved (project, site) a connections command acts
// on, plus the directory that holds the site's code.
type connectionTarget struct {
	ProjectID   string
	ProjectName string
	Site        api.Site
	AppDir      string
}

// failf prints the ❌ line every command uses and exits 1, so a script can
// rely on the exit code. Tests stub exitFn.
func failf(format string, args ...interface{}) {
	fmt.Printf("❌ "+format+"\n", args...)
	exitFn(1)
}

// resolveConnectionTarget resolves the site a command acts on through the
// ladder every site-scoped command uses — the linked site of this directory,
// the workspace entry, --site, or a picker at a workspace root — then maps it
// onto a LIVE site of the project (pickLiveSite). The site-less and cancelled
// cases become the errors reportSiteError already knows how to print.
func resolveConnectionTarget(client *api.Client, siteFlag, verb string) (*connectionTarget, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	ctx, err := resolveSiteContext(cwd, siteFlag, verb)
	switch {
	case errors.Is(err, errAttachCancelled):
		return nil, errors.New("Cancelled")
	case errors.Is(err, errNoProjectConfig):
		return nil, errors.New("no project config found — run 'ghayma init' first")
	case err != nil:
		return nil, err
	}
	if ctx.NoSite {
		return nil, errNoSite
	}
	sites, err := client.ListSites(ctx.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to list sites: %v", err)
	}
	site, err := pickLiveSite(sites, ctx.Site)
	if err != nil {
		return nil, err
	}
	return &connectionTarget{ProjectID: ctx.ProjectID, ProjectName: ctx.ProjectName, Site: *site, AppDir: appDirOf(ctx.SourceDir, ctx.RootDirectory)}, nil
}

func runConnections(cmd *cobra.Command, args []string) {
	cfg := config.Load()
	if !cfg.LoggedIn() {
		failf("Please login first: ghayma login")
		return
	}
	client := api.NewClient(cfg)

	var projectID, projectName, siteID string
	if connectionsSite != "" {
		target, err := resolveConnectionTarget(client, connectionsSite, "list connections for")
		if err != nil {
			reportSiteError(err)
			exitFn(1)
			return
		}
		projectID, projectName, siteID = target.ProjectID, target.ProjectName, target.Site.ID
	} else {
		// Project-wide: no site to pick, so the nearest config is enough — a
		// workspace manifest included.
		var err error
		projectID, _, projectName, err = localConfig()
		if err != nil {
			failf("%v", err)
			return
		}
	}

	rows, err := client.ListConnections(projectID, siteID)
	if err != nil {
		failf("Failed to list connections: %v", err)
		return
	}
	printConnections(projectName, rows, connectionsJSON)
}

// printConnections renders the listing: the API rows as JSON, or the table
// with the project's name above it.
func printConnections(projectName string, rows []api.Connection, asJSON bool) {
	if asJSON {
		if rows == nil {
			rows = []api.Connection{}
		}
		b, _ := json.MarshalIndent(rows, "", "  ")
		fmt.Println(string(b))
		return
	}
	if len(rows) == 0 {
		fmt.Println("No connections yet.")
		fmt.Println("   Connect a service with: ghayma connect <database|bucket|auth> <name>")
		return
	}
	fmt.Printf("🔗 Connections for %s:\n\n", projectName)
	fmt.Print(renderConnectionsTable(rows))
}

func init() {
	connectionsCmd.Flags().StringVar(&connectionsSite, "site", "", "Only this site (name or slug)")
	connectionsCmd.Flags().BoolVar(&connectionsJSON, "json", false, "Print the rows as JSON")
	rootCmd.AddCommand(connectionsCmd)
}
```

Note: `reportSiteError` prints the site-less line (and already exits 1 through `failNoSite`) or the `❌` form; the explicit `exitFn(1)` after it keeps the exit code uniform for the other errors. Double-calling exitFn in the site-less case is harmless under the real `os.Exit` (never returns) and the stubbed one (records 1 twice).

- [ ] **Step 4: Run the tests and build**

Run: `gofmt -l cmd/ && go vet ./cmd/ && go test ./cmd/ -run 'Failf|PrintConnections' && go build -o /dev/null . && go run . connections --help | head -5`
Expected: gofmt lists nothing; tests `ok`; build OK; the help text starts with `List the project's connections`.

- [ ] **Step 5: Commit**

```bash
git add cmd/connections.go cmd/connections_test.go
git commit -m "feat: ghayma connections lists which apps may use which services"
```

---

### Task 4: `ghayma connect` and `ghayma disconnect`

**Files:**
- Create: `cmd/connect.go`
- Modify: `cmd/db_sites.go:20-44` (help pointer only)
- Test: `cmd/connect_test.go`

**Interfaces:**
- Consumes: Task 1 client (`GetSiteConnections`, `AddSiteConnection`, `RemoveSiteConnection`), Task 2 helpers, Task 3 `resolveConnectionTarget`, `failf`, `argChecker(argName, listCmd string, min, max int) cobra.PositionalArgs` (cmd/args.go).
- Produces:
  - `func planConnect(view *api.SiteConnections, kind, name, levelFlag string) (connectPlan, error)`
  - `type connectPlan struct { Item api.ConnectionItem; Held *api.Connection; NoChange bool; Note string }`

- [ ] **Step 1: Write the failing tests**

Create `cmd/connect_test.go`:

```go
package cmd

import (
	"strings"
	"testing"

	"paas-cli/internal/api"
)

func TestPlanConnect_NewResourceDefaultsLevelToTheServer(t *testing.T) {
	plan, err := planConnect(siteView(), "database", "pg-analytics", "")
	if err != nil {
		t.Fatalf("planConnect: %v", err)
	}
	if plan.NoChange || plan.Held != nil || plan.Item.Kind != "database" || plan.Item.ResourceID != "d2" || plan.Item.Level != "" {
		t.Errorf("plan = %+v; want a POST for d2 with no level", plan)
	}
	if plan.Note != "" {
		t.Errorf("no admin note for a database, got %q", plan.Note)
	}
}

func TestPlanConnect_AlreadyConnectedSameLevelIsNoChange(t *testing.T) {
	plan, err := planConnect(siteView(), "auth_app", "shop", "")
	if err != nil || !plan.NoChange || plan.Held == nil || plan.Held.Level != "client" {
		t.Errorf("plan = %+v, %v; want NoChange at client", plan, err)
	}
	plan, err = planConnect(siteView(), "auth_app", "shop", "client")
	if err != nil || !plan.NoChange {
		t.Errorf("explicit same level is still NoChange, got %+v, %v", plan, err)
	}
}

func TestPlanConnect_LevelChangeCarriesTheAdminNote(t *testing.T) {
	plan, err := planConnect(siteView(), "auth_app", "shop", "admin")
	if err != nil {
		t.Fatalf("planConnect: %v", err)
	}
	if plan.NoChange || plan.Item.Level != "admin" || plan.Item.ResourceID != "a1" || plan.Held == nil {
		t.Errorf("plan = %+v; want a level change POST for a1", plan)
	}
	if plan.Note != adminNote {
		t.Errorf("note = %q; want the admin note", plan.Note)
	}
}

func TestPlanConnect_RejectsBadLevelBeforeTheRequest(t *testing.T) {
	_, err := planConnect(siteView(), "bucket", "uploads", "admin")
	if err == nil || !strings.Contains(err.Error(), "read-write") {
		t.Errorf("want an accepted-levels error, got %v", err)
	}
}

func TestPlanConnect_UnknownName(t *testing.T) {
	_, err := planConnect(siteView(), "database", "nope", "")
	if err == nil || !strings.Contains(err.Error(), "no database named") {
		t.Errorf("got %v", err)
	}
}

func TestDisconnectPrompt_YesSkipsAndAnswersGate(t *testing.T) {
	if !confirmDisconnect("database", "pg", "main", true, func() string { t.Fatal("must not prompt with --yes"); return "" }) {
		t.Error("--yes must confirm")
	}
	if confirmDisconnect("database", "pg", "main", false, func() string { return "n" }) {
		t.Error("'n' must cancel")
	}
	if !confirmDisconnect("database", "pg", "main", false, func() string { return "Y" }) {
		t.Error("'Y' must confirm")
	}
}

func TestConnectOutcomeLines(t *testing.T) {
	row := &api.Connection{Kind: "database", ResourceName: "pg", Level: "connect", SiteSlug: "main"}
	if got := connectedLine(row, nil); !strings.Contains(got, "Connected database 'pg' to 'main'") || !strings.Contains(got, "connect") {
		t.Errorf("new connection line = %q", got)
	}
	held := &api.Connection{Kind: "auth_app", ResourceName: "shop", Level: "client", SiteSlug: "main"}
	row = &api.Connection{Kind: "auth_app", ResourceName: "shop", Level: "admin", SiteSlug: "main"}
	if got := connectedLine(row, held); !strings.Contains(got, "client → admin") {
		t.Errorf("level change line = %q", got)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/ -run 'PlanConnect|DisconnectPrompt|ConnectOutcomeLines'`
Expected: compile errors (`undefined: planConnect`, …)

- [ ] **Step 3: Write `cmd/connect.go`**

```go
package cmd

import (
	"fmt"
	"strings"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

var (
	connectSite     string
	connectLevel    string
	disconnectSite  string
	disconnectYes   bool
)

var connectCmd = &cobra.Command{
	Use:   "connect <database|bucket|auth> <name>",
	Short: "Let this app use a database, bucket or auth app",
	Long: `Connect a site (app) to one of the project's services. The app then
receives the service's variables, the network path to a database opens, and
the app's managed platform key gains what the connection implies — on the
running deployment and at every deploy.

Names: a database's or bucket's name, an auth app's app id. The site is the
one this directory is linked to, or --site. The default level is the weakest
one the kind accepts (database: connect, bucket: read-write, auth: client);
'auth ... --level admin' additionally lets the app manage all of that auth
app's users. Running it again with another --level changes the level.

Examples:
  ghayma connect database my-postgres
  ghayma connect bucket uploads --site admin
  ghayma connect auth shop --level admin`,
	Args: argChecker("argument", "connections", 2, 2),
	Run:  runConnect,
}

var disconnectCmd = &cobra.Command{
	Use:   "disconnect <database|bucket|auth> <name>",
	Short: "Stop this app using a database, bucket or auth app",
	Long: `Disconnect a site (app) from one of the project's services. Its variables
leave the app, the network path to a database closes, and the app's managed
platform key loses what the connection implied. Asks for confirmation unless
--yes. Disconnecting something already disconnected is not an error.

Examples:
  ghayma disconnect database my-postgres
  ghayma disconnect auth shop --site admin --yes`,
	Args: argChecker("argument", "connections", 2, 2),
	Run:  runDisconnect,
}

// connectPlan is what runConnect will do once the site view is loaded: the
// item to POST, the connection it replaces (a level change), or nothing.
type connectPlan struct {
	Item     api.ConnectionItem
	Held     *api.Connection
	NoChange bool
	Note     string
}

// planConnect turns (kind, name, --level) into a request against the site's
// current view — pure, so the rules are testable without a server.
func planConnect(view *api.SiteConnections, kind, name, levelFlag string) (connectPlan, error) {
	held, avail, err := findConnectable(view, kind, name)
	if err != nil {
		return connectPlan{}, err
	}
	var accepted []string
	if avail != nil {
		accepted = avail.Levels
	}
	level, err := resolveLevel(kind, levelFlag, accepted)
	if err != nil {
		return connectPlan{}, err
	}

	plan := connectPlan{Held: held}
	if held != nil {
		if level == "" || level == held.Level {
			plan.NoChange = true
			return plan, nil
		}
		plan.Item = api.ConnectionItem{Kind: kind, ResourceID: held.ResourceID, Level: level}
	} else {
		plan.Item = api.ConnectionItem{Kind: kind, ResourceID: avail.ResourceID, Level: level}
	}
	if plan.Item.Level == "admin" {
		plan.Note = adminNote
	}
	return plan, nil
}

// connectedLine is the success line: a new connection, or a level change.
func connectedLine(row, held *api.Connection) string {
	if held != nil {
		return fmt.Sprintf("✅ %s '%s' on '%s': level %s → %s", capitalize(kindLabel(row.Kind)), row.ResourceName, row.SiteSlug, held.Level, row.Level)
	}
	return fmt.Sprintf("✅ Connected %s '%s' to '%s' (%s)", kindLabel(row.Kind), row.ResourceName, row.SiteSlug, row.Level)
}

func runConnect(cmd *cobra.Command, args []string) {
	cfg := config.Load()
	if !cfg.LoggedIn() {
		failf("Please login first: ghayma login")
		return
	}
	kind, err := parseConnectionKind(args[0])
	if err != nil {
		failf("%v", err)
		return
	}
	client := api.NewClient(cfg)
	target, err := resolveConnectionTarget(client, connectSite, "connect a service to")
	if err != nil {
		reportSiteError(err)
		exitFn(1)
		return
	}

	view, err := client.GetSiteConnections(target.ProjectID, target.Site.ID)
	if err != nil {
		failf("Failed to load connections: %v", err)
		return
	}
	plan, err := planConnect(view, kind, args[1], connectLevel)
	if err != nil {
		failf("%v", err)
		return
	}
	if plan.NoChange {
		fmt.Printf("ℹ️  %s '%s' is already connected to '%s' (%s) — no change.\n", kindLabel(kind), plan.Held.ResourceName, target.Site.Slug, plan.Held.Level)
		return
	}
	if plan.Note != "" {
		fmt.Printf("ℹ️  %s\n", plan.Note)
	}

	row, err := client.AddSiteConnection(target.ProjectID, target.Site.ID, plan.Item)
	if err != nil {
		failf("Failed to connect: %v", err)
		return
	}
	if row.SiteSlug == "" {
		row.SiteSlug = target.Site.Slug
	}
	fmt.Println(connectedLine(row, plan.Held))
	fmt.Println("   Variables are live on the running app; pull them locally with: ghayma env pull")
}

// confirmDisconnect is the two-step confirm, skipped by --yes. ask reads one
// token from the terminal; tests inject it.
func confirmDisconnect(kind, name, siteSlug string, yes bool, ask func() string) bool {
	if yes {
		return true
	}
	fmt.Printf("⚠️  This stops '%s' using %s '%s': its variables leave the app and, for a database, the network path closes. Continue? [y/N] ", siteSlug, kindLabel(kind), name)
	answer := strings.TrimSpace(ask())
	return answer == "y" || answer == "Y"
}

func readAnswer() string {
	var s string
	fmt.Scanln(&s)
	return s
}

func runDisconnect(cmd *cobra.Command, args []string) {
	cfg := config.Load()
	if !cfg.LoggedIn() {
		failf("Please login first: ghayma login")
		return
	}
	kind, err := parseConnectionKind(args[0])
	if err != nil {
		failf("%v", err)
		return
	}
	client := api.NewClient(cfg)
	target, err := resolveConnectionTarget(client, disconnectSite, "disconnect a service from")
	if err != nil {
		reportSiteError(err)
		exitFn(1)
		return
	}

	view, err := client.GetSiteConnections(target.ProjectID, target.Site.ID)
	if err != nil {
		failf("Failed to load connections: %v", err)
		return
	}
	held, _, err := findConnectable(view, kind, args[1])
	if err != nil {
		failf("%v", err)
		return
	}
	if held == nil {
		fmt.Printf("ℹ️  %s '%s' is not connected to '%s' — no change.\n", kindLabel(kind), args[1], target.Site.Slug)
		return
	}
	if !confirmDisconnect(kind, held.ResourceName, target.Site.Slug, disconnectYes, readAnswer) {
		fmt.Println("❌ Cancelled.")
		return
	}

	removed, err := client.RemoveSiteConnection(target.ProjectID, target.Site.ID, kind, held.ResourceID)
	if err != nil {
		failf("Failed to disconnect: %v", err)
		return
	}
	if !removed {
		fmt.Printf("ℹ️  %s '%s' was already disconnected from '%s' — no change.\n", kindLabel(kind), held.ResourceName, target.Site.Slug)
		return
	}
	fmt.Printf("✅ Disconnected %s '%s' from '%s'\n", kindLabel(kind), held.ResourceName, target.Site.Slug)
}

func init() {
	connectCmd.Flags().StringVar(&connectSite, "site", "", "Site (app) to connect, by name or slug")
	connectCmd.Flags().StringVar(&connectLevel, "level", "", "Access level (database: connect; bucket: read-write; auth: client|admin)")
	disconnectCmd.Flags().StringVar(&disconnectSite, "site", "", "Site (app) to disconnect, by name or slug")
	disconnectCmd.Flags().BoolVar(&disconnectYes, "yes", false, "Skip the confirmation")
	rootCmd.AddCommand(connectCmd)
	rootCmd.AddCommand(disconnectCmd)
}
```

The `capitalize` helper, placed right above `connectedLine`:

```go
// capitalize upper-cases the first letter of a label for the start of a line.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
```

- [ ] **Step 4: Point `db sites` at the new commands**

In `cmd/db_sites.go`, the `Long` help ends with the `--set main,admin` example line. Append, before the closing backtick, on new lines:

```
See also: ghayma connections, and ghayma connect database <name> --site <slug>
(the same record, with buckets and auth apps too).
```

- [ ] **Step 5: Run the tests and build**

Run: `gofmt -l cmd/ && go vet ./cmd/ && go test ./cmd/ && go build -o /dev/null . && go run . connect --help | head -3 && go run . disconnect --help | head -3`
Expected: gofmt lists nothing; vet clean; `ok  	paas-cli/cmd`; both help texts print.

- [ ] **Step 6: Commit**

```bash
git add cmd/connect.go cmd/connect_test.go cmd/db_sites.go
git commit -m "feat: ghayma connect and disconnect"
```

---

### Task 5: `ghayma env pull`

**Files:**
- Create: `cmd/env_pull.go`
- Modify: `cmd/env.go:512-530` (`init`: register the subcommand)
- Test: `cmd/env_pull_test.go`

**Interfaces:**
- Consumes: Task 1 `GetEffectiveSiteEnv`; Task 3 `resolveConnectionTarget`, `failf`; `envSite` (the env group's persistent `--site`); `sortedKeys(map[string]string) []string` (cmd/env.go).
- Produces:
  - `func renderDotenv(header string, env map[string]string) string`
  - `func dotenvQuote(v string) string`
  - `func envPullRefusal(tracked, ignored, inRepo, force bool, name string) error`
  - `func gitFileState(path string) (tracked, ignored, inRepo bool)`

- [ ] **Step 1: Write the failing tests**

Create `cmd/env_pull_test.go`:

```go
package cmd

import (
	"strings"
	"testing"
)

func TestDotenvQuote(t *testing.T) {
	cases := map[string]string{
		"plain":                      `'plain'`,
		"postgres://u:p$w@h/db":      `'postgres://u:p$w@h/db'`, // single quotes: no $ expansion in any dotenv dialect
		"":                           `''`,
		`it's`:                       `"it's"`,
		"line1\nline2":               `"line1\nline2"`,
		`say "hi"`:                   `'say "hi"'`,
		"it's \"both\"":              `"it's \"both\""`,
		`back\slash and 'quote'`:     `"back\\slash and 'quote'"`,
	}
	for in, want := range cases {
		if got := dotenvQuote(in); got != want {
			t.Errorf("dotenvQuote(%q) = %s; want %s", in, got, want)
		}
	}
}

func TestRenderDotenv_SortedHeaderedAndTrailingNewline(t *testing.T) {
	out := renderDotenv("# hdr", map[string]string{"B": "2", "A": "1", "NEXT_PUBLIC_X": "y"})
	want := "# hdr\nA='1'\nB='2'\nNEXT_PUBLIC_X='y'\n"
	if out != want {
		t.Errorf("renderDotenv =\n%q\nwant\n%q", out, want)
	}
	if got := renderDotenv("# hdr", nil); got != "# hdr\n" {
		t.Errorf("empty env = %q", got)
	}
}

func TestEnvPullRefusal(t *testing.T) {
	if err := envPullRefusal(true, true, true, true, ".env.local"); err == nil || !strings.Contains(err.Error(), "git rm --cached") {
		t.Errorf("a tracked file is refused even with --force, got %v", err)
	}
	if err := envPullRefusal(false, false, true, false, ".env.local"); err == nil || !strings.Contains(err.Error(), ".gitignore") || !strings.Contains(err.Error(), "--force") {
		t.Errorf("an unignored file in a repo is refused with the fix and the override, got %v", err)
	}
	if err := envPullRefusal(false, false, true, true, ".env.local"); err != nil {
		t.Errorf("--force allows an unignored file, got %v", err)
	}
	if err := envPullRefusal(false, true, true, false, ".env.local"); err != nil {
		t.Errorf("an ignored file is fine, got %v", err)
	}
	if err := envPullRefusal(false, false, false, false, ".env.local"); err != nil {
		t.Errorf("outside a repository nothing can be tracked, got %v", err)
	}
}

func TestPullHeaderNamesSiteAndWarns(t *testing.T) {
	h := pullHeader("shop", "main")
	if !strings.HasPrefix(h, "# ") || !strings.Contains(h, "shop/main") || !strings.Contains(h, "ghayma env pull") {
		t.Errorf("header = %q", h)
	}
	for _, line := range strings.Split(strings.TrimRight(h, "\n"), "\n") {
		if !strings.HasPrefix(line, "#") {
			t.Errorf("every header line must be a comment, got %q", line)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/ -run 'DotenvQuote|RenderDotenv|EnvPullRefusal|PullHeader'`
Expected: compile errors (`undefined: dotenvQuote`, …)

- [ ] **Step 3: Write `cmd/env_pull.go`**

```go
package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

var (
	envPullOut   string
	envPullForce bool
)

var envPullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Write the site's effective variables to a local dotenv file",
	Long: `Write the site's EFFECTIVE environment to a local dotenv file: the
variables you set plus every value the platform derives from the site's
connections — database URLs, storage credentials, the auth app's variables,
cron secrets and the managed platform key. It is exactly what the deployed
app receives, so your local app runs against the same services.

The file is .env.local next to the app (its directory in a workspace), or
--out. The values are secrets: the command refuses a file git tracks,
refuses one git does not ignore unless --force, writes it readable by you
only, and never prints a value. Needs the project admin role; every pull is
recorded in the project's audit log.

Examples:
  ghayma env pull
  ghayma env pull --site admin --out .env.development.local`,
	Args: cobra.NoArgs,
	Run:  runEnvPull,
}

// dotenvQuote renders a value so every dotenv dialect (dotenv, @next/env with
// dotenv-expand, godotenv, python-dotenv, docker compose, a shell `source`)
// reads it back literally. Single quotes are the only form none of them
// expand or unescape, so they are the default; a value holding a single
// quote or a line break falls back to double quotes with the escapes those
// dialects agree on.
func dotenvQuote(v string) string {
	if !strings.ContainsAny(v, "'\n\r") {
		return "'" + v + "'"
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`)
	return `"` + r.Replace(v) + `"`
}

// renderDotenv writes the header then KEY=value lines in key order, so two
// pulls of the same site diff cleanly.
func renderDotenv(header string, env map[string]string) string {
	var b strings.Builder
	b.WriteString(header)
	if !strings.HasSuffix(header, "\n") {
		b.WriteString("\n")
	}
	for _, k := range sortedKeys(env) {
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(dotenvQuote(env[k]))
		b.WriteString("\n")
	}
	return b.String()
}

func pullHeader(projectName, siteSlug string) string {
	return fmt.Sprintf("# Generated by `ghayma env pull` for %s/%s on %s\n# Effective variables: exactly what the deployed app receives. Contains secrets — keep this file out of git.\n",
		projectName, siteSlug, time.Now().UTC().Format(time.RFC3339))
}

// envPullRefusal decides whether writing the file is safe. A tracked file is
// never written — the next commit would publish every secret. Inside a
// repository an unignored file is refused too unless --force, since
// `git add .` would stage it.
func envPullRefusal(tracked, ignored, inRepo, force bool, name string) error {
	if tracked {
		return fmt.Errorf("%s is tracked by git — the secrets would land in your next commit. Untrack it (git rm --cached %s), add it to .gitignore, then re-run", name, name)
	}
	if inRepo && !ignored && !force {
		return fmt.Errorf("%s is not git-ignored — add it (echo '%s' >> .gitignore) or pass --force to write it anyway", name, name)
	}
	return nil
}

// gitFileState asks git whether the path is tracked and whether it is ignored.
// No git on PATH, or no repository, reads as "not in a repository": nothing
// can be tracked there. Exit codes: ls-files --error-unmatch is 0 iff tracked;
// check-ignore is 0 iff ignored, 1 iff not, 128 outside a repository.
func gitFileState(path string) (tracked, ignored, inRepo bool) {
	dir, name := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	run := func(args ...string) (int, bool) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Stdout, cmd.Stderr = nil, nil
		if err := cmd.Run(); err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				return exit.ExitCode(), true
			}
			return -1, false // git missing or unrunnable
		}
		return 0, true
	}
	code, ran := run("ls-files", "--error-unmatch", "--", name)
	if !ran || code == 128 {
		return false, false, false
	}
	tracked = code == 0
	code, ran = run("check-ignore", "-q", "--", name)
	if !ran || code == 128 {
		return tracked, false, tracked
	}
	return tracked, code == 0, true
}

func runEnvPull(cmd *cobra.Command, args []string) {
	cfg := config.Load()
	if !cfg.LoggedIn() {
		failf("Please login first: ghayma login")
		return
	}
	client := api.NewClient(cfg)
	target, err := resolveConnectionTarget(client, envSite, "pull env vars for")
	if err != nil {
		reportSiteError(err)
		exitFn(1)
		return
	}

	out := envPullOut
	if out == "" {
		out = filepath.Join(target.AppDir, ".env.local")
	} else if !filepath.IsAbs(out) {
		cwd, _ := os.Getwd()
		out = filepath.Join(cwd, out)
	}
	tracked, ignored, inRepo := gitFileState(out)
	if err := envPullRefusal(tracked, ignored, inRepo, envPullForce, filepath.Base(out)); err != nil {
		failf("%v", err)
		return
	}

	env, err := client.GetEffectiveSiteEnv(target.ProjectID, target.Site.ID)
	if err != nil {
		if strings.Contains(err.Error(), "insufficient role") {
			failf("The project admin role is needed to pull effective variables (they include service credentials) — ask the project owner")
			return
		}
		failf("Failed to pull env vars: %v", err)
		return
	}

	data := renderDotenv(pullHeader(target.ProjectName, target.Site.Slug), env)
	if err := os.WriteFile(out, []byte(data), 0o600); err != nil {
		failf("Failed to write %s: %v", out, err)
		return
	}

	shown := out
	if cwd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(cwd, out); err == nil && !strings.HasPrefix(rel, "..") {
			shown = rel
		}
	}
	fmt.Printf("✅ Wrote %d variables for '%s' to %s\n", len(env), target.Site.Slug, shown)
	for _, k := range sortedKeys(env) {
		fmt.Printf("   %s\n", k)
	}
	fmt.Println("\n   Contains secrets — keep it out of git. Values are never printed here; open the file.")
}
```

- [ ] **Step 4: Register the subcommand**

In `cmd/env.go`'s `init()`, after `envImportCmd.Flags().BoolVar(&envImportForce, …)` add:

```go
	envPullCmd.Flags().StringVar(&envPullOut, "out", "", "File to write (default: .env.local next to the app)")
	envPullCmd.Flags().BoolVar(&envPullForce, "force", false, "Write even if the file is not git-ignored (a tracked file is always refused)")
```

and after `envCmd.AddCommand(envImportCmd)` add:

```go
	envCmd.AddCommand(envPullCmd)
```

- [ ] **Step 5: Run the tests, vet on all three OS targets, build**

Run: `gofmt -l cmd/ && go vet ./cmd/ && GOOS=windows go vet ./cmd/ && go test ./... && go build -o /dev/null . && go run . env pull --help | head -3`
Expected: gofmt lists nothing; both vets clean (Windows must compile: `exec.ExitError.ExitCode()` and `filepath` are portable); every package `ok`; help prints.

- [ ] **Step 6: Commit**

```bash
git add cmd/env_pull.go cmd/env_pull_test.go cmd/env.go
git commit -m "feat: ghayma env pull writes the site's effective variables"
```

---

### Task 6: README and final verification

**Files:**
- Modify: `README.md:86-94` (Environment Variables table) and a new `### Connections` section right after it.

- [ ] **Step 1: Update the README**

In the `### Environment Variables` table, after the `ghayma env delete KEY` row, add:

```
| `ghayma env import <file>` | Import variables from a dotenv file |
| `ghayma env pull` | Write the site's effective variables (connection-derived ones included) to a git-ignored `.env.local` |
```

After that table (before `### Databases`), add:

```
### Connections

A connection lets one site (app) use one of the project's services — a database, a bucket or an auth app — at a level. It is what hands the app its variables, opens the network path to a database and shapes the app's managed platform key.

| Command | Description |
|---|---|
| `ghayma connections [--site <slug>] [--json]` | List which apps may use which services |
| `ghayma connect <database\|bucket\|auth> <name> [--site <slug>] [--level <level>]` | Connect a service to an app (levels: database `connect`, bucket `read-write`, auth `client` or `admin`) |
| `ghayma disconnect <database\|bucket\|auth> <name> [--site <slug>] [--yes]` | Disconnect a service from an app |
```

- [ ] **Step 2: Whole-repo verification**

Run: `gofmt -l . ; go vet ./... && GOOS=windows go vet ./... && go test ./... && go build -o /dev/null .`
Expected: gofmt prints nothing; vets clean; every package `ok`; build OK.

- [ ] **Step 3: Smoke the help tree**

Run: `go run . --help | grep -E 'connect|disconnect|connections' && go run . env --help | grep pull`
Expected: the three top-level commands and `pull` are listed.

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs: connections and env pull in the README"
```

- [ ] **Step 5: Report**

List the commits (`git log --oneline origin/main..HEAD`), paste the Step 2 output, and note anything you deviated from. Do not push and do not open a PR — the reviewer does that.
