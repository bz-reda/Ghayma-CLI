package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"paas-cli/internal/api"

	"github.com/manifoldco/promptui"
)

// Ask before connecting, at deploy (2026-10-02): a service no site holds is
// offered to the site being deployed before the upload starts, and only the
// services answered Yes ride on it. A script, --no-connect or a No connects
// nothing and is told the command that would.

// shopServices are a project's services that no site holds, in listing order.
var shopServices = []api.Unconnected{
	{Kind: "database", ResourceID: "d1", ResourceName: "shop-db", DefaultLevel: "connect"},
	{Kind: "bucket", ResourceID: "b1", ResourceName: "media", DefaultLevel: "read-write"},
	{Kind: "auth_app", ResourceID: "a1", ResourceName: "shop", DefaultLevel: "client"},
}

// deployAPI stands in for the backend of a source deploy: the site listing,
// the services no site holds, the upload and the deployment poll. The fields
// above URL are set before start and only read by the handler.
type deployAPI struct {
	sites       string // GET …/sites
	sitesStatus int    // the site list's status, 200 when 0
	listStatus  int    // the listing's status, 200 when 0
	listBody    string // the listing's body
	dropList    bool   // the listing's connection is dropped instead
	uploadReply string // the upload's 201 body; "" answers as a current server, connecting what was sent

	URL     string
	mu      sync.Mutex
	calls   []string
	connect string // the upload's connect_resources field, "" when it carried none
	siteID  string // the upload's site_id field, "" when it carried none
}

func (a *deployAPI) start(t *testing.T) *deployAPI {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.calls = append(a.calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/projects/p1/connections/unconnected":
			if a.dropList {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					conn.Close()
				}
				return
			}
			if a.listStatus != 0 {
				w.WriteHeader(a.listStatus)
			}
			io.WriteString(w, a.listBody)
		case r.URL.Path == "/api/v1/deploy/upload":
			if err := r.ParseMultipartForm(32 << 20); err != nil {
				t.Errorf("upload is not a multipart form: %v", err)
			}
			a.connect = r.FormValue("connect_resources")
			a.siteID = r.FormValue("site_id")
			reply := a.uploadReply
			if reply == "" {
				connected := a.connect
				if connected == "" {
					connected = "[]"
				}
				landed := a.siteID
				if landed == "" {
					landed = "s1"
				}
				reply = `{"deployment_id":"dep-1","status":"queued","site_id":"` + landed + `","connections":{"connected":` + connected + `,"failed":[]}}`
			}
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, reply)
		case strings.HasSuffix(r.URL.Path, "/sites"):
			if a.sitesStatus != 0 {
				w.WriteHeader(a.sitesStatus)
			}
			io.WriteString(w, a.sites)
		case r.URL.Path == "/api/v1/deployments/dep-1":
			io.WriteString(w, `{"id":"dep-1","status":"live","domains":["shop.ghayma.app"]}`)
		case r.URL.Path == "/api/v1/projects/p1":
			io.WriteString(w, `{"id":"p1","custom_dockerfile_enabled":false}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"unexpected `+r.URL.Path+`"}`)
		}
	}))
	t.Cleanup(ts.Close)
	a.URL = ts.URL
	return a
}

func (a *deployAPI) seen() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.calls...)
}

func (a *deployAPI) uploaded() bool {
	return uploaded(a.seen())
}

// connectField is the upload's connect_resources, "" when it carried none.
func (a *deployAPI) connectField() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.connect
}

// uploadSiteID is the upload's site_id, "" when it left the site to the server.
func (a *deployAPI) uploadSiteID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.siteID
}

// unconnectedBody is the listing's answer for the given services.
func unconnectedBody(t *testing.T, services []api.Unconnected) string {
	t.Helper()
	raw, err := json.Marshal(map[string][]api.Unconnected{"unconnected": services})
	if err != nil {
		t.Fatalf("marshal the listing: %v", err)
	}
	return string(raw)
}

// uploadStubWithUnconnected serves a site-less project — its first deploy
// creates main — whose services no site holds.
func uploadStubWithUnconnected(t *testing.T, services []api.Unconnected) *deployAPI {
	t.Helper()
	return (&deployAPI{sites: `[]`, listBody: unconnectedBody(t, services)}).start(t)
}

