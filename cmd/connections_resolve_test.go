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
