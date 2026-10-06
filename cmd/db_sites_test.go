package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"paas-cli/internal/api"
)

// sitesFixture is a project with three sites; only main is granted initially.
// The numbers/ids are test inputs — the command reads every value from the GET
// response at runtime.
func sitesFixture() []api.DatabaseSiteAccess {
	return []api.DatabaseSiteAccess{
		{SiteID: "id-main", Slug: "main", Name: "main", HasAccess: true},
		{SiteID: "id-admin", Slug: "admin", Name: "Admin", HasAccess: false},
		{SiteID: "id-api", Slug: "api", Name: "API", HasAccess: false},
	}
}

// --set replaces the whole set with exactly the named sites, emitted in project
// order (deterministic PUT body), and reports no per-slug no-ops.
func TestComputeDesiredSites_Set(t *testing.T) {
	desired, noops, err := computeDesiredSites(sitesFixture(), "set", []string{"admin", "api"})
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if len(noops) != 0 {
		t.Errorf("set noops = %v; want none", noops)
	}
	if !sameStringSet(desired, []string{"id-admin", "id-api"}) {
		t.Errorf("set desired = %v; want id-admin,id-api (main dropped)", desired)
	}
	// Project order: admin (position 1) before api (position 2).
	if len(desired) != 2 || desired[0] != "id-admin" || desired[1] != "id-api" {
		t.Errorf("set order = %v; want [id-admin id-api] in project order", desired)
	}
}

// --set with no slugs is rejected: deny-all is expressed via --remove, not an
// empty --set (documented in the command help).
func TestComputeDesiredSites_SetEmptyErrors(t *testing.T) {
	if _, _, err := computeDesiredSites(sitesFixture(), "set", nil); err == nil {
		t.Error("--set with no slugs must error (deny-all goes through --remove)")
	}
}

// --add grants the named site(s) while keeping the currently-granted ones.
func TestComputeDesiredSites_Add(t *testing.T) {
	desired, noops, err := computeDesiredSites(sitesFixture(), "add", []string{"admin"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(noops) != 0 {
		t.Errorf("add noops = %v; want none", noops)
	}
	if !sameStringSet(desired, []string{"id-main", "id-admin"}) {
		t.Errorf("add desired = %v; want main+admin", desired)
	}
}

// Adding an already-granted site is a no-op, not an error: it is reported and
// the desired set is left unchanged.
func TestComputeDesiredSites_AddAlreadyGrantedIsNoop(t *testing.T) {
	desired, noops, err := computeDesiredSites(sitesFixture(), "add", []string{"main"})
	if err != nil {
		t.Fatalf("add main: %v", err)
	}
	if len(noops) != 1 || noops[0] != "main" {
		t.Errorf("noops = %v; want [main]", noops)
	}
	if !sameStringSet(desired, []string{"id-main"}) {
		t.Errorf("desired = %v; want unchanged main-only", desired)
	}
}

// --remove revokes the named site(s), keeping the rest.
func TestComputeDesiredSites_Remove(t *testing.T) {
	current := sitesFixture()
	current[1].HasAccess = true // admin also granted
	desired, noops, err := computeDesiredSites(current, "remove", []string{"admin"})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(noops) != 0 {
		t.Errorf("remove noops = %v; want none", noops)
	}
	if !sameStringSet(desired, []string{"id-main"}) {
		t.Errorf("remove desired = %v; want main-only", desired)
	}
}

// Removing an ungranted site is a no-op, not an error.
func TestComputeDesiredSites_RemoveUngrantedIsNoop(t *testing.T) {
	desired, noops, err := computeDesiredSites(sitesFixture(), "remove", []string{"admin"})
	if err != nil {
		t.Fatalf("remove admin: %v", err)
	}
	if len(noops) != 1 || noops[0] != "admin" {
		t.Errorf("noops = %v; want [admin]", noops)
	}
	if !sameStringSet(desired, []string{"id-main"}) {
		t.Errorf("desired = %v; want unchanged main-only", desired)
	}
}

// Removing every granted site yields an empty desired set = deny-all.
func TestComputeDesiredSites_RemoveAllDenyAll(t *testing.T) {
	desired, _, err := computeDesiredSites(sitesFixture(), "remove", []string{"main"})
	if err != nil {
		t.Fatalf("remove main: %v", err)
	}
	if len(desired) != 0 {
		t.Errorf("desired = %v; want empty (deny-all)", desired)
	}
}

// An unknown slug errors, naming the bad slug and the valid slugs.
func TestComputeDesiredSites_UnknownSlugErrors(t *testing.T) {
	_, _, err := computeDesiredSites(sitesFixture(), "add", []string{"nope"})
	if err == nil {
		t.Fatal("unknown slug must error")
	}
	for _, want := range []string{"nope", "main", "admin", "api"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q (bad slug + valid slugs)", err.Error(), want)
		}
	}
}

// splitSlugs splits a comma list into trimmed, non-empty slugs.
func TestSplitSlugs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"main", []string{"main"}},
		{"main,admin", []string{"main", "admin"}},
		{" main , admin ", []string{"main", "admin"}},
		{"main,,admin,", []string{"main", "admin"}},
	}
	for _, c := range cases {
		if got := splitSlugs(c.in); !slices.Equal(got, c.want) {
			t.Errorf("splitSlugs(%q) = %v; want %v", c.in, got, c.want)
		}
	}
}