// answerDeployQuestions stands in for the terminal: it records each question
// and gives the answers in order. A question put once the upload has started
// fails the test.
func answerDeployQuestions(t *testing.T, stub *deployAPI, answers ...bool) *[]string {
	t.Helper()
	var asked []string
	orig := promptDeployConnectFn
	t.Cleanup(func() { promptDeployConnectFn = orig })
	promptDeployConnectFn = func(label string) (bool, error) {
		asked = append(asked, label)
		if stub != nil && stub.uploaded() {
			t.Errorf("question %q put after the upload started", label)
		}
		if len(asked) > len(answers) {
			t.Errorf("unexpected question %q", label)
			return false, nil
		}
		return answers[len(asked)-1], nil
	}
	return &asked
}

// noDeployQuestions fails the test if a question is put at all.
func noDeployQuestions(t *testing.T) {
	t.Helper()
	answerDeployQuestions(t, nil)
}

// deploySetup is what every command-level deploy test needs besides its stub.
func deploySetup(t *testing.T, tty bool) {
	t.Helper()
	forceStdin(t, tty)
	noPrompt(t)
	fastPolls(t)
}

func TestDeployNonInteractiveConnectsNothingAndSaysHow(t *testing.T) {
	deploySetup(t, false)
	noDeployQuestions(t)
	stub := uploadStubWithUnconnected(t, shopServices[:1])
	cliHome(t, stub.URL)

	out := runCLI(t, siteLessDir(t), "deploy")

	if !stub.uploaded() {
		t.Fatalf("requests = %v; the deploy must still upload\n%s", stub.seen(), out)
	}
	if got := stub.connectField(); got != "" {
		t.Fatalf("a non-interactive deploy must not connect, sent %q", got)
	}
	notice := "ℹ️  Not connected to any site: database 'shop-db'.\n" +
		"   Connect with:\n" +
		"     ghayma connect database shop-db --site main\n"
	if !strings.Contains(out, notice) {
		t.Fatalf("missing the notice %q:\n%s", notice, out)
	}
}

func TestDeployInteractiveYesCarriesTheChoice(t *testing.T) {
	deploySetup(t, true)
	stub := uploadStubWithUnconnected(t, shopServices[:1])
	answerDeployQuestions(t, stub, true)
	cliHome(t, stub.URL)

	out := runCLI(t, siteLessDir(t), "deploy")

	if got := stub.connectField(); got != `[{"kind":"database","resource_id":"d1"}]` {
		t.Fatalf("choice not sent: %q", got)
	}
	if !strings.Contains(out, "🔗 Connected database 'shop-db' to 'main'.\n") {
		t.Errorf("missing the outcome:\n%s", out)
	}
	if strings.Contains(out, "Not connected to any site") {
		t.Errorf("a service answered Yes must not be listed as unconnected:\n%s", out)
	}
}

// The listing never blocks a deploy: a project key is refused it by design, a
// server can fail it, a network can drop it. One warning line, nothing
// connected, and the upload still happens — on a terminal too.
func TestDeployProceedsWhenTheListFails(t *testing.T) {
	const warning = "⚠️  couldn't check for services no site uses: "
	for _, tc := range []struct {
		name   string
		stub   *deployAPI
		reason string
	}{
		{"project key", &deployAPI{sites: `[]`, listStatus: http.StatusForbidden, listBody: `{"error":"project keys cannot list unconnected services"}`}, "project keys cannot list unconnected services"},
		{"server error", &deployAPI{sites: `[]`, listStatus: http.StatusInternalServerError, listBody: `{"error":"boom"}`}, "boom"},
		// The transport's wording differs by OS, so only its shape is checked below.
		{"dropped connection", &deployAPI{sites: `[]`, dropList: true}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deploySetup(t, true)
			noDeployQuestions(t)
			stub := tc.stub.start(t)
			cliHome(t, stub.URL)

			out := runCLI(t, siteLessDir(t), "deploy")

			if !stub.uploaded() {
				t.Fatalf("requests = %v; a failed listing must not stop the deploy\n%s", stub.seen(), out)
			}
			if got := stub.connectField(); got != "" {
				t.Errorf("connect_resources = %q; want none", got)
			}
			if n := strings.Count(out, warning); n != 1 {
				t.Fatalf("want exactly one warning line, got %d:\n%s", n, out)
			}
			line := out[strings.Index(out, warning):]
			line = line[:strings.Index(line, "\n")]
			if !strings.Contains(line, tc.reason) {
				t.Errorf("warning %q; want the reason %q", line, tc.reason)
			}
			// The reason is the failure's own, not the request it failed.
			if reason := strings.TrimPrefix(line, warning); reason == "" || strings.Contains(reason, stub.URL) || strings.Contains(reason, "/connections/unconnected") {
				t.Errorf("warning %q; want a short reason without the request", line)
			}
			if !strings.Contains(out, "✅ Deployed successfully!") {
				t.Errorf("the deploy must run to the end:\n%s", out)
			}
		})
	}
}

