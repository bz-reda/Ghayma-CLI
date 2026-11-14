package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"paas-cli/internal/api"
)

// protectionSites is a production default site and a development one.
var protectionSites = []api.Site{
	{ID: "s1", Name: "main", Slug: "main", Status: "live", IsDefault: true, Environment: "production"},
	{ID: "s2", Name: "dev", Slug: "dev", Status: "live", Environment: "development"},
}

const protectionPathS2 = "/api/v1/projects/p1/sites/s2/protection"

// protectionStub serves the site list and each "METHOD /path" in routes. An
// unmapped request gets the plain 404 an older backend sends. It records every
// request line and every decoded JSON body, in order.
func protectionStub(t *testing.T, sites []api.Site, routes map[string]stubReply) (*httptest.Server, *[]string, *[]map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	var bodies []map[string]any
	list, _ := json.Marshal(sites)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, key)
		if len(raw) > 0 {
			body := map[string]any{}
			_ = json.Unmarshal(raw, &body)
			bodies = append(bodies, body)
		}
		mu.Unlock()
		if key == "GET /api/v1/projects/p1/sites" {
			w.Header().Set("Content-Type", "application/json")
			w.Write(list)
			return
		}
		reply, ok := routes[key]
		if !ok {
			http.Error(w, "404 page not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.status)
		io.WriteString(w, reply.body)
	}))
	t.Cleanup(ts.Close)
	return ts, &seen, &bodies
}

// protectionViewBody renders a view body for the stub.
func protectionViewBody(available, enabled, effective bool, environment string, emails int) string {
	v := api.Protection{
		Available: available, Enabled: enabled, Effective: effective, Environment: environment,
		Emails: []api.ProtectionEmail{}, Tokens: []api.ProtectionToken{},
		Limits: api.ProtectionLimits{Emails: 50, Tokens: 5},
	}
	for i := 0; i < emails; i++ {
		v.Emails = append(v.Emails, api.ProtectionEmail{ID: "e1", Email: "ana@example.com"})
	}
	raw, _ := json.Marshal(v)
	return string(raw)
}

// answerProtection injects the confirmation answer for `off`.
func answerProtection(t *testing.T, answer string) *int {
	t.Helper()
	asked := 0
	old := protectionAsk
	protectionAsk = func() string { asked++; return answer }
	t.Cleanup(func() { protectionAsk = old })
	return &asked
}

func runProtection(t *testing.T, routes map[string]stubReply, args ...string) (string, *[]string, *[]map[string]any) {
	t.Helper()
	ts, seen, bodies := protectionStub(t, protectionSites, routes)
	cliHome(t, ts.URL)
	noPrompt(t)
	out := runCLI(t, linkedDir(t), append([]string{"site", "protection"}, args...)...)
	return out, seen, bodies
}

func countPrefix(seen []string, prefix string) int {
	n := 0
	for _, s := range seen {
		if strings.HasPrefix(s, prefix) {
			n++
		}
	}
	return n
}

func TestSiteProtection_ShowPrivate(t *testing.T) {
	out, seen, _ := runProtection(t, map[string]stubReply{
		"GET " + protectionPathS2: {200, protectionViewBody(true, true, true, "development", 1)},
	}, "dev")

	if !containsPath(*seen, "GET "+protectionPathS2) {
		t.Errorf("requests = %v; want the GET on the site's UUID", *seen)
	}
	for _, want := range []string{
		"🔒 'dev' is private — only project members and allowed emails can open it.",
		"   Allowed emails: 1 of 50   Bypass tokens: 0 of 5",
		"   Manage them: ghayma site visitors list dev · ghayma site bypass list dev",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q; want %q", out, want)
		}
	}
	if lastExitCode != 0 {
		t.Errorf("exit = %d; want 0", lastExitCode)
	}
}

func TestSiteProtection_ShowPublicDevSite(t *testing.T) {
	out, _, _ := runProtection(t, map[string]stubReply{
		"GET " + protectionPathS2: {200, protectionViewBody(true, false, false, "development", 0)},
	}, "dev")

	for _, want := range []string{
		"🌐 'dev' is public — anyone with the URL can open it.",
		"   Make it private: ghayma site protection dev on",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q; want %q", out, want)
		}
	}
}

func TestSiteProtection_ShowProductionSite(t *testing.T) {
	out, _, _ := runProtection(t, map[string]stubReply{
		"GET /api/v1/projects/p1/sites/s1/protection": {200, protectionViewBody(true, true, false, "production", 0)},
	}, "main")

	for _, want := range []string{
		"🌐 'main' is a production site, so it is public.",
		"   Protection applies when the site is staging or development.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q; want %q", out, want)
		}
	}
}

func TestSiteProtection_ShowUnavailable(t *testing.T) {
	out, _, _ := runProtection(t, map[string]stubReply{
		"GET " + protectionPathS2: {200, protectionViewBody(false, false, false, "development", 0)},
	}, "dev")

	if !strings.Contains(out, "ℹ️  Site protection is not switched on for this platform yet.") {
		t.Errorf("output = %q; want the unavailable line", out)
	}
	if strings.Contains(out, "🌐") || strings.Contains(out, "🔒") {
		t.Errorf("output = %q; an unavailable platform must not claim a state", out)
	}
}

func TestSiteProtection_ShowJSON(t *testing.T) {
	out, _, _ := runProtection(t, map[string]stubReply{
		"GET " + protectionPathS2: {200, protectionViewBody(true, true, true, "development", 1)},
	}, "dev", "--json")

	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("output = %q; want only JSON: %v", out, err)
	}
	if v["enabled"] != true {
		t.Errorf("json = %v; want enabled true", v)
	}
}

func TestSiteProtection_OnSendsThePut(t *testing.T) {
	out, seen, bodies := runProtection(t, map[string]stubReply{
		"PUT " + protectionPathS2: {200, protectionViewBody(true, true, true, "development", 0)},
	}, "dev", "on")

	if !containsPath(*seen, "PUT "+protectionPathS2) {
		t.Fatalf("requests = %v; want the PUT", *seen)
	}
	if len(*bodies) != 1 || (*bodies)[0]["enabled"] != true {
		t.Errorf("bodies = %v; want enabled:true", *bodies)
	}
	for _, want := range []string{
		"✅ 'dev' is now private.",
		"   Project members can open it. Allow someone else: ghayma site visitors add dev <email>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q; want %q", out, want)
		}
	}

	// With emails already allowed, the hint to add one is not needed.
	out, _, _ = runProtection(t, map[string]stubReply{
		"PUT " + protectionPathS2: {200, protectionViewBody(true, true, true, "development", 1)},
	}, "dev", "on")
	if strings.Contains(out, "Allow someone else") {
		t.Errorf("output = %q; no hint when the view has emails", out)
	}
}