// grantedIDs returns the site IDs whose has_access flag is set.
func TestGrantedIDs(t *testing.T) {
	current := sitesFixture()
	current[2].HasAccess = true // api granted too
	if got := grantedIDs(current); !sameStringSet(got, []string{"id-main", "id-api"}) {
		t.Errorf("grantedIDs = %v; want main+api", got)
	}
}

// pendingFixture is sitesFixture with main still waiting for the database to
// accept connections instead of granted.
func pendingFixture() []api.DatabaseSiteAccess {
	current := sitesFixture()
	current[0].HasAccess, current[0].Pending = false, true
	return current
}

// --add keeps a site that is still connecting: the PUT names the whole set.
func TestComputeDesiredSites_AddKeepsPending(t *testing.T) {
	desired, noops, err := computeDesiredSites(pendingFixture(), "add", []string{"admin"})
	if err != nil || len(noops) != 0 {
		t.Fatalf("add admin: noops %v, err %v", noops, err)
	}
	if !slices.Equal(desired, []string{"id-main", "id-admin"}) {
		t.Errorf("desired = %v; want [id-main id-admin] (pending main kept)", desired)
	}
}

// --remove of one site keeps the others, pending ones included.
func TestComputeDesiredSites_RemoveKeepsOtherPending(t *testing.T) {
	current := pendingFixture()
	current[1].HasAccess = true // admin granted
	desired, noops, err := computeDesiredSites(current, "remove", []string{"admin"})
	if err != nil || len(noops) != 0 {
		t.Fatalf("remove admin: noops %v, err %v", noops, err)
	}
	if !slices.Equal(desired, []string{"id-main"}) {
		t.Errorf("desired = %v; want [id-main] (pending main kept)", desired)
	}
}

// Adding a site that is already connecting changes nothing.
func TestComputeDesiredSites_AddPendingIsNoop(t *testing.T) {
	desired, noops, err := computeDesiredSites(pendingFixture(), "add", []string{"main"})
	if err != nil {
		t.Fatalf("add main: %v", err)
	}
	if !slices.Equal(noops, []string{"main"}) || !slices.Equal(desired, []string{"id-main"}) {
		t.Errorf("noops %v, desired %v; want [main], [id-main]", noops, desired)
	}
}

// Removing a site that is still connecting cancels its connection.
func TestComputeDesiredSites_RemovePendingCancelsIt(t *testing.T) {
	desired, noops, err := computeDesiredSites(pendingFixture(), "remove", []string{"main"})
	if err != nil || len(noops) != 0 {
		t.Fatalf("remove main: noops %v, err %v", noops, err)
	}
	if len(desired) != 0 {
		t.Errorf("desired = %v; want empty", desired)
	}
}

// A pending site is part of the current set, so an unchanged set skips the PUT.
func TestGrantedIDs_IncludesPending(t *testing.T) {
	current := pendingFixture()
	current[2].HasAccess = true // api granted
	if got := grantedIDs(current); !slices.Equal(got, []string{"id-main", "id-api"}) {
		t.Errorf("grantedIDs = %v; want [id-main id-api]", got)
	}
}