// --no-connect answers for every service at once, even on a terminal, and
// each is listed with the command that connects it — an auth app in connect's
// own word for it.
func TestDeployNoConnectSkipsTheQuestion(t *testing.T) {
	deploySetup(t, true)
	noDeployQuestions(t)
	stub := uploadStubWithUnconnected(t, shopServices)
	cliHome(t, stub.URL)

	out := runCLI(t, siteLessDir(t), "deploy", "--no-connect")

	if !stub.uploaded() {
		t.Fatalf("requests = %v; want the upload\n%s", stub.seen(), out)
	}
	if got := stub.connectField(); got != "" {
		t.Errorf("--no-connect must connect nothing, sent %q", got)
	}
	notice := "ℹ️  Not connected to any site: database 'shop-db', bucket 'media', auth app 'shop'.\n" +
		"   Connect with:\n" +
		"     ghayma connect database shop-db --site main\n" +
		"     ghayma connect bucket media --site main\n" +
		"     ghayma connect auth shop --site main\n"
	if !strings.Contains(out, notice) {
		t.Errorf("missing the notice %q:\n%s", notice, out)
	}
}

// The deploy puts the create commands' No/Yes question, with Yes first.
func TestPromptDeployConnect_YesFirst(t *testing.T) {
	asked := answerSiteQuestions(t, true)
	const label = "Database 'shop-db' isn't connected to any site. Connect it to 'main'?"

	yes, err := promptDeployConnect(label)

	if err != nil || !yes {
		t.Fatalf("answer = %v, %v; want the Yes given", yes, err)
	}
	if want := []siteQuestion{{label, true}}; !reflect.DeepEqual(*asked, want) {
		t.Errorf("asked %+v; want %+v", *asked, want)
	}
}

func TestDeployNoAnswerSendsNothingAndSaysHow(t *testing.T) {
	deploySetup(t, true)
	stub := uploadStubWithUnconnected(t, shopServices[:1])
	asked := answerDeployQuestions(t, stub, false)
	cliHome(t, stub.URL)

	out := runCLI(t, siteLessDir(t), "deploy")

	if len(*asked) != 1 {
		t.Fatalf("asked %v; want the one question", *asked)
	}
	if !stub.uploaded() {
		t.Fatalf("requests = %v; a No must not stop the deploy\n%s", stub.seen(), out)
	}
	if got := stub.connectField(); got != "" {
		t.Errorf("a No must send nothing, sent %q", got)
	}
	if !strings.Contains(out, "     ghayma connect database shop-db --site main\n") {
		t.Errorf("missing the hint for the declined service:\n%s", out)
	}
	if strings.Contains(out, "🔗 Connected") || strings.Contains(out, "couldn't read the connection result") {
		t.Errorf("nothing was chosen, so there is no outcome to report:\n%s", out)
	}
}