func TestSiteProtection_OnProductionExplains(t *testing.T) {
	out, _, _ := runProtection(t, map[string]stubReply{
		"PUT /api/v1/projects/p1/sites/s1/protection": {200, protectionViewBody(true, true, false, "production", 0)},
	}, "main", "on")

	want := "ℹ️  Saved. 'main' is a production site, so it stays public; protection applies when it is staging or development."
	if !strings.Contains(out, want) {
		t.Errorf("output = %q; want %q", out, want)
	}
	if strings.Contains(out, "now private") || lastExitCode != 0 {
		t.Errorf("output = %q, exit %d; a production site is not private", out, lastExitCode)
	}
}

func TestSiteProtection_OnRouteApplyFailedIsAWarningAndExit1(t *testing.T) {
	out, _, _ := runProtection(t, map[string]stubReply{
		"PUT " + protectionPathS2: {502, `{"error":"route apply failed","code":"route_apply_failed"}`},
	}, "dev", "on")

	want := "⚠️  Saved, but the route is not updated yet. It is retried automatically within a few minutes; run the command again to retry now."
	if !strings.Contains(out, want) {
		t.Errorf("output = %q; want %q", out, want)
	}
	if strings.Contains(out, "❌") {
		t.Errorf("output = %q; a saved setting is a warning, not an error", out)
	}
	if lastExitCode != 1 {
		t.Errorf("exit = %d; want 1", lastExitCode)
	}
}

func TestSiteProtection_OffAsksFirst(t *testing.T) {
	routes := map[string]stubReply{
		"PUT " + protectionPathS2: {200, protectionViewBody(true, false, false, "development", 0)},
	}
	asked := answerProtection(t, "n")
	out, seen, _ := runProtection(t, routes, "dev", "off")

	if !strings.Contains(out, "⚠️  This makes 'dev' public: anyone with the URL can open it. Continue? [y/N] ") {
		t.Errorf("output = %q; want the prompt", out)
	}
	if *asked != 1 || countPrefix(*seen, "PUT") != 0 {
		t.Errorf("asked %d, requests %v; a decline must send nothing", *asked, *seen)
	}
	if !strings.Contains(out, "❌ Cancelled.") || lastExitCode != 1 {
		t.Errorf("output = %q, exit %d; want Cancelled and exit 1", out, lastExitCode)
	}

	answerProtection(t, "y")
	out, seen, bodies := runProtection(t, routes, "dev", "off")
	if !containsPath(*seen, "PUT "+protectionPathS2) || len(*bodies) != 1 || (*bodies)[0]["enabled"] != false {
		t.Errorf("requests %v, bodies %v; want the PUT with enabled:false", *seen, *bodies)
	}
	if !strings.Contains(out, "✅ 'dev' is now public.") || lastExitCode != 0 {
		t.Errorf("output = %q, exit %d; want the public line", out, lastExitCode)
	}
}

func TestSiteProtection_OffWithYesSkipsThePrompt(t *testing.T) {
	asked := answerProtection(t, "n")
	out, seen, _ := runProtection(t, map[string]stubReply{
		"PUT " + protectionPathS2: {200, protectionViewBody(true, false, false, "development", 0)},
	}, "dev", "off", "--yes")

	if *asked != 0 || strings.Contains(out, "Continue?") {
		t.Errorf("asked %d, output %q; --yes must not prompt", *asked, out)
	}
	if !containsPath(*seen, "PUT "+protectionPathS2) || !strings.Contains(out, "✅ 'dev' is now public.") {
		t.Errorf("requests %v, output %q; want the PUT and the public line", *seen, out)
	}
}

func TestSiteProtection_OlderBackend(t *testing.T) {
	out, _, _ := runProtection(t, nil, "dev")

	want := "❌ This platform does not serve site protection yet — the update that adds it is not deployed."
	if !strings.Contains(out, want) {
		t.Errorf("output = %q; want %q", out, want)
	}
	if lastExitCode != 1 {
		t.Errorf("exit = %d; want 1", lastExitCode)
	}

	out, _, _ = runProtection(t, nil, "dev", "on")
	if !strings.Contains(out, want) || lastExitCode != 1 {
		t.Errorf("on against an older backend = %q, exit %d", out, lastExitCode)
	}
}

func TestSiteProtection_ForbiddenNamesTheRole(t *testing.T) {
	out, _, _ := runProtection(t, map[string]stubReply{
		"PUT " + protectionPathS2: {403, `{"error":"forbidden","code":"forbidden"}`},
	}, "dev", "on")

	if !strings.Contains(out, "❌ You need the write role on 'dev' to change its protection.") || lastExitCode != 1 {
		t.Errorf("output = %q, exit %d; want the role sentence", out, lastExitCode)
	}
}

func TestSiteProtection_UnknownSiteListsTheAvailableOnes(t *testing.T) {
	out, seen, _ := runProtection(t, nil, "nope", "on")

	if !strings.Contains(out, `site "nope" not found`) || !strings.Contains(out, "main") || !strings.Contains(out, "dev") {
		t.Errorf("output = %q; want resolveNamedSite's message with the available sites", out)
	}
	for _, s := range *seen {
		if strings.HasSuffix(s, "/protection") {
			t.Errorf("requests = %v; an unknown site must not reach the protection route", *seen)
		}
	}
	if lastExitCode != 1 {
		t.Errorf("exit = %d; want 1", lastExitCode)
	}
}

func TestSiteProtection_BadValue(t *testing.T) {
	out, seen, _ := runProtection(t, nil, "dev", "maybe")

	if !strings.Contains(out, `❌ expected 'on' or 'off', got "maybe"`) || lastExitCode != 1 {
		t.Errorf("output = %q, exit %d; want parseOnOff's error", out, lastExitCode)
	}
	if len(*seen) != 0 {
		t.Errorf("requests = %v; a bad value must send nothing", *seen)
	}
}

func TestProtectionFailure(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{&api.APIError{Status: 409, Code: api.CodeProtectionUnavailable, Message: "x"}, "Site protection is not switched on for this platform yet."},
		{&api.APIError{Status: 403, Message: "x"}, "You need the write role on 'dev' to change its protection."},
		{&api.APIError{Status: 404, Message: "404 page not found"}, "This platform does not serve site protection yet — the update that adds it is not deployed."},
		{&api.APIError{Status: 404, Code: api.CodeNotFound, Message: "x"}, "That site, email or token was not found."},
		{&api.APIError{Status: 422, Code: api.CodeInvalidEmail, Message: "bad email"}, "bad email"},
	}
	for _, tc := range cases {
		if got := protectionFailure(tc.err, "dev"); got != tc.want {
			t.Errorf("protectionFailure(%v) = %q; want %q", tc.err, got, tc.want)
		}
	}
}

// visitorsViewBody renders a view with ana (an account) and bo (none yet).
func visitorsViewBody(effective bool, emails ...api.ProtectionEmail) string {
	v := api.Protection{
		Available: true, Enabled: effective, Effective: effective, Environment: "development",
		Emails: emails, Tokens: []api.ProtectionToken{},
		Limits: api.ProtectionLimits{Emails: 50, Tokens: 5},
	}
	if v.Emails == nil {
		v.Emails = []api.ProtectionEmail{}
	}
	raw, _ := json.Marshal(v)
	return string(raw)
}

