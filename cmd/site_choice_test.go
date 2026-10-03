package cmd

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/manifoldco/promptui"
)

// Ask before connecting (2026-10-02): a new database, bucket or auth app is
// connected to the sites the user chose — --site, --no-connect, or one question
// per site on a terminal — and to nothing else.

// shopSites is a project with a production site, a development one and one
// from a server that predates environments.
const shopSites = `[{"id":"s1","name":"main","slug":"main","environment":"production"},` +
	`{"id":"s2","name":"admin","slug":"admin","environment":"development"},` +
	`{"id":"s3","name":"legacy","slug":"legacy"}]`

// createAPI stands in for the backend of the three create commands: it serves
// project p1's site listing, 404s the catalog (so no pricing question runs),
// answers the create with created, and records every request and the create
// body it received.
type createAPI struct {
	URL   string
	mu    sync.Mutex
	calls []string
	body  map[string]json.RawMessage
}

func (a *createAPI) seen() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.calls...)
}

// sentSites is the create body's connect_site_ids, raw — "" when no create
// went out.
func (a *createAPI) sentSites() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return string(a.body["connect_site_ids"])
}

func (a *createAPI) created() bool {
	for _, c := range a.seen() {
		if strings.HasPrefix(c, "POST ") {
			return true
		}
	}
	return false
}

func createStub(t *testing.T, sites, created string) *createAPI {
	t.Helper()
	stub := &createAPI{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.mu.Lock()
		defer stub.mu.Unlock()
		stub.calls = append(stub.calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/p1/sites":
			io.WriteString(w, sites)
		case r.Method == http.MethodPost && (r.URL.Path == "/api/v1/databases" || r.URL.Path == "/api/v1/storage" || r.URL.Path == "/api/v1/auth-apps"):
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &stub.body)
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, created)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)
	stub.URL = ts.URL
	return stub
}

func (a *createAPI) client() *api.Client {
	return api.NewClient(&config.Config{APIHost: a.URL, Token: "t"})
}

// siteQuestion is one question put to the user and the answer it starts on.
type siteQuestion struct {
	Label      string
	DefaultYes bool
}

// answerSiteQuestions stands in for the terminal: it records every question
// and gives the answers in order.
func answerSiteQuestions(t *testing.T, answers ...bool) *[]siteQuestion {
	t.Helper()
	var asked []siteQuestion
	orig := promptConnectSiteFn
	t.Cleanup(func() { promptConnectSiteFn = orig })
	promptConnectSiteFn = func(label string, defaultYes bool) (bool, error) {
		asked = append(asked, siteQuestion{label, defaultYes})
		if len(asked) > len(answers) {
			t.Errorf("unexpected question %q", label)
			return false, nil
		}
		return answers[len(asked)-1], nil
	}
	return &asked
}

// noSiteQuestions fails the test if a question is put at all.
func noSiteQuestions(t *testing.T) {
	t.Helper()
	answerSiteQuestions(t)
}

func TestResolveSiteChoice_SiteAndNoConnectRefusedBeforeAnyCall(t *testing.T) {
	forceStdin(t, true)
	noSiteQuestions(t)
	stub := createStub(t, shopSites, "")

	_, _, _, err := resolveSiteChoice(stub.client(), "p1", []string{"main"}, true, "database", "shop-db")
	if err == nil || !strings.Contains(err.Error(), "--site") || !strings.Contains(err.Error(), "--no-connect") {
		t.Fatalf("err = %v; want a refusal naming both flags", err)
	}
	if calls := stub.seen(); len(calls) != 0 {
		t.Errorf("the refusal must come before any call, got %v", calls)
	}
}

func TestResolveSiteChoice_NoConnectChoosesNothingWithoutAsking(t *testing.T) {
	forceStdin(t, true)
	noSiteQuestions(t)
	stub := createStub(t, shopSites, "")

	ids, _, note, err := resolveSiteChoice(stub.client(), "p1", nil, true, "database", "shop-db")
	if err != nil || ids == nil || len(ids) != 0 || note != "" {
		t.Fatalf("ids, note, err = %#v, %q, %v; want [], no note, nil", ids, note, err)
	}
	if calls := stub.seen(); len(calls) != 0 {
		t.Errorf("--no-connect needs no site listing, got %v", calls)
	}
}