func TestPrintDBSitesTable_MarksPending(t *testing.T) {
	current := pendingFixture()
	current[2].HasAccess = true // api granted
	out := captureStdout(t, func() { printDBSitesTable(current) })
	for _, row := range [][2]string{{"main", "⏳"}, {"Admin", "-"}, {"API", "✓"}} {
		found := false
		for _, line := range strings.Split(out, "\n") {
			if f := strings.Fields(line); len(f) == 3 && f[1] == row[0] {
				found = f[2] == row[1]
			}
		}
		if !found {
			t.Errorf("row %s: want ACCESS %s in:\n%s", row[0], row[1], out)
		}
	}
}

// The help no longer claims a default grant and explains the ⏳ row.
func TestDBSitesHelp(t *testing.T) {
	if strings.Contains(dbSitesCmd.Long, "by default") {
		t.Errorf("help still claims a default grant:\n%s", dbSitesCmd.Long)
	}
	for _, want := range []string{
		"A database reaches only the sites connected to it",
		"A site marked ⏳ is connecting once the database accepts connections; --add and --remove of other sites keep it; --remove <that site> cancels it.",
	} {
		if !strings.Contains(strings.Join(strings.Fields(dbSitesCmd.Long), " "), want) {
			t.Errorf("help lacks %q:\n%s", want, dbSitesCmd.Long)
		}
	}
}

// dbSitesAPI serves database shop-db (d1), whose project's sites are rows, and
// records every request and the body of a PUT.
type dbSitesAPI struct {
	URL   string
	mu    sync.Mutex
	calls []string
	put   string
}

func dbSitesStub(t *testing.T, rows string) *dbSitesAPI {
	t.Helper()
	stub := &dbSitesAPI{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.mu.Lock()
		defer stub.mu.Unlock()
		stub.calls = append(stub.calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/databases":
			io.WriteString(w, `{"databases":[{"id":"d1","name":"shop-db","type":"postgres"}]}`)
		case r.URL.Path == "/api/v1/databases/d1/sites":
			if r.Method == http.MethodPut {
				raw, _ := io.ReadAll(r.Body)
				stub.put = string(raw)
			}
			io.WriteString(w, `{"sites":`+rows+`}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)
	stub.URL = ts.URL
	return stub
}

func (a *dbSitesAPI) putBody() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.put
}

// pendingRows is a project whose main site is still connecting to shop-db.
const pendingRows = `[{"site_id":"s1","slug":"main","name":"main","has_access":false,"pending":true},` +
	`{"site_id":"s2","slug":"admin","name":"Admin","has_access":false}]`

func TestDBSites_PendingSiteStaysConnecting(t *testing.T) {
	stub := dbSitesStub(t, pendingRows)
	cliHome(t, stub.URL)

	out := runCLI(t, t.TempDir(), "db", "sites", "shop-db")
	if !strings.Contains(out, "✓ = allowed   ⏳ = connecting once the database accepts connections   - = blocked") {
		t.Errorf("missing the legend:\n%s", out)
	}

	out = runCLI(t, t.TempDir(), "db", "sites", "shop-db", "--add", "main")
	if !strings.Contains(out, "ℹ️  'main' is already connecting — no change.") {
		t.Errorf("missing the no-op line:\n%s", out)
	}
	if got := stub.putBody(); got != "" {
		t.Errorf("an already-connecting site needs no PUT, sent %s", got)
	}

	runCLI(t, t.TempDir(), "db", "sites", "shop-db", "--add", "admin")
	if got := stub.putBody(); got != `{"site_ids":["s1","s2"]}` {
		t.Errorf("PUT body = %s; want main kept beside admin", got)
	}
}

// sameStringSet is order-insensitive set equality used to detect a no-op PUT.
func TestSameStringSet(t *testing.T) {
	if !sameStringSet([]string{"a", "b"}, []string{"b", "a"}) {
		t.Error("order must not matter")
	}
	if sameStringSet([]string{"a"}, []string{"a", "b"}) {
		t.Error("different sizes are not equal")
	}
	if sameStringSet([]string{"a"}, []string{"b"}) {
		t.Error("different members are not equal")
	}
	if !sameStringSet(nil, nil) {
		t.Error("two empties are equal")
	}
}