// Every service gets its own question, Yes first, in the listing's order; only
// the Yes answers ride on the upload and the rest are listed with the command
// that connects them.
func TestDeployAsksEachServiceInTurn(t *testing.T) {
	deploySetup(t, true)
	stub := uploadStubWithUnconnected(t, shopServices)
	asked := answerDeployQuestions(t, stub, true, false, true)
	cliHome(t, stub.URL)

	out := runCLI(t, siteLessDir(t), "deploy")

	want := []string{
		"Database 'shop-db' isn't connected to any site. Connect it to 'main'?",
		"Bucket 'media' isn't connected to any site. Connect it to 'main'?",
		"Auth app 'shop' isn't connected to any site. Connect it to 'main'?",
	}
	if !reflect.DeepEqual(*asked, want) {
		t.Errorf("asked %q; want %q", *asked, want)
	}
	if got := stub.connectField(); got != `[{"kind":"database","resource_id":"d1"},{"kind":"auth_app","resource_id":"a1"}]` {
		t.Errorf("connect_resources = %s; want the two services answered Yes", got)
	}
	notice := "ℹ️  Not connected to any site: bucket 'media'.\n" +
		"   Connect with:\n" +
		"     ghayma connect bucket media --site main\n"
	if !strings.Contains(out, notice) {
		t.Errorf("missing the notice %q:\n%s", notice, out)
	}
	for _, line := range []string{"🔗 Connected database 'shop-db' to 'main'.\n", "🔗 Connected auth app 'shop' to 'main'.\n"} {
		if !strings.Contains(out, line) {
			t.Errorf("missing %q in:\n%s", line, out)
		}
	}
}

// The question follows the first-deploy notice — it names the site that notice
// announces — and every answer is in before the headline and the upload.
func TestDeployAsksBetweenTheNoticeAndTheHeadline(t *testing.T) {
	deploySetup(t, true)
	stub := uploadStubWithUnconnected(t, shopServices[:1])
	cliHome(t, stub.URL)
	orig := promptDeployConnectFn
	t.Cleanup(func() { promptDeployConnectFn = orig })
	promptDeployConnectFn = func(string) (bool, error) {
		if stub.uploaded() {
			t.Error("the question must come before the upload")
		}
		fmt.Println("<question>")
		return true, nil
	}

	out := runCLI(t, siteLessDir(t), "deploy")

	notice := strings.Index(out, "deploying creates the site 'main'")
	question := strings.Index(out, "<question>")
	headline := strings.Index(out, "🚀 Deploying")
	if notice < 0 || question < notice || headline < question {
		t.Errorf("want the notice, then the question, then the headline:\n%s", out)
	}
}

func TestDeployCancelledQuestionUploadsNothing(t *testing.T) {
	deploySetup(t, true)
	stub := uploadStubWithUnconnected(t, shopServices[:1])
	cliHome(t, stub.URL)
	orig := promptDeployConnectFn
	t.Cleanup(func() { promptDeployConnectFn = orig })
	promptDeployConnectFn = func(string) (bool, error) { return false, promptui.ErrInterrupt }

	out := runCLI(t, siteLessDir(t), "deploy")

	if stub.uploaded() {
		t.Errorf("requests = %v; a cancelled question must not upload", stub.seen())
	}
	if !strings.Contains(out, "❌ Cancelled") {
		t.Errorf("want the cancel line:\n%s", out)
	}
}

func TestDeployPrintsWhatTheUploadConnected(t *testing.T) {
	deploySetup(t, true)
	stub := (&deployAPI{
		sites:    `[]`,
		listBody: unconnectedBody(t, shopServices[:2]),
		uploadReply: `{"deployment_id":"dep-1","status":"queued","site_id":"s1","connections":{` +
			`"connected":[{"kind":"database","resource_id":"d1"}],` +
			`"failed":[{"kind":"bucket","resource_id":"b1","error":"the site's platform key could not be updated"}]}}`,
	}).start(t)
	answerDeployQuestions(t, stub, true, true)
	cliHome(t, stub.URL)

	out := runCLI(t, siteLessDir(t), "deploy")

	queued := strings.Index(out, "📦 Build queued")
	for _, line := range []string{
		"🔗 Connected database 'shop-db' to 'main'.\n",
		"⚠️  Couldn't connect bucket 'media': the site's platform key could not be updated\n",
	} {
		i := strings.Index(out, line)
		if i < 0 {
			t.Errorf("missing %q in:\n%s", line, out)
			continue
		}
		if queued < i {
			t.Errorf("%q must come with the upload's answer, before the build is waited on:\n%s", line, out)
		}
	}
	if strings.Contains(out, "couldn't read the connection result") {
		t.Errorf("an answer that accounts for the choice must not be doubted:\n%s", out)
	}
}

