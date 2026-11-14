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
	out, _, _ := runVisitors(t, map[string]stubReply{
		"POST " + visitorsEmailsPath: {201, visitorsViewBody(true, visitorAna, visitorBo)},
	}, "add", "dev", " Bo@Example.com ")

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

	if !containsPath(*seen, "DELETE "+visitorsEmailsPath+"/e1") {
		t.Fatalf("requests = %v; want the DELETE by ID", *seen)
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