var (
	visitorAna = api.ProtectionEmail{ID: "e1", Email: "ana@example.com", HasAccount: true, CreatedAt: time.Date(2026, 11, 14, 10, 21, 33, 0, time.UTC)}
	visitorBo  = api.ProtectionEmail{ID: "e2", Email: "bo@example.com", HasAccount: false, CreatedAt: time.Date(2026, 11, 13, 23, 59, 0, 0, time.UTC)}
)

const visitorsEmailsPath = protectionPathS2 + "/emails"

func runVisitors(t *testing.T, routes map[string]stubReply, args ...string) (string, *[]string, *[]map[string]any) {
	t.Helper()
	ts, seen, bodies := protectionStub(t, protectionSites, routes)
	cliHome(t, ts.URL)
	noPrompt(t)
	out := runCLI(t, linkedDir(t), append([]string{"site", "visitors"}, args...)...)
	return out, seen, bodies
}

func TestSiteVisitors_ListTable(t *testing.T) {
	out, seen, _ := runVisitors(t, map[string]stubReply{
		"GET " + protectionPathS2: {200, visitorsViewBody(true, visitorAna, visitorBo)},
	}, "list", "dev")

	if !containsPath(*seen, "GET "+protectionPathS2) {
		t.Errorf("requests = %v; want the GET", *seen)
	}
	for _, want := range []string{
		"Project members can always open the site; they do not need to be listed.",
		"EMAIL            ACCOUNT         ADDED",
		"ana@example.com  yes             2026-11-14",
		"bo@example.com   no account yet  2026-11-13",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q; want %q", out, want)
		}
	}
	if strings.Contains(out, "not private right now") || lastExitCode != 0 {
		t.Errorf("output = %q, exit %d; a private site needs no note", out, lastExitCode)
	}
}

func TestSiteVisitors_ListEmpty(t *testing.T) {
	out, _, _ := runVisitors(t, map[string]stubReply{
		"GET " + protectionPathS2: {200, visitorsViewBody(true)},
	}, "list", "dev")

	for _, want := range []string{
		"ℹ️  No extra emails are allowed on 'dev'.",
		"   Allow one: ghayma site visitors add dev <email>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q; want %q", out, want)
		}
	}
	if strings.Contains(out, "EMAIL") || lastExitCode != 0 {
		t.Errorf("output = %q, exit %d; no table for an empty list", out, lastExitCode)
	}
}

func TestSiteVisitors_ListJSON(t *testing.T) {
	out, _, _ := runVisitors(t, map[string]stubReply{
		"GET " + protectionPathS2: {200, visitorsViewBody(false, visitorAna)},
	}, "list", "dev", "--json")

	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output = %q; want only a JSON array: %v", out, err)
	}
	if len(got) != 1 || got[0]["email"] != "ana@example.com" {
		t.Errorf("json = %v; want ana", got)
	}

	out, _, _ = runVisitors(t, map[string]stubReply{
		"GET " + protectionPathS2: {200, visitorsViewBody(true)},
	}, "list", "dev", "--json")
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("output = %q; an empty list prints []", out)
	}
}

func TestSiteVisitors_AddPostsTheEmail(t *testing.T) {
	out, seen, bodies := runVisitors(t, map[string]stubReply{
		"POST " + visitorsEmailsPath: {201, visitorsViewBody(true, visitorAna)},
	}, "add", "dev", "ana@example.com")

	if !containsPath(*seen, "POST "+visitorsEmailsPath) {
		t.Fatalf("requests = %v; want the POST", *seen)
	}
	if len(*bodies) != 1 || (*bodies)[0]["email"] != "ana@example.com" {
		t.Errorf("bodies = %v; want email ana@example.com", *bodies)
	}
	if !strings.Contains(out, "✅ ana@example.com can now open 'dev'.") || lastExitCode != 0 {
		t.Errorf("output = %q, exit %d; want the success line", out, lastExitCode)
	}
	if strings.Contains(out, "Ghayma account") || strings.Contains(out, "not private") {
		t.Errorf("output = %q; no hint for an account on a private site", out)
	}
}

func TestSiteVisitors_AddNoAccountHint(t *testing.T) {
	out, _, bodies := runVisitors(t, map[string]stubReply{
		"POST " + visitorsEmailsPath: {201, visitorsViewBody(true, visitorAna, visitorBo)},
	}, "add", "dev", " Bo@Example.com ")

	if len(*bodies) != 1 || (*bodies)[0]["email"] != "Bo@Example.com" {
		t.Errorf("bodies = %v; want the trimmed email", *bodies)
	}

	for _, want := range []string{
		"✅ bo@example.com can now open 'dev'.",
		"   They need a Ghayma account with this email before they can sign in.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q; want %q", out, want)
		}
	}
}

func TestSiteVisitors_AddAlreadyThereIsNotAnError(t *testing.T) {
	out, _, _ := runVisitors(t, map[string]stubReply{
		"POST " + visitorsEmailsPath: {409, `{"error":"exists","code":"email_exists"}`},
	}, "add", "dev", "ana@example.com")

	if !strings.Contains(out, "ℹ️  ana@example.com is already allowed on 'dev'.") {
		t.Errorf("output = %q; want the already-allowed line", out)
	}
	if strings.Contains(out, "❌") || lastExitCode != 0 {
		t.Errorf("output = %q, exit %d; want exit 0 and no error", out, lastExitCode)
	}
}

func TestSiteVisitors_AddInvalidEmail(t *testing.T) {
	out, _, _ := runVisitors(t, map[string]stubReply{
		"POST " + visitorsEmailsPath: {422, `{"error":"invalid email","code":"invalid_email"}`},
	}, "add", "dev", "not-an-email")

	if !strings.Contains(out, "❌ That is not a valid email address.") || lastExitCode != 1 {
		t.Errorf("output = %q, exit %d; want the invalid line and exit 1", out, lastExitCode)
	}
}

func TestSiteVisitors_AddAtTheLimit(t *testing.T) {
	out, _, _ := runVisitors(t, map[string]stubReply{
		"POST " + visitorsEmailsPath: {409, `{"error":"limit","code":"email_limit_reached"}`},
		"GET " + protectionPathS2:    {200, visitorsViewBody(true, visitorAna)},
	}, "add", "dev", "cy@example.com")

	if !strings.Contains(out, "❌ 'dev' already has the maximum of 50 allowed emails. Remove one first.") || lastExitCode != 1 {
		t.Errorf("output = %q, exit %d; want the limit line naming 50", out, lastExitCode)
	}

	// Without the view the message drops the number.
	out, _, _ = runVisitors(t, map[string]stubReply{
		"POST " + visitorsEmailsPath: {409, `{"error":"limit","code":"email_limit_reached"}`},
	}, "add", "dev", "cy@example.com")
	if !strings.Contains(out, "❌ 'dev' already has the maximum number of allowed emails. Remove one first.") || lastExitCode != 1 {
		t.Errorf("output = %q, exit %d; want the limit line without a number", out, lastExitCode)
	}
}