func TestResolveSiteChoice_SiteFlagsResolveToIDs(t *testing.T) {
	forceStdin(t, true)
	noSiteQuestions(t)
	stub := createStub(t, shopSites, "")

	ids, sites, note, err := resolveSiteChoice(stub.client(), "p1", []string{"admin", "main", "Admin"}, false, "bucket", "media")
	if err != nil {
		t.Fatalf("resolveSiteChoice: %v", err)
	}
	if !reflect.DeepEqual(ids, []string{"s2", "s1"}) {
		t.Errorf("ids = %v; want [s2 s1] in flag order, once each", ids)
	}
	if len(sites) != 3 || note != "" {
		t.Errorf("sites = %d, note = %q; want the 3 listed sites and no note", len(sites), note)
	}
}

func TestResolveSiteChoice_UnknownSiteListsTheSites(t *testing.T) {
	forceStdin(t, true)
	noSiteQuestions(t)
	stub := createStub(t, shopSites, "")

	_, _, _, err := resolveSiteChoice(stub.client(), "p1", []string{"main", "nope"}, false, "database", "shop-db")
	if err == nil || !strings.Contains(err.Error(), `"nope"`) || !strings.Contains(err.Error(), "main, admin, legacy") {
		t.Fatalf("err = %v; want the unknown slug and the project's sites", err)
	}

	empty := createStub(t, `[]`, "")
	_, _, _, err = resolveSiteChoice(empty.client(), "p1", []string{"main"}, false, "database", "shop-db")
	if err == nil || !strings.Contains(err.Error(), `"main"`) || !strings.Contains(err.Error(), "no site yet") {
		t.Errorf("site-less project: err = %v; want the slug and that there is no site yet", err)
	}
}

func TestResolveSiteChoice_ListingFailureStops(t *testing.T) {
	forceStdin(t, true)
	noSiteQuestions(t)
	stub := createStub(t, shopSites, "")

	if _, _, _, err := resolveSiteChoice(stub.client(), "p-missing", nil, false, "database", "shop-db"); err == nil {
		t.Error("a failed site listing must stop the create, not skip the question")
	}
}

func TestResolveSiteChoice_TerminalWithoutSitesSaysDeployWillOffer(t *testing.T) {
	forceStdin(t, true)
	noSiteQuestions(t)
	stub := createStub(t, `[]`, "")

	ids, _, note, err := resolveSiteChoice(stub.client(), "p1", nil, false, "database", "shop-db")
	if err != nil || ids == nil || len(ids) != 0 {
		t.Fatalf("ids, err = %#v, %v; want [], nil", ids, err)
	}
	if note != "No site yet — `ghayma deploy` will offer to connect it." {
		t.Errorf("note = %q", note)
	}
}

func TestResolveSiteChoice_TerminalOneSiteAsksYesFirst(t *testing.T) {
	forceStdin(t, true)
	oneSiteJSON := `[{"id":"s1","name":"main","slug":"main","environment":"production"}]`

	for _, tc := range []struct {
		kind, name, label string
		answer            bool
		want              []string
	}{
		{"database", "shop-db", "Connect database 'shop-db' to site 'main'?", true, []string{"s1"}},
		{"bucket", "media", "Connect bucket 'media' to site 'main'?", true, []string{"s1"}},
		{"auth_app", "shop", "Connect auth app 'shop' to site 'main'?", false, []string{}},
	} {
		asked := answerSiteQuestions(t, tc.answer)
		stub := createStub(t, oneSiteJSON, "")

		ids, _, note, err := resolveSiteChoice(stub.client(), "p1", nil, false, tc.kind, tc.name)
		if err != nil || !reflect.DeepEqual(ids, tc.want) || note != "" {
			t.Errorf("%s: ids, note, err = %#v, %q, %v; want %#v, no note", tc.kind, ids, note, err, tc.want)
		}
		if want := []siteQuestion{{tc.label, true}}; !reflect.DeepEqual(*asked, want) {
			t.Errorf("%s: asked %+v; want %+v", tc.kind, *asked, want)
		}
	}
}

