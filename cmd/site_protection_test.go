package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

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