func TestSiteVisitors_RemoveResolvesTheID(t *testing.T) {
	out, seen, _ := runVisitors(t, map[string]stubReply{
		"GET " + protectionPathS2:              {200, visitorsViewBody(true, visitorAna, visitorBo)},
		"DELETE " + visitorsEmailsPath + "/e1": {200, visitorsViewBody(true, visitorBo)},
	}, "remove", "dev", " Ana@Example.com ")

	if countPrefix(*seen, "DELETE") != 1 || !containsPath(*seen, "DELETE "+visitorsEmailsPath+"/e1") {
		t.Fatalf("requests = %v; want exactly one DELETE, by ID", *seen)
	}
	for _, want := range []string{
		"✅ ana@example.com can no longer open 'dev'.",
		"   It takes effect within a minute.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q; want %q", out, want)
		}
	}
	if lastExitCode != 0 {
		t.Errorf("exit = %d; want 0", lastExitCode)
	}
}

func TestSiteVisitors_RemoveUnknownEmail(t *testing.T) {
	out, seen, _ := runVisitors(t, map[string]stubReply{
		"GET " + protectionPathS2: {200, visitorsViewBody(true, visitorAna)},
	}, "remove", "dev", "cy@example.com")

	if countPrefix(*seen, "DELETE") != 0 {
		t.Errorf("requests = %v; an unknown email must send no DELETE", *seen)
	}
	if !strings.Contains(out, "❌ cy@example.com is not on the list for 'dev'.") || lastExitCode != 1 {
		t.Errorf("output = %q, exit %d; want the not-on-list line", out, lastExitCode)
	}
}

func TestSiteVisitors_EmailsAreSanitisedForDisplay(t *testing.T) {
	spoofed := api.ProtectionEmail{ID: "e3", Email: "eve\x1b[2K@example.com", CreatedAt: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	out, _, _ := runVisitors(t, map[string]stubReply{
		"GET " + protectionPathS2: {200, visitorsViewBody(true, visitorAna, spoofed)},
	}, "list", "dev")

	if strings.Contains(out, "\x1b") || !strings.Contains(out, "eve [2K@example.com") {
		t.Errorf("output = %q; want the email without the ESC byte", out)
	}
}

func TestSiteVisitors_NotPrivateNote(t *testing.T) {
	note := "   Note: 'dev' is not private right now (ghayma site protection dev)."
	out, _, _ := runVisitors(t, map[string]stubReply{
		"GET " + protectionPathS2: {200, visitorsViewBody(false, visitorAna)},
	}, "list", "dev")
	if !strings.Contains(out, "ana@example.com") || !strings.HasSuffix(strings.TrimRight(out, "\n"), note) {
		t.Errorf("list output = %q; want the table ending with %q", out, note)
	}

	out, _, _ = runVisitors(t, map[string]stubReply{
		"POST " + visitorsEmailsPath: {201, visitorsViewBody(false, visitorAna)},
	}, "add", "dev", "ana@example.com")
	if !strings.Contains(out, "✅ ana@example.com can now open 'dev'.") || !strings.HasSuffix(strings.TrimRight(out, "\n"), note) {
		t.Errorf("add output = %q; want the success line ending with %q", out, note)
	}
}

func TestSiteVisitors_Unavailable(t *testing.T) {
	v := visitorsViewBody(true, visitorAna)
	v = strings.Replace(v, `"available":true`, `"available":false`, 1)
	out, _, _ := runVisitors(t, map[string]stubReply{"GET " + protectionPathS2: {200, v}}, "list", "dev")
	if strings.TrimSpace(out) != "ℹ️  Site protection is not switched on for this platform yet." || lastExitCode != 0 {
		t.Errorf("output = %q, exit %d; want only the unavailable line", out, lastExitCode)
	}
}

func TestSiteVisitors_ListJSONWhenUnavailable(t *testing.T) {
	v := visitorsViewBody(true, visitorAna)
	v = strings.Replace(v, `"available":true`, `"available":false`, 1)
	out, _, _ := runVisitors(t, map[string]stubReply{"GET " + protectionPathS2: {200, v}}, "list", "dev", "--json")

	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output = %q; want only a JSON array: %v", out, err)
	}
	if len(got) != 1 || lastExitCode != 0 {
		t.Errorf("json = %v, exit %d; want the array and exit 0", got, lastExitCode)
	}
}

func TestSiteVisitors_OlderBackend(t *testing.T) {
	want := "❌ This platform does not serve site protection yet — the update that adds it is not deployed."
	for _, args := range [][]string{
		{"list", "dev"},
		{"add", "dev", "ana@example.com"},
		{"remove", "dev", "ana@example.com"},
	} {
		out, _, _ := runVisitors(t, nil, args...)
		if !strings.Contains(out, want) || lastExitCode != 1 {
			t.Errorf("%v against an older backend = %q, exit %d", args, out, lastExitCode)
		}
	}
}

var (
	bypassCI1 = api.ProtectionToken{ID: "t1", Name: "CI", Hint: "gpb_…Xy3Q", CreatedAt: time.Date(2026, 11, 14, 10, 22, 1, 0, time.UTC)}
	bypassCI2 = api.ProtectionToken{ID: "t2", Name: "CI", Hint: "gpb_…Ab7Z", CreatedAt: time.Date(2026, 11, 12, 23, 30, 0, 0, time.UTC)}
	bypassApp = api.ProtectionToken{ID: "t3", Name: "mobile app", Hint: "gpb_…Qq9W", CreatedAt: time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)}
)

const (
	bypassTokensPath = protectionPathS2 + "/tokens"
	bypassSecret     = "gpb_0123456789abcdefghijklmnopqrstuvwxyzABCDEFG"
)

// bypassViewBody renders a view of the dev site holding the given tokens.
func bypassViewBody(available, effective bool, tokens ...api.ProtectionToken) string {
	v := api.Protection{
		Available: available, Enabled: effective, Effective: effective, Environment: "development",
		Emails: []api.ProtectionEmail{}, Tokens: tokens,
		Limits: api.ProtectionLimits{Emails: 50, Tokens: 5},
	}
	if v.Tokens == nil {
		v.Tokens = []api.ProtectionToken{}
	}
	raw, _ := json.Marshal(v)
	return string(raw)
}

func runBypass(t *testing.T, routes map[string]stubReply, args ...string) (string, *[]string, *[]map[string]any) {
	t.Helper()
	ts, seen, bodies := protectionStub(t, protectionSites, routes)
	cliHome(t, ts.URL)
	noPrompt(t)
	out := runCLI(t, linkedDir(t), append([]string{"site", "bypass"}, args...)...)
	return out, seen, bodies
}