func TestResolveSiteChoice_TerminalSeveralSitesAsksEachDefaultNo(t *testing.T) {
	forceStdin(t, true)
	asked := answerSiteQuestions(t, false, true, true)
	stub := createStub(t, shopSites, "")

	ids, sites, note, err := resolveSiteChoice(stub.client(), "p1", nil, false, "database", "shop-db")
	if err != nil || note != "" {
		t.Fatalf("note, err = %q, %v", note, err)
	}
	if !reflect.DeepEqual(ids, []string{"s2", "s3"}) {
		t.Errorf("ids = %v; want the two sites answered Yes", ids)
	}
	if len(sites) != 3 {
		t.Errorf("sites = %d; want the listing back for the outcome", len(sites))
	}
	want := []siteQuestion{
		{"Connect to 'main' (production)?", false},
		{"Connect to 'admin' (development)?", false},
		{"Connect to 'legacy'?", false},
	}
	if !reflect.DeepEqual(*asked, want) {
		t.Errorf("asked %+v; want %+v", *asked, want)
	}
}

func TestResolveSiteChoice_CancelStopsBeforeTheCreate(t *testing.T) {
	forceStdin(t, true)
	orig := promptConnectSiteFn
	t.Cleanup(func() { promptConnectSiteFn = orig })
	promptConnectSiteFn = func(string, bool) (bool, error) { return false, promptui.ErrInterrupt }
	stub := createStub(t, shopSites, "")

	_, _, _, err := resolveSiteChoice(stub.client(), "p1", nil, false, "database", "shop-db")
	if !errors.Is(err, errAttachCancelled) {
		t.Errorf("err = %v; want the cancel", err)
	}
}

func TestResolveSiteChoice_NotATerminalConnectsNothingAndSaysHow(t *testing.T) {
	forceStdin(t, false)
	noSiteQuestions(t)

	for _, tc := range []struct{ kind, name, note string }{
		{"database", "shop-db", "Not connected. Connect it with: ghayma connect database shop-db --site main"},
		{"bucket", "media", "Not connected. Connect it with: ghayma connect bucket media --site main"},
		{"auth_app", "shop", "Not connected. Connect it with: ghayma connect auth shop --site main"},
	} {
		stub := createStub(t, shopSites, "")
		ids, _, note, err := resolveSiteChoice(stub.client(), "p1", nil, false, tc.kind, tc.name)
		if err != nil || ids == nil || len(ids) != 0 {
			t.Errorf("%s: ids, err = %#v, %v; want [], nil", tc.kind, ids, err)
		}
		if note != tc.note {
			t.Errorf("%s: note = %q; want %q", tc.kind, note, tc.note)
		}
		if calls := stub.seen(); !reflect.DeepEqual(calls, []string{"GET /api/v1/projects/p1/sites"}) {
			t.Errorf("%s: requests = %v; want the one site listing", tc.kind, calls)
		}
	}
}

// A script's hint names the site the server would pick — the default site,
// else main, else the only one — and keeps the placeholder when the list is
// empty, unreadable or names no such site. A listing that fails never stops
// the create.
func TestResolveSiteChoice_NotATerminalNamesTheServersSite(t *testing.T) {
	forceStdin(t, false)
	noSiteQuestions(t)

	for _, tc := range []struct {
		name, project, sites, want string
	}{
		{"the default site", "p1", mainAndDefaultWWW, "www"},
		{"main when none is default", "p1", shopSites, "main"},
		{"the only site", "p1", `[{"id":"s2","name":"www","slug":"www"}]`, "www"},
		{"no site yet", "p1", `[]`, "<slug>"},
		{"several, none default or main", "p1", `[{"id":"s2","name":"www","slug":"www"},{"id":"s3","name":"admin","slug":"admin"}]`, "<slug>"},
		{"list unreadable", "p-missing", shopSites, "<slug>"},
	} {
		stub := createStub(t, tc.sites, "")
		ids, _, note, err := resolveSiteChoice(stub.client(), tc.project, nil, false, "database", "shop-db")
		if err != nil || ids == nil || len(ids) != 0 {
			t.Errorf("%s: ids, err = %#v, %v; want [], nil", tc.name, ids, err)
		}
		if want := "Not connected. Connect it with: ghayma connect database shop-db --site " + tc.want; note != want {
			t.Errorf("%s: note = %q; want %q", tc.name, note, want)
		}
	}
}