// A server that predates the field connects nothing and says nothing: the
// deploy says where to look rather than claiming either way.
func TestDeployOlderBackendSaysWhereToCheck(t *testing.T) {
	deploySetup(t, true)
	stub := (&deployAPI{
		sites:       `[]`,
		listBody:    unconnectedBody(t, shopServices[:1]),
		uploadReply: `{"deployment_id":"dep-1","status":"queued"}`,
	}).start(t)
	answerDeployQuestions(t, stub, true)
	cliHome(t, stub.URL)

	out := runCLI(t, siteLessDir(t), "deploy")

	if !strings.Contains(out, "⚠️  couldn't read the connection result — check with: ghayma connections\n") {
		t.Errorf("missing the line:\n%s", out)
	}
	if strings.Contains(out, "🔗 Connected") {
		t.Errorf("nothing was reported connected:\n%s", out)
	}
	if !strings.Contains(out, "✅ Deployed successfully!") {
		t.Errorf("the deploy must run to the end:\n%s", out)
	}
}

func TestPrintDeployConnections(t *testing.T) {
	const unreadable = "couldn't read the connection result — check with: ghayma connections"
	chosen := shopServices[:1]

	for _, tc := range []struct {
		name       string
		outcome    api.DeployConnections
		chosen     []api.Unconnected
		lines      []string
		unreadable bool
	}{
		{"older backend", api.DeployConnections{}, chosen, nil, true},
		{"only another service", api.DeployConnections{Connected: []api.ConnectionItem{{Kind: "database", ResourceID: "d9"}}}, chosen, []string{"🔗 Connected database 'd9' to 'main'."}, true},
		{"nothing chosen", api.DeployConnections{}, nil, nil, false},
		{"failed counts", api.DeployConnections{Failed: []api.DeployConnectFailure{{Kind: "database", ResourceID: "d1", Error: "x"}}}, chosen, []string{"⚠️  Couldn't connect database 'shop-db': x"}, false},
	} {
		out := captureStdout(t, func() { printDeployConnections(tc.outcome, tc.chosen, "main") })
		for _, line := range tc.lines {
			if !strings.Contains(out, line+"\n") {
				t.Errorf("%s: missing %q in:\n%s", tc.name, line, out)
			}
		}
		if got := strings.Contains(out, unreadable); got != tc.unreadable {
			t.Errorf("%s: unreadable line printed = %v; want %v:\n%s", tc.name, got, tc.unreadable, out)
		}
		if tc.chosen == nil && out != "" {
			t.Errorf("%s: nothing chosen prints nothing, got:\n%s", tc.name, out)
		}
	}
}