func TestSiteBypass_ListTable(t *testing.T) {
	out, seen, _ := runBypass(t, map[string]stubReply{
		"GET " + protectionPathS2: {200, bypassViewBody(true, true, bypassCI1, bypassApp)},
	}, "list", "dev")

	if !containsPath(*seen, "GET "+protectionPathS2) {
		t.Errorf("requests = %v; want the GET", *seen)
	}
	for _, want := range []string{
		"NAME        TOKEN      CREATED     ID",
		"CI          gpb_…Xy3Q  2026-11-14  t1",
		"mobile app  gpb_…Qq9W  2026-11-13  t3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q; want %q", out, want)
		}
	}
	if strings.Contains(out, "not private right now") || lastExitCode != 0 {
		t.Errorf("output = %q, exit %d; a private site needs no note", out, lastExitCode)
	}

	// A public site gets the note after the table.
	out, _, _ = runBypass(t, map[string]stubReply{
		"GET " + protectionPathS2: {200, bypassViewBody(true, false, bypassCI1)},
	}, "list", "dev")
	note := "   Note: 'dev' is not private right now (ghayma site protection dev)."
	if !strings.HasSuffix(strings.TrimRight(out, "\n"), note) {
		t.Errorf("output = %q; want it to end with %q", out, note)
	}
}

func TestSiteBypass_ListEmpty(t *testing.T) {
	out, _, _ := runBypass(t, map[string]stubReply{
		"GET " + protectionPathS2: {200, bypassViewBody(true, true)},
	}, "list", "dev")

	for _, want := range []string{
		"ℹ️  'dev' has no bypass tokens.",
		"   Create one for scripts, mobile apps or webhooks: ghayma site bypass create dev <name>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q; want %q", out, want)
		}
	}
	if strings.Contains(out, "NAME") || lastExitCode != 0 {
		t.Errorf("output = %q, exit %d; no table for an empty list", out, lastExitCode)
	}
}

func TestSiteBypass_ListJSON(t *testing.T) {
	out, _, _ := runBypass(t, map[string]stubReply{
		"GET " + protectionPathS2: {200, bypassViewBody(true, false, bypassCI1)},
	}, "list", "dev", "--json")

	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output = %q; want only a JSON array: %v", out, err)
	}
	if len(got) != 1 || got[0]["id"] != "t1" || got[0]["hint"] != "gpb_…Xy3Q" {
		t.Errorf("json = %v; want t1", got)
	}

	out, _, _ = runBypass(t, map[string]stubReply{
		"GET " + protectionPathS2: {200, bypassViewBody(false, false)},
	}, "list", "dev", "--json")
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("output = %q; an unavailable platform still prints []", out)
	}
}

func TestSiteBypass_ListUnavailable(t *testing.T) {
	out, _, _ := runBypass(t, map[string]stubReply{
		"GET " + protectionPathS2: {200, bypassViewBody(false, false, bypassCI1)},
	}, "list", "dev")
	if strings.TrimSpace(out) != "ℹ️  Site protection is not switched on for this platform yet." || lastExitCode != 0 {
		t.Errorf("output = %q, exit %d; want only the unavailable line", out, lastExitCode)
	}
}

func createdBody(view string) string {
	return `{"token":"` + bypassSecret + `","view":` + view + `}`
}

func TestSiteBypass_CreatePrintsTheSecretOnce(t *testing.T) {
	out, seen, bodies := runBypass(t, map[string]stubReply{
		"POST " + bypassTokensPath: {201, createdBody(bypassViewBody(true, true, bypassCI1))},
	}, "create", "dev", "CI")

	if !containsPath(*seen, "POST "+bypassTokensPath) {
		t.Fatalf("requests = %v; want the POST", *seen)
	}
	if len(*bodies) != 1 || len((*bodies)[0]) != 1 || (*bodies)[0]["name"] != "CI" {
		t.Errorf("bodies = %v; want {\"name\":\"CI\"}", *bodies)
	}
	want := strings.Join([]string{
		`✅ Created bypass token "CI" for 'dev'`,
		"Token (shown once): " + bypassSecret,
		"Save it now — it will not be shown again.",
		"   Send it as a header:   " + api.BypassHeader + ": " + bypassSecret,
		"   Or in a URL:           https://<your-site>/path?" + api.BypassQueryParam + "=" + bypassSecret,
		"   A token in a URL can end up in logs; prefer the header.",
	}, "\n")
	if !strings.Contains(out, want) {
		t.Errorf("output = %q; want %q", out, want)
	}
	if n := strings.Count(out, bypassSecret); n != 3 {
		t.Errorf("secret printed %d times; want the shown-once line plus the two usage lines", n)
	}
	if strings.Contains(out, "not private right now") || lastExitCode != 0 {
		t.Errorf("output = %q, exit %d; a private site needs no note", out, lastExitCode)
	}
}

func TestSiteBypass_CreateWithNullViewStillPrintsTheSecret(t *testing.T) {
	out, seen, _ := runBypass(t, map[string]stubReply{
		"POST " + bypassTokensPath: {201, createdBody("null")},
	}, "create", "dev", "CI")

	if !strings.Contains(out, "Token (shown once): "+bypassSecret) || !strings.Contains(out, "prefer the header.") {
		t.Errorf("output = %q; want the secret and the usage lines", out)
	}
	if countPrefix(*seen, "GET "+protectionPathS2) != 0 {
		t.Errorf("requests = %v; a null view must not trigger a GET", *seen)
	}
	if strings.Contains(out, "❌") || lastExitCode != 0 {
		t.Errorf("output = %q, exit %d; want success", out, lastExitCode)
	}
}

func TestSiteBypass_CreateNotPrivateNote(t *testing.T) {
	out, _, _ := runBypass(t, map[string]stubReply{
		"POST " + bypassTokensPath: {201, createdBody(bypassViewBody(true, false, bypassCI1))},
	}, "create", "dev", "CI")
	note := "   Note: 'dev' is not private right now (ghayma site protection dev)."
	if !strings.Contains(out, bypassSecret) || !strings.HasSuffix(strings.TrimRight(out, "\n"), note) {
		t.Errorf("output = %q; want the secret, then %q", out, note)
	}
}

func TestSiteBypass_CreateJSON(t *testing.T) {
	out, _, _ := runBypass(t, map[string]stubReply{
		"POST " + bypassTokensPath: {201, createdBody(bypassViewBody(true, false, bypassCI1))},
	}, "create", "dev", "CI", "--json")

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output = %q; want only JSON: %v", out, err)
	}
	if got["token"] != bypassSecret {
		t.Errorf("json = %v; want the token", got)
	}
	if view, ok := got["view"].(map[string]any); !ok || view["available"] != true {
		t.Errorf("json = %v; want the view", got)
	}
	for _, absent := range []string{"✅", "Token (shown once)", "Save it now", "Send it as a header", "Note:"} {
		if strings.Contains(out, absent) {
			t.Errorf("output = %q; --json must not print %q", out, absent)
		}
	}
	if strings.Count(out, bypassSecret) != 1 {
		t.Errorf("output = %q; the secret appears once in the JSON", out)
	}
}