// The --site flags still apply without a terminal: only the question needs one.
func TestResolveSiteChoice_NotATerminalTakesTheFlags(t *testing.T) {
	forceStdin(t, false)
	noSiteQuestions(t)
	stub := createStub(t, shopSites, "")

	ids, _, note, err := resolveSiteChoice(stub.client(), "p1", []string{"legacy"}, false, "database", "shop-db")
	if err != nil || !reflect.DeepEqual(ids, []string{"s3"}) || note != "" {
		t.Errorf("ids, note, err = %v, %q, %v; want [s3], no note", ids, note, err)
	}
}

func TestPrintConnectOutcome_NamesEachSiteBySlug(t *testing.T) {
	sites := []api.Site{{ID: "s1", Slug: "main"}, {ID: "s2", Slug: "admin"}, {ID: "s3", Slug: "staging"}}
	outcome := api.ConnectChoice{
		Connected: []string{"s2", "s9"},
		Pending:   []string{"s1"},
		Failed:    []api.SiteConnectFailure{{SiteID: "s3", Error: "you need write access to this site"}},
	}
	out := captureStdout(t, func() { printConnectOutcome(&outcome, []string{"s1", "s2", "s3"}, sites, "", "database", "shop-db") })

	for _, want := range []string{
		"Connected to 'admin'",
		"Connected to 's9'",
		"Connecting to 'main' once it is ready to accept connections",
		"Not connected to 'staging': you need write access to this site\n   Retry with: ghayma connect database shop-db --site staging\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "couldn't read") {
		t.Errorf("an outcome that accounts for the choice must not doubt it:\n%s", out)
	}
}

// Buckets and auth apps share the outcome lines, so a pending one never reads
// as a database.
func TestPrintConnectOutcome_PendingIsKindNeutral(t *testing.T) {
	sites := []api.Site{{ID: "s1", Slug: "main"}}
	out := captureStdout(t, func() {
		printConnectOutcome(&api.ConnectChoice{Pending: []string{"s1"}}, []string{"s1"}, sites, "", "bucket", "media")
	})
	if out != "⏳ Connecting to 'main' once it is ready to accept connections\n" {
		t.Errorf("printed %q", out)
	}
}

func TestPrintConnectOutcome_UnreadableResultSaysWhereToCheck(t *testing.T) {
	const line = "couldn't read the connection result — check with: ghayma connections\n"
	sites := []api.Site{{ID: "s1", Slug: "main"}}

	for _, tc := range []struct {
		name    string
		outcome *api.ConnectChoice
		chosen  []string
		want    bool
	}{
		{"an answer naming no site", &api.ConnectChoice{}, []string{"s1"}, true},
		{"only other sites", &api.ConnectChoice{Connected: []string{"s7"}}, []string{"s1"}, true},
		{"nothing chosen", &api.ConnectChoice{}, []string{}, false},
		{"pending counts", &api.ConnectChoice{Pending: []string{"s1"}}, []string{"s1"}, false},
		{"failed counts", &api.ConnectChoice{Failed: []api.SiteConnectFailure{{SiteID: "s1", Error: "x"}}}, []string{"s1"}, false},
		// An older server gets its own warning instead.
		{"older server", nil, []string{"s1"}, false},
	} {
		out := captureStdout(t, func() { printConnectOutcome(tc.outcome, tc.chosen, sites, "", "database", "shop-db") })
		if got := strings.Contains(out, line); got != tc.want {
			t.Errorf("%s: line printed = %v; want %v:\n%s", tc.name, got, tc.want, out)
		}
	}
}