// The question, the hints and the outcome name the site the upload lands on,
// by its live slug. An upload naming no site lands where the server puts it:
// the default site, else main, else the only site, and main on a project with
// none. Several sites with neither, or a linked site since deleted, leave the
// server no site, so the deploy offers nothing.
func TestDeployTargetSite(t *testing.T) {
	var (
		main  = api.Site{ID: "s1", Name: "main", Slug: "main"}
		www   = api.Site{ID: "s2", Name: "www", Slug: "www"}
		admin = api.Site{ID: "s3", Name: "Admin Console", Slug: "admin"}
	)
	byDefault := func(s api.Site) api.Site { s.IsDefault = true; return s }
	siteLess := SiteContext{NoSite: true}
	for _, tc := range []struct {
		name   string
		ctx    SiteContext
		sites  []api.Site
		listed bool
		want   string
		ok     bool
	}{
		{"site-less project", siteLess, nil, true, "main", true},
		{"the default site", siteLess, []api.Site{main, byDefault(www)}, true, "www", true},
		{"main when none is default", siteLess, []api.Site{www, main}, true, "main", true},
		{"the only site", siteLess, []api.Site{www}, true, "www", true},
		{"several, none default or main", siteLess, []api.Site{www, admin}, true, "", false},
		{"list unreadable", siteLess, nil, false, "main", true},
		{"named main before main existed", SiteContext{Site: SiteEntry{SiteName: "main"}}, []api.Site{byDefault(www)}, true, "www", true},
		{"a display name is never the label", SiteContext{Site: SiteEntry{SiteName: "Admin Console"}}, nil, true, "main", true},
		{"linked site renamed since", SiteContext{Site: SiteEntry{SiteID: "s2", SiteSlug: "web"}}, []api.Site{main, www}, true, "www", true},
		{"linked site deleted since", SiteContext{Site: SiteEntry{SiteID: "s9", SiteSlug: "old"}}, []api.Site{main, www}, true, "", false},
		{"linked site, list unreadable", SiteContext{Site: SiteEntry{SiteID: "s2", SiteName: "Web Site", SiteSlug: "web"}}, nil, false, "web", true},
		{"linked by id alone, list unreadable", SiteContext{Site: SiteEntry{SiteID: "s2", SiteName: "Web Site"}}, nil, false, "s2", true},
	} {
		got, ok := deployTargetSite(&tc.ctx, tc.sites, tc.listed)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: %q, %v; want %q, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

// mainAndDefaultWWW is a project whose default site is not main.
const mainAndDefaultWWW = `[{"id":"s1","name":"main","slug":"main"},{"id":"s2","name":"www","slug":"www","is_default":true}]`

// A config naming no site uploads without a site_id, and the server deploys it
// to the project's default site, so that is the site asked about and hinted.
func TestDeployLegacyConfigNamesTheDefaultSite(t *testing.T) {
	deploySetup(t, true)
	stub := (&deployAPI{sites: mainAndDefaultWWW, listBody: unconnectedBody(t, shopServices[:1])}).start(t)
	asked := answerDeployQuestions(t, stub, false)
	cliHome(t, stub.URL)

	out := runCLI(t, legacyDir(t), "deploy")

	if want := []string{"Database 'shop-db' isn't connected to any site. Connect it to 'www'?"}; !reflect.DeepEqual(*asked, want) {
		t.Errorf("asked %q; want %q", *asked, want)
	}
	if !strings.Contains(out, "     ghayma connect database shop-db --site www\n") {
		t.Errorf("the hint must name the default site:\n%s", out)
	}
}

func TestDeployLegacyConfigNamesTheOnlySite(t *testing.T) {
	deploySetup(t, false)
	noDeployQuestions(t)
	stub := (&deployAPI{sites: `[{"id":"s2","name":"www","slug":"www"}]`, listBody: unconnectedBody(t, shopServices[:1])}).start(t)
	cliHome(t, stub.URL)

	out := runCLI(t, legacyDir(t), "deploy")

	if !strings.Contains(out, "     ghayma connect database shop-db --site www\n") {
		t.Errorf("the hint must name the only site:\n%s", out)
	}
}

// Several sites, none default and none main: the server will not guess and
// refuses the upload. Nothing is listed, asked or said about services, and the
// upload goes out as it always did.
func TestDeployAmbiguousSitesOfferNothing(t *testing.T) {
	deploySetup(t, true)
	noDeployQuestions(t)
	stub := (&deployAPI{
		sites:    `[{"id":"s2","name":"www","slug":"www"},{"id":"s3","name":"admin","slug":"admin"}]`,
		listBody: unconnectedBody(t, shopServices[:1]),
	}).start(t)
	cliHome(t, stub.URL)

	out := runCLI(t, legacyDir(t), "deploy")

	if containsPath(stub.seen(), "GET /api/v1/projects/p1/connections/unconnected") {
		t.Errorf("requests = %v; nothing is offered, so nothing is listed", stub.seen())
	}
	if !stub.uploaded() {
		t.Fatalf("requests = %v; the upload must still go out\n%s", stub.seen(), out)
	}
	if got, site := stub.connectField(), stub.uploadSiteID(); got != "" || site != "" {
		t.Errorf("upload carried connect_resources %q, site_id %q; want neither", got, site)
	}
	for _, said := range []string{"Not connected to any site", "couldn't check for services", "ghayma connect", "Connected"} {
		if strings.Contains(out, said) {
			t.Errorf("nothing is said about services, got %q in:\n%s", said, out)
		}
	}
}

// A site list that cannot be read leaves main, as before.
func TestDeployUnreadableSiteListNamesMain(t *testing.T) {
	deploySetup(t, false)
	noDeployQuestions(t)
	stub := (&deployAPI{sitesStatus: http.StatusInternalServerError, sites: `{"error":"boom"}`, listBody: unconnectedBody(t, shopServices[:1])}).start(t)
	cliHome(t, stub.URL)

	out := runCLI(t, legacyDir(t), "deploy")

	if !strings.Contains(out, "     ghayma connect database shop-db --site main\n") {
		t.Errorf("the hint must fall back to main:\n%s", out)
	}
}

// The outcome names the site the server says the upload landed on.
func TestDeployOutcomeNamesTheSiteTheServerAnswered(t *testing.T) {
	deploySetup(t, true)
	stub := (&deployAPI{
		sites:    `[{"id":"s1","name":"main","slug":"main","is_default":true},{"id":"s2","name":"www","slug":"www"}]`,
		listBody: unconnectedBody(t, shopServices[:1]),
		uploadReply: `{"deployment_id":"dep-1","status":"queued","site_id":"s2","connections":{` +
			`"connected":[{"kind":"database","resource_id":"d1"}],"failed":[]}}`,
	}).start(t)
	answerDeployQuestions(t, stub, true)
	cliHome(t, stub.URL)

	out := runCLI(t, legacyDir(t), "deploy")

	if !strings.Contains(out, "🔗 Connected database 'shop-db' to 'www'.\n") {
		t.Errorf("the outcome must name the site the server answered with:\n%s", out)
	}
}

// A linked config keeps the slug its site had when it was written; the hint
// names the site's slug now.
func TestDeployLinkedConfigNamesTheLiveSlug(t *testing.T) {
	deploySetup(t, false)
	noDeployQuestions(t)
	stub := (&deployAPI{sites: `[{"id":"s1","name":"main","slug":"storefront","is_default":true}]`, listBody: unconnectedBody(t, shopServices[:1])}).start(t)
	cliHome(t, stub.URL)

	out := runCLI(t, linkedDir(t), "deploy")

	if !strings.Contains(out, "     ghayma connect database shop-db --site storefront\n") {
		t.Errorf("the hint must name the live slug, not the config's:\n%s", out)
	}
}

// A config naming no site deploys where --site points, so that is the site
// offered and named in the hint — not main.
func TestDeploySiteFlagOnSiteLessConfigNamesThatSite(t *testing.T) {
	deploySetup(t, false)
	noDeployQuestions(t)
	stub := (&deployAPI{sites: mainAndLab, listBody: unconnectedBody(t, shopServices[:1])}).start(t)
	cliHome(t, stub.URL)

	out := runCLI(t, legacyDir(t), "deploy", "--site", "tier-lab")

	if !strings.Contains(out, "     ghayma connect database shop-db --site tier-lab\n") {
		t.Errorf("the hint must name the site --site picked:\n%s", out)
	}
}

// A workspace deploy uploads the one site it picked, so that site is offered.
func TestDeployFromTheWorkspaceRootOffersThePickedSite(t *testing.T) {
	deploySetup(t, true)
	stub := (&deployAPI{
		sites:    `[{"id":"s1","name":"main","slug":"taarefni","is_default":true},{"id":"s2","name":"admin","slug":"taarefni-admin"}]`,
		listBody: unconnectedBody(t, shopServices[:1]),
	}).start(t)
	asked := answerDeployQuestions(t, stub, true)
	cliHome(t, stub.URL)

	runCLI(t, manifestFixture(t), "deploy", "--site", "taarefni-admin")

	if want := []string{"Database 'shop-db' isn't connected to any site. Connect it to 'taarefni-admin'?"}; !reflect.DeepEqual(*asked, want) {
		t.Errorf("asked %q; want %q", *asked, want)
	}
	if got := stub.connectField(); got != `[{"kind":"database","resource_id":"d1"}]` {
		t.Errorf("connect_resources = %q; want the database", got)
	}
}

// An image deploy carries no choice, so it never lists or asks.
func TestDeployImageNeverAsksAboutServices(t *testing.T) {
	deploySetup(t, true)
	noDeployQuestions(t)
	ts, seen, _ := imageDeployStub(t, oneSite, `{"id":"dep-1","status":"live","domains":["shop.ghayma.app"]}`)
	cliHome(t, ts.URL)

	runCLI(t, linkedDir(t), "deploy", "--image", "v1")

	for _, got := range *seen {
		if strings.Contains(got, "/connections/unconnected") {
			t.Errorf("requests = %v; an image deploy must not list unconnected services", *seen)
		}
	}
}