func TestSiteBypass_CreateJSONWithNullView(t *testing.T) {
	out, _, _ := runBypass(t, map[string]stubReply{
		"POST " + bypassTokensPath: {201, createdBody("null")},
	}, "create", "dev", "CI", "--json")

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output = %q; want only JSON: %v", out, err)
	}
	if view, ok := got["view"]; !ok || view != nil || got["token"] != bypassSecret {
		t.Errorf("json = %v; want the token and a null view", got)
	}
}

func TestSiteBypass_CreateInvalidName(t *testing.T) {
	out, _, _ := runBypass(t, map[string]stubReply{
		"POST " + bypassTokensPath: {422, `{"error":"invalid name","code":"invalid_name"}`},
	}, "create", "dev", "x")

	if !strings.Contains(out, "❌ A token name must be 1 to 50 characters.") || lastExitCode != 1 {
		t.Errorf("output = %q, exit %d; want the name rule and exit 1", out, lastExitCode)
	}
}

func TestSiteBypass_CreateAtTheLimit(t *testing.T) {
	out, _, _ := runBypass(t, map[string]stubReply{
		"POST " + bypassTokensPath: {409, `{"error":"limit","code":"token_limit_reached"}`},
		"GET " + protectionPathS2:  {200, bypassViewBody(true, true, bypassCI1)},
	}, "create", "dev", "CI")

	want := "❌ 'dev' already has the maximum of 5 bypass tokens. Revoke one first: ghayma site bypass list dev"
	if !strings.Contains(out, want) || lastExitCode != 1 {
		t.Errorf("output = %q, exit %d; want %q", out, lastExitCode, want)
	}

	// Without the view the message drops the number.
	out, _, _ = runBypass(t, map[string]stubReply{
		"POST " + bypassTokensPath: {409, `{"error":"limit","code":"token_limit_reached"}`},
	}, "create", "dev", "CI")
	want = "❌ 'dev' already has the maximum number of bypass tokens. Revoke one first: ghayma site bypass list dev"
	if !strings.Contains(out, want) || lastExitCode != 1 {
		t.Errorf("output = %q, exit %d; want %q", out, lastExitCode, want)
	}
}

func TestSiteBypass_RevokeByName(t *testing.T) {
	asked := answerProtection(t, "y")
	out, seen, _ := runBypass(t, map[string]stubReply{
		"GET " + protectionPathS2:            {200, bypassViewBody(true, true, bypassCI1, bypassApp)},
		"DELETE " + bypassTokensPath + "/t1": {200, bypassViewBody(true, true, bypassApp)},
	}, "revoke", "dev", "CI")

	if !strings.Contains(out, `⚠️  This revokes "CI" (gpb_…Xy3Q). Anything using it will be refused by 'dev' within a minute. Continue? [y/N] `) || *asked != 1 {
		t.Errorf("output = %q, asked %d; want the prompt once", out, *asked)
	}
	if countPrefix(*seen, "DELETE") != 1 || !containsPath(*seen, "DELETE "+bypassTokensPath+"/t1") {
		t.Fatalf("requests = %v; want one DELETE of t1", *seen)
	}
	if !strings.Contains(out, `✅ Revoked bypass token "CI".`) || lastExitCode != 0 {
		t.Errorf("output = %q, exit %d; want the revoked line", out, lastExitCode)
	}
	if strings.Count(out, "gpb_") != 1 {
		t.Errorf("output = %q; revoke prints only the token's hint", out)
	}
}

func TestSiteBypass_RevokeAsksFirst(t *testing.T) {
	asked := answerProtection(t, "n")
	out, seen, _ := runBypass(t, map[string]stubReply{
		"GET " + protectionPathS2:            {200, bypassViewBody(true, true, bypassCI1)},
		"DELETE " + bypassTokensPath + "/t1": {200, bypassViewBody(true, true)},
	}, "revoke", "dev", "CI")

	if *asked != 1 || countPrefix(*seen, "DELETE") != 0 {
		t.Errorf("asked %d, requests %v; a decline must send no DELETE", *asked, *seen)
	}
	if !strings.Contains(out, "❌ Cancelled.") || lastExitCode != 1 {
		t.Errorf("output = %q, exit %d; want Cancelled and exit 1", out, lastExitCode)
	}
}

func TestSiteBypass_RevokeAmbiguousNameListsBoth(t *testing.T) {
	asked := answerProtection(t, "y")
	out, seen, _ := runBypass(t, map[string]stubReply{
		"GET " + protectionPathS2: {200, bypassViewBody(true, true, bypassCI1, bypassApp, bypassCI2)},
	}, "revoke", "dev", "CI")

	want := strings.Join([]string{
		`❌ 2 bypass tokens are named "CI". Pick one with --id:`,
		"   t1  gpb_…Xy3Q  created 2026-11-14",
		"   t2  gpb_…Ab7Z  created 2026-11-12",
	}, "\n")
	if !strings.Contains(out, want) {
		t.Errorf("output = %q; want %q", out, want)
	}
	if *asked != 0 || countPrefix(*seen, "DELETE") != 0 || lastExitCode != 1 {
		t.Errorf("asked %d, requests %v, exit %d; an ambiguous name must not prompt, send or succeed", *asked, *seen, lastExitCode)
	}
}

func TestSiteBypass_RevokeByID(t *testing.T) {
	asked := answerProtection(t, "n")
	out, seen, _ := runBypass(t, map[string]stubReply{
		"GET " + protectionPathS2:            {200, bypassViewBody(true, true, bypassCI1, bypassCI2)},
		"DELETE " + bypassTokensPath + "/t2": {200, bypassViewBody(true, true, bypassCI1)},
	}, "revoke", "dev", "--id", "t2", "--yes")

	if *asked != 0 || strings.Contains(out, "Continue?") {
		t.Errorf("asked %d, output %q; --yes must not prompt", *asked, out)
	}
	if countPrefix(*seen, "DELETE") != 1 || !containsPath(*seen, "DELETE "+bypassTokensPath+"/t2") {
		t.Fatalf("requests = %v; want one DELETE of t2", *seen)
	}
	if !strings.Contains(out, `✅ Revoked bypass token "CI".`) || lastExitCode != 0 {
		t.Errorf("output = %q, exit %d; want the revoked line", out, lastExitCode)
	}

	// An ID that is not on the site sends nothing.
	out, seen, _ = runBypass(t, map[string]stubReply{
		"GET " + protectionPathS2: {200, bypassViewBody(true, true, bypassCI1)},
	}, "revoke", "dev", "--id", "t9", "--yes")
	if !strings.Contains(out, `❌ No bypass token with ID "t9" on 'dev'.`) || lastExitCode != 1 || countPrefix(*seen, "DELETE") != 0 {
		t.Errorf("output = %q, exit %d, requests %v; want the unknown-ID line and no DELETE", out, lastExitCode, *seen)
	}
}