func TestPrintConnectOutcome_PrintsTheNote(t *testing.T) {
	out := captureStdout(t, func() {
		printConnectOutcome(&api.ConnectChoice{}, []string{}, nil, "Not connected. Connect it with: ghayma connect bucket media --site <slug>", "bucket", "media")
	})
	if !strings.Contains(out, "Not connected. Connect it with: ghayma connect bucket media --site <slug>\n") {
		t.Errorf("note missing:\n%s", out)
	}
}

// A server that predates the choice sends no connections and may still have
// connected the service on its own: the create says so and where to check —
// never "not connected", nor that a deploy will offer it — and keeps the
// command that connects it.
func TestPrintConnectOutcome_OlderServerMayHaveConnected(t *testing.T) {
	const warning = "⚠️  this server didn't report connections — it may have connected the auth app to the project's main site; check with: ghayma connections\n"
	sites := []api.Site{{ID: "s1", Slug: "main"}}
	for _, tc := range []struct {
		name, note, hint string
		chosen           []string
	}{
		{"not a terminal", "Not connected. Connect it with: ghayma connect auth shop --site main", "ℹ️  Connect it with: ghayma connect auth shop --site main\n", []string{}},
		{"no site yet", "No site yet — `ghayma deploy` will offer to connect it.", "", []string{}},
		{"--no-connect", "", "", []string{}},
		{"a site chosen", "", "", []string{"s1"}},
	} {
		out := captureStdout(t, func() { printConnectOutcome(nil, tc.chosen, sites, tc.note, "auth_app", "shop") })
		if want := warning + tc.hint; out != want {
			t.Errorf("%s: printed %q; want %q", tc.name, out, want)
		}
	}
}

// A display name never wins over another site's slug: every slug is tried
// before any name, and names before ids — for create's --site and for the
// live pick alike.
func TestSiteFlag_SlugWinsOverAnotherSitesName(t *testing.T) {
	sites := []api.Site{
		{ID: "s1", Name: "admin", Slug: "main"},
		{ID: "s2", Name: "Admin Console", Slug: "admin"},
	}
	if s, err := liveSiteFor(sites, "admin"); err != nil || s.ID != "s2" {
		t.Errorf("liveSiteFor(admin) = %v, %v; want s2, the site whose slug is admin", s, err)
	}
	if ids, err := siteIDsByFlag(sites, []string{"admin"}); err != nil || !reflect.DeepEqual(ids, []string{"s2"}) {
		t.Errorf("siteIDsByFlag(admin) = %v, %v; want [s2]", ids, err)
	}
	// A name or an id still names a site when no slug does.
	if s, err := liveSiteFor(sites, "admin console"); err != nil || s.ID != "s2" {
		t.Errorf("by name: %v, %v", s, err)
	}
	if ids, err := siteIDsByFlag(sites, []string{"S1"}); err != nil || !reflect.DeepEqual(ids, []string{"s1"}) {
		t.Errorf("by id: %v, %v", ids, err)
	}
}

// Both pickers refuse an unknown site with the same sentence.
func TestSiteFlag_UnknownSiteIsOneSentence(t *testing.T) {
	sites := []api.Site{{ID: "s1", Slug: "main"}, {ID: "s2", Slug: "admin"}}
	_, live := liveSiteFor(sites, "nope")
	_, flags := siteIDsByFlag(sites, []string{"nope"})
	const want = `site "nope" not found in this project (available: main, admin)`
	if live == nil || flags == nil || live.Error() != want || flags.Error() != want {
		t.Errorf("liveSiteFor: %v; siteIDsByFlag: %v; want %q from both", live, flags, want)
	}
}

// The Select opens on its first item, so Enter alone takes the default.
func TestYesNoItems_DefaultFirst(t *testing.T) {
	if got := yesNoItems(true); !reflect.DeepEqual(got, []string{"Yes", "No"}) {
		t.Errorf("default Yes: %q; want Yes first", got)
	}
	if got := yesNoItems(false); !reflect.DeepEqual(got, []string{"No", "Yes"}) {
		t.Errorf("default No: %q; want No first", got)
	}
}