func TestSiteBypass_RevokeIDWithAMismatchedName(t *testing.T) {
	asked := answerProtection(t, "y")
	out, seen, _ := runBypass(t, map[string]stubReply{
		"GET " + protectionPathS2:            {200, bypassViewBody(true, true, bypassCI1, bypassApp)},
		"DELETE " + bypassTokensPath + "/t3": {200, bypassViewBody(true, true, bypassCI1)},
	}, "revoke", "dev", "CI", "--id", "t3")

	want := `❌ Token t3 is named "mobile app", not "CI". Drop the name or fix the ID.`
	if !strings.Contains(out, want) || lastExitCode != 1 {
		t.Errorf("output = %q, exit %d; want %q", out, lastExitCode, want)
	}
	if *asked != 0 || countPrefix(*seen, "DELETE") != 0 {
		t.Errorf("asked %d, requests %v; a mismatch must not prompt or send", *asked, *seen)
	}

	// The matching name with --id still revokes, after naming the token.
	asked = answerProtection(t, "y")
	out, seen, _ = runBypass(t, map[string]stubReply{
		"GET " + protectionPathS2:            {200, bypassViewBody(true, true, bypassCI1, bypassApp)},
		"DELETE " + bypassTokensPath + "/t3": {200, bypassViewBody(true, true, bypassCI1)},
	}, "revoke", "dev", "mobile app", "--id", "t3")
	if !strings.Contains(out, `⚠️  This revokes "mobile app" (gpb_…Qq9W).`) || *asked != 1 {
		t.Errorf("output = %q, asked %d; want the prompt naming the token", out, *asked)
	}
	if !containsPath(*seen, "DELETE "+bypassTokensPath+"/t3") || !strings.Contains(out, `✅ Revoked bypass token "mobile app".`) || lastExitCode != 0 {
		t.Errorf("output = %q, exit %d, requests %v; want t3 revoked", out, lastExitCode, *seen)
	}
}

func TestSiteBypass_NamesPrintAsTyped(t *testing.T) {
	name := "أداة CI\u200f"
	tok := api.ProtectionToken{ID: "t7", Name: name, Hint: "gpb_…Zz1A", CreatedAt: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}

	out, _, _ := runBypass(t, map[string]stubReply{
		"POST " + bypassTokensPath: {201, createdBody(bypassViewBody(true, true, tok))},
	}, "create", "dev", name)
	if !strings.Contains(out, "✅ Created bypass token \""+name+"\" for 'dev'") || strings.Contains(out, `\`) {
		t.Errorf("output = %q; want the name byte for byte", out)
	}

	out, _, _ = runBypass(t, map[string]stubReply{
		"GET " + protectionPathS2:            {200, bypassViewBody(true, true, tok)},
		"DELETE " + bypassTokensPath + "/t7": {200, bypassViewBody(true, true)},
	}, "revoke", "dev", name, "--yes")
	if !strings.Contains(out, "✅ Revoked bypass token \""+name+"\".") || strings.Contains(out, `\`) {
		t.Errorf("output = %q; want the name byte for byte", out)
	}
}

func TestDisplayText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"CI", "CI"},
		{"أداة CI", "أداة CI"},
		{"أداة CI\u200f", "أداة CI\u200f"},
		{"👩\u200d💻 app", "👩\u200d💻 app"},
		{"a\x1b[31mb", "a [31mb"},
		{"line1\nline2", "line1 line2"},
		{"admin\u202egnp.exe", "admingnp.exe"},
		{"x\u2066y\u2069", "xy"},
		{"a\u0085b\x7fc", "a b c"},
		{"  padded\t", "padded"},
		{"\x00", "(unnamed)"},
		{"", "(unnamed)"},
	}
	for _, tc := range cases {
		if got := displayText(tc.in); got != tc.want {
			t.Errorf("displayText(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
	got := displayText("ok\xff\xfe")
	if !strings.Contains(got, "\uFFFD") || !utf8.ValidString(got) {
		t.Errorf("displayText(invalid UTF-8) = %q; want U+FFFD and valid UTF-8", got)
	}
}

func TestSiteBypass_ControlCharactersNeverReachTheTerminal(t *testing.T) {
	evil := api.ProtectionToken{ID: "t8", Name: "CI\x1b[2K\u202eevil", Hint: "gpb_…Ev1L", CreatedAt: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	clean := func(label, out string) {
		t.Helper()
		if strings.Contains(out, "\x1b") || strings.Contains(out, "\u202e") {
			t.Errorf("%s output = %q; want no ESC byte and no U+202E", label, out)
		}
	}

	out, _, _ := runBypass(t, map[string]stubReply{
		"GET " + protectionPathS2: {200, bypassViewBody(true, true, evil)},
	}, "list", "dev")
	clean("list", out)
	if !strings.Contains(out, "CI [2Kevil") {
		t.Errorf("list output = %q; want the sanitised name in the table", out)
	}

	answerProtection(t, "y")
	out, seen, _ := runBypass(t, map[string]stubReply{
		"GET " + protectionPathS2:            {200, bypassViewBody(true, true, evil)},
		"DELETE " + bypassTokensPath + "/t8": {200, bypassViewBody(true, true)},
	}, "revoke", "dev", "--id", "t8")
	clean("revoke", out)
	if !strings.Contains(out, `⚠️  This revokes "CI [2Kevil" (gpb_…Ev1L).`) || !strings.Contains(out, `✅ Revoked bypass token "CI [2Kevil".`) {
		t.Errorf("revoke output = %q; want the prompt and the success line", out)
	}
	if !containsPath(*seen, "DELETE "+bypassTokensPath+"/t8") {
		t.Errorf("requests = %v; want t8 revoked", *seen)
	}
}

func TestSiteBypass_RevokeUnknownName(t *testing.T) {
	out, seen, _ := runBypass(t, map[string]stubReply{
		"GET " + protectionPathS2: {200, bypassViewBody(true, true, bypassCI1)},
	}, "revoke", "dev", "ci")

	if !strings.Contains(out, `❌ No bypass token named "ci" on 'dev'.`) || lastExitCode != 1 {
		t.Errorf("output = %q, exit %d; names match case-sensitively", out, lastExitCode)
	}
	if countPrefix(*seen, "DELETE") != 0 {
		t.Errorf("requests = %v; want no DELETE", *seen)
	}

	out, seen, _ = runBypass(t, map[string]stubReply{
		"GET " + protectionPathS2: {200, bypassViewBody(true, true, bypassCI1)},
	}, "revoke", "dev")
	if !strings.Contains(out, "❌ Give the token's name, or pick one with --id.") || lastExitCode != 1 || len(*seen) != 0 {
		t.Errorf("output = %q, exit %d, requests %v; want the missing-name line", out, lastExitCode, *seen)
	}
}

func TestSiteBypass_OlderBackend(t *testing.T) {
	want := "❌ This platform does not serve site protection yet — the update that adds it is not deployed."
	for _, args := range [][]string{
		{"list", "dev"},
		{"create", "dev", "CI"},
		{"revoke", "dev", "CI", "--yes"},
	} {
		out, _, _ := runBypass(t, nil, args...)
		if !strings.Contains(out, want) || lastExitCode != 1 {
			t.Errorf("%v against an older backend = %q, exit %d", args, out, lastExitCode)
		}
	}
}

const (
	devPrivateHint  = "🔒 Private — only project members and allowed emails can open it (ghayma site protection dev)."
	mainPrivateHint = "🔒 Private — only project members and allowed emails can open it (ghayma site protection main)."
	privateMainSite = `[{"id":"s1","name":"main","slug":"main","is_default":true,"protection_enabled":true,"protection_effective":true}]`
)

func TestPrivateHint(t *testing.T) {
	if got := privateHint("dev"); got != devPrivateHint {
		t.Errorf("privateHint = %q; want %q", got, devPrivateHint)
	}
}

func TestSiteList_MarksPrivateSites(t *testing.T) {
	sites := []api.Site{
		{ID: "s1", Name: "main", Slug: "main", Status: "live", IsDefault: true, Environment: "production", ProtectionEnabled: true},
		{ID: "s2", Name: "dev", Slug: "dev", Status: "live", Environment: "development", ProtectionEnabled: true, ProtectionEffective: true},
	}
	ts, _, _ := protectionStub(t, sites, nil)
	cliHome(t, ts.URL)
	noPrompt(t)
	out := runCLI(t, linkedDir(t), "site", "list")

	if !strings.Contains(out, "dev  (slug: dev, env: development, private, status: live, id: s2)") {
		t.Errorf("output = %q; want the dev row marked private", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "main  (slug: main") && strings.Contains(line, "private") {
			t.Errorf("main row = %q; a public site is not marked", line)
		}
	}

	// A production site whose setting is on but not in force reads as before.
	if got := siteListLine(sites[0], true); got != "  ▶ main  (slug: main, env: production, status: live, id: s1)" {
		t.Errorf("public row = %q; want it unchanged", got)
	}
}

func runSiteCreatePrivate(t *testing.T, created string, args ...string) string {
	t.Helper()
	ts, _, _ := protectionStub(t, protectionSites, map[string]stubReply{
		"POST /api/v1/projects/p1/sites": {201, created},
	})
	cliHome(t, ts.URL)
	noPrompt(t)
	return runCLI(t, linkedDir(t), append([]string{"site", "create"}, args...)...)
}

func TestSiteCreate_PrivateHint(t *testing.T) {
	out := runSiteCreatePrivate(t,
		`{"id":"s9","name":"staging","slug":"staging","environment":"staging","protection_enabled":true,"protection_effective":true}`,
		"staging", "--env", "staging")
	want := "✅ Site 'staging' created (slug: staging, id: s9)\n" +
		"   Environment: staging\n" +
		"   🔒 Private — only project members and allowed emails can open it (ghayma site protection staging).\n"
	if !strings.Contains(out, want) {
		t.Errorf("output = %q; want %q", out, want)
	}

	// Without an environment line the hint follows the created line.
	out = runSiteCreatePrivate(t, `{"id":"s9","name":"qa","slug":"qa","protection_effective":true}`, "qa")
	want = "✅ Site 'qa' created (slug: qa, id: s9)\n" +
		"   🔒 Private — only project members and allowed emails can open it (ghayma site protection qa).\n"
	if !strings.Contains(out, want) {
		t.Errorf("output = %q; want %q", out, want)
	}
}

func TestSiteCreate_NoHintWhenPublic(t *testing.T) {
	out := runSiteCreatePrivate(t,
		`{"id":"s9","name":"staging","slug":"staging","environment":"staging","protection_enabled":false,"protection_effective":false}`,
		"staging", "--env", "staging")
	if !strings.Contains(out, "   Environment: staging\n") || strings.Contains(out, "🔒") {
		t.Errorf("output = %q; want the environment line and no hint", out)
	}
}

func TestDeploy_PrivateHintAfterTheURLs(t *testing.T) {
	ts, _, _ := builderStub(t, `[{"id":"s1","name":"web","slug":"web","is_default":true,"protection_effective":true}]`)
	deployQuietly(t, ts.URL)

	out := runCLI(t, perAppWithBuilder(t, ""), "deploy")

	want := "🚀 Deploying shop......\n✅ Deployed successfully!\n🌐 Your app is live at:\n   https://shop.ghayma.app\n" +
		"🔒 Private — only project members and allowed emails can open it (ghayma site protection web).\n"
	if out != want {
		t.Errorf("output:\n got  %q\n want %q", out, want)
	}
}

func TestDeploy_NoHintForAPublicSite(t *testing.T) {
	ts, _, _ := builderStub(t, webSiteAuto)
	deployQuietly(t, ts.URL)

	out := runCLI(t, perAppWithBuilder(t, ""), "deploy")

	want := "🚀 Deploying shop......\n✅ Deployed successfully!\n🌐 Your app is live at:\n   https://shop.ghayma.app\n"
	if out != want {
		t.Errorf("output:\n got  %q\n want %q", out, want)
	}
}

// With no domains the hint follows the "live" line.
func TestDeployImage_PrivateHintWithoutDomains(t *testing.T) {
	ts, _, _ := imageDeployStub(t, privateMainSite, `{"id":"dep-1","status":"live"}`)
	cliHome(t, ts.URL)
	noPrompt(t)
	fastPolls(t)

	out := runCLI(t, linkedDir(t), "deploy", "--image", "v1")

	if !strings.HasSuffix(out, "✅ Deployed successfully!\n🌐 Your app is live\n"+mainPrivateHint+"\n") {
		t.Errorf("output = %q; want the hint after the live line", out)
	}
}

func TestDockerPushDeploy_PrivateHint(t *testing.T) {
	stubDocker(t, func(args []string) (string, error) {
		if args[0] == "push" {
			return pushOutput, nil
		}
		return "", nil
	})
	ts, _, _ := dockerStub(t, privateMainSite)
	cliHome(t, ts.URL)
	noPrompt(t)
	fastPolls(t)

	out := runCLI(t, linkedDir(t), "docker", "push", "my-app", "--tag", "v1", "--deploy")

	if !strings.HasSuffix(out, "   https://shop.ghayma.app\n"+mainPrivateHint+"\n") {
		t.Errorf("output = %q; want the hint after the URLs", out)
	}
}

func TestPromote_PrivateHint(t *testing.T) {
	ts, _, _ := promoteStub(t, privateMainSite, 0, promoteAccepted, `{"id":"dep-9","status":"live","domains":["shop.ghayma.app"]}`)
	cliHome(t, ts.URL)
	noPrompt(t)
	fastPolls(t)

	out := runCLI(t, linkedDir(t), "promote", "--from", "dev")

	if !strings.HasSuffix(out, "   https://shop.ghayma.app\n"+mainPrivateHint+"\n") {
		t.Errorf("output = %q; want the hint after the URLs", out)
	}
}
