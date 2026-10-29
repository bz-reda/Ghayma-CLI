package cmd

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"paas-cli/internal/api"
	"paas-cli/internal/config"
)

type loginReply struct {
	status int
	body   string
	header string // Retry-After
	hang   bool   // answers nothing until the client gives up or the test ends
	drop   bool   // closes the connection without answering
}

func (r loginReply) write(w http.ResponseWriter, req *http.Request, release <-chan struct{}) {
	switch {
	case r.hang:
		select {
		case <-req.Context().Done():
		case <-release:
		}
	case r.drop:
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	default:
		if r.header != "" {
			w.Header().Set("Retry-After", r.header)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(r.status)
		io.WriteString(w, r.body)
	}
}

// loginBackend fakes the endpoints a login touches and records what it was asked.
type loginBackend struct {
	mu     sync.Mutex
	create loginReply
	polls  []loginReply // answered in order; the last repeats
	login  loginReply
	me     loginReply
	accept loginReply

	pollCalls   int
	meCalls     int
	acceptAuth  []string
	acceptBody  []string
	mints       int
	createCalls int
}

func newLoginBackend() *loginBackend {
	return &loginBackend{
		create: loginReply{status: 200, body: `{"code":"c0de","expires_in":300,"login_url":"https://dash.test/auth/cli?code=c0de"}`},
		polls:  []loginReply{{status: 200, body: `{"confirmed":true,"token":"jwt-1","email":"poll@b.dz"}`}},
		login:  loginReply{status: 200, body: `{"token":"jwt-1","user":{"id":"u1","email":"a@b.dz","name":"A"}}`},
		me:     loginReply{status: 200, body: `{"id":"u1","email":"a@b.dz","name":"A"}`},
		accept: loginReply{status: 200, body: `{"privacy_version":"1.0","privacy_accepted_at":"2026-10-29T10:00:00Z"}`},
	}
}

func (b *loginBackend) serve(t *testing.T) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		var reply loginReply
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/cli/session":
			b.createCalls++
			reply = b.create
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/auth/cli/session/c0de":
			reply = b.polls[min(b.pollCalls, len(b.polls)-1)]
			b.pollCalls++
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/login":
			reply = b.login
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/auth/me":
			b.meCalls++
			reply = b.me
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/privacy/accept":
			raw, _ := io.ReadAll(r.Body)
			b.acceptAuth = append(b.acceptAuth, r.Header.Get("Authorization"))
			b.acceptBody = append(b.acceptBody, string(raw))
			reply = b.accept
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/tokens":
			reply = loginReply{status: 200, body: `[]`}
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/tokens":
			b.mints++
			reply = loginReply{status: 201, body: `{"id":"tok-1","name":"ghayma-cli@test","token":"gh_minted","scope":"full"}`}
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			reply = loginReply{status: 404, body: `{"error":"not found"}`}
		}
		b.mu.Unlock()
		reply.write(w, r, release)
	}))
	// A fresh connection per request, so a dropped one is never replayed
	// onto the next scripted reply.
	ts.Config.SetKeepAlivesEnabled(false)
	ts.Start()
	t.Cleanup(ts.Close)
	// Runs first: a hung reply ends, so Close never waits on it.
	t.Cleanup(func() { close(release) })
	return ts
}

type fakeClock struct {
	at    time.Time
	slept []time.Duration
}

func (c *fakeClock) now() time.Time { return c.at }

func (c *fakeClock) sleep(d time.Duration) {
	c.slept = append(c.slept, d)
	c.at = c.at.Add(d)
}

type loginHarness struct {
	t       *testing.T
	backend *loginBackend
	cfgPath string
	cfg     *config.Config
	client  *api.Client
	clock   *fakeClock
	opened  []string
	asked   int
	answer  string
	flow    loginFlow
}

func newLoginHarness(t *testing.T, backend *loginBackend) *loginHarness {
	t.Helper()
	ts := backend.serve(t)
	h := &loginHarness{t: t, backend: backend, cfgPath: filepath.Join(t.TempDir(), "cli.json"), clock: &fakeClock{at: time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)}}
	t.Setenv("GHAYMA_CONFIG", h.cfgPath)
	t.Setenv("GHAYMA_API_HOST", "")
	forceStdin(t, true)
	h.cfg = config.Load()
	h.cfg.SetHost(ts.URL)
	h.client = api.NewClient(h.cfg)
	h.flow = loginFlow{
		interval:    5 * time.Second,
		wait:        5 * time.Minute,
		timeout:     2 * time.Second,
		now:         h.clock.now,
		sleep:       h.clock.sleep,
		openBrowser: func(u string) { h.opened = append(h.opened, u) },
		askPrivacy: func() (string, error) {
			h.asked++
			return h.answer, nil
		},
	}
	return h
}

func (h *loginHarness) browser() (string, error) {
	var err error
	out := captureStdout(h.t, func() { err = browserLogin(h.cfg, h.client, h.flow) })
	return out, err
}

func (h *loginHarness) email() (string, error) {
	var err error
	out := captureStdout(h.t, func() { err = emailLogin(h.cfg, h.client, "a@b.dz", "pw", h.flow) })
	return out, err
}

// saved is the config file as written, or nil when nothing was saved.
func (h *loginHarness) saved() *config.Config {
	h.t.Helper()
	raw, err := os.ReadFile(h.cfgPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		h.t.Fatal(err)
	}
	var c config.Config
	if err := json.Unmarshal(raw, &c); err != nil {
		h.t.Fatal(err)
	}
	return &c
}

func (h *loginHarness) assertNothingSaved() {
	h.t.Helper()
	if c := h.saved(); c != nil {
		h.t.Errorf("config was written: token set %v, api token set %v", c.Token != "", c.APIToken != "")
	}
	if h.backend.mints != 0 {
		h.t.Errorf("minted %d CLI tokens; want none", h.backend.mints)
	}
}

func (h *loginHarness) assertLoggedIn(email string) {
	h.t.Helper()
	c := h.saved()
	if c == nil {
		h.t.Fatal("nothing saved after a successful login")
	}
	if c.Token != "jwt-1" || c.APIToken != "gh_minted" || c.Email != email || c.UserID != "u1" {
		h.t.Errorf("saved email %q user %q, token match %v, api token match %v", c.Email, c.UserID, c.Token == "jwt-1", c.APIToken == "gh_minted")
	}
}

func TestBrowserLogin_CreateRefusedShowsServerMessage(t *testing.T) {
	cases := []struct {
		name  string
		reply loginReply
		want  []string
	}{
		{"per-address limit", loginReply{status: 429, header: "60", body: `{"error":"too many pending CLI logins from your network; finish one or wait a few minutes","retry_after":60}`},
			[]string{"too many pending CLI logins from your network", "try again in 1 minute"}},
		{"store full", loginReply{status: 503, header: "60", body: `{"error":"too many CLI logins in progress; try again in a few minutes","retry_after":60}`},
			[]string{"too many CLI logins in progress; try again in a few minutes"}},
		{"rate limit middleware", loginReply{status: 429, header: "17", body: `{"error":"rate limit exceeded","retry_after":17}`},
			[]string{"rate limit exceeded", "try again in 17 seconds"}},
		{"header only", loginReply{status: 503, header: "120", body: `{"error":"maintenance"}`},
			[]string{"maintenance", "try again in 2 minutes"}},
		{"bare failure", loginReply{status: 500, body: `oops`}, []string{"HTTP 500"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := newLoginBackend()
			backend.create = tc.reply
			h := newLoginHarness(t, backend)

			out, err := h.browser()
			if err == nil {
				t.Fatal("a refused login session must fail")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
			if n := strings.Count(err.Error(), "try again"); n > 1 {
				t.Errorf("error %q names the wait %d times; want once", err, n)
			}
			if msg := strings.ToLower(err.Error() + out); strings.Contains(msg, "expired") {
				t.Errorf("a refusal must not read as an expired session: %q / %q", err, out)
			}
			if len(h.opened) != 0 || backend.pollCalls != 0 || len(h.clock.slept) != 0 {
				t.Errorf("opened %v, polled %d times, slept %v; want none of it", h.opened, backend.pollCalls, h.clock.slept)
			}
			h.assertNothingSaved()
		})
	}
}

func TestBrowserLogin_IncompleteSession(t *testing.T) {
	for _, body := range []string{
		`{"code":"","expires_in":300,"login_url":"https://dash.test/auth/cli?code="}`,
		`{"code":"c0de","expires_in":300,"login_url":""}`,
		`{}`,
	} {
		backend := newLoginBackend()
		backend.create = loginReply{status: 200, body: body}
		h := newLoginHarness(t, backend)

		_, err := h.browser()
		if err == nil || !strings.Contains(err.Error(), "incomplete") {
			t.Errorf("%s: err = %v; want an incomplete-session error", body, err)
		}
		if len(h.opened) != 0 || backend.pollCalls != 0 {
			t.Errorf("%s: opened %v, polled %d; want neither", body, h.opened, backend.pollCalls)
		}
	}
}

func TestBrowserLogin_WaitsThenLogsIn(t *testing.T) {
	backend := newLoginBackend()
	backend.polls = []loginReply{
		{status: 202, body: `{"confirmed":false,"status":"waiting"}`},
		{status: 202, body: `{"confirmed":false,"status":"waiting"}`},
		{status: 200, body: `{"confirmed":true,"token":"jwt-1","email":"poll@b.dz"}`},
	}
	h := newLoginHarness(t, backend)

	out, err := h.browser()
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if len(h.opened) != 1 || h.opened[0] != "https://dash.test/auth/cli?code=c0de" {
		t.Errorf("opened %v; want the login page once", h.opened)
	}
	if got := len(h.clock.slept); got != 3 || h.clock.slept[0] != 5*time.Second {
		t.Errorf("slept %v; want three 5s waits", h.clock.slept)
	}
	if strings.Contains(out, "jwt-1") || strings.Contains(out, "gh_minted") {
		t.Error("a token was printed")
	}
	h.assertLoggedIn("a@b.dz")
}

func TestBrowserLogin_ExpiredSession(t *testing.T) {
	for _, status := range []int{404, 410} {
		backend := newLoginBackend()
		backend.polls = []loginReply{{status: 202, body: `{}`}, {status: status, body: `{"error":"session expired"}`}}
		h := newLoginHarness(t, backend)

		_, err := h.browser()
		if err == nil || !strings.Contains(err.Error(), "Login session expired") {
			t.Errorf("%d: err = %v; want the expired message", status, err)
		}
		h.assertNothingSaved()
	}
}

func TestBrowserLogin_ThrottledPollWaitsForTheServer(t *testing.T) {
	for _, throttle := range []loginReply{
		{status: 429, body: `{"error":"rate limit exceeded","retry_after":30}`},
		{status: 429, header: "30", body: `{"error":"rate limit exceeded"}`},
	} {
		backend := newLoginBackend()
		backend.polls = []loginReply{throttle, throttle, {status: 200, body: `{"confirmed":true,"token":"jwt-1","email":"a@b.dz"}`}}
		h := newLoginHarness(t, backend)

		out, err := h.browser()
		if err != nil {
			t.Fatalf("a throttled poll must not fail the login: %v", err)
		}
		want := []time.Duration{5 * time.Second, 30 * time.Second, 30 * time.Second}
		if len(h.clock.slept) != len(want) || h.clock.slept[1] != want[1] || h.clock.slept[2] != want[2] {
			t.Errorf("slept %v; want %v", h.clock.slept, want)
		}
		if n := strings.Count(out, "30 seconds"); n != 1 {
			t.Errorf("the wait was announced %d times; want once:\n%s", n, out)
		}
		h.assertLoggedIn("a@b.dz")
	}
}

func TestBrowserLogin_ServerTroubleSaidOnce(t *testing.T) {
	backend := newLoginBackend()
	backend.polls = []loginReply{
		{status: 503, body: `{"error":"unavailable"}`},
		{status: 502, body: `bad gateway`},
		{drop: true},
		{status: 200, body: `{"confirmed":true,"token":"jwt-1","email":"a@b.dz"}`},
	}
	h := newLoginHarness(t, backend)

	out, err := h.browser()
	if err != nil {
		t.Fatalf("server trouble before the deadline must not fail the login: %v", err)
	}
	if n := strings.Count(out, "Server unavailable, retrying"); n != 1 {
		t.Errorf("said %d times; want once:\n%s", n, out)
	}
	if backend.pollCalls != 4 {
		t.Errorf("polled %d times; want 4", backend.pollCalls)
	}
	h.assertLoggedIn("a@b.dz")
}

func TestBrowserLogin_PollTimesOutPerRequest(t *testing.T) {
	backend := newLoginBackend()
	backend.polls = []loginReply{{hang: true}, {status: 200, body: `{"confirmed":true,"token":"jwt-1","email":"a@b.dz"}`}}
	h := newLoginHarness(t, backend)
	h.flow.timeout = 50 * time.Millisecond

	out, err := h.browser()
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if !strings.Contains(out, "Server unavailable, retrying") {
		t.Errorf("a hung poll must be reported:\n%s", out)
	}
	h.assertLoggedIn("a@b.dz")
}

func TestBrowserLogin_CreateTimesOut(t *testing.T) {
	backend := newLoginBackend()
	backend.create = loginReply{hang: true}
	h := newLoginHarness(t, backend)
	h.flow.timeout = 50 * time.Millisecond

	_, err := h.browser()
	if err == nil || !strings.Contains(err.Error(), "Could not start the login") {
		t.Errorf("err = %v; want the create failure", err)
	}
	if len(h.opened) != 0 {
		t.Errorf("opened %v", h.opened)
	}
}

func TestBrowserLogin_TimesOutAtTheDeadline(t *testing.T) {
	cases := []struct {
		name   string
		create string
		wait   time.Duration
		want   time.Duration
	}{
		{"server expiry", `{"code":"c0de","expires_in":60,"login_url":"https://dash.test/x"}`, 5 * time.Minute, 60 * time.Second},
		{"no expiry named", `{"code":"c0de","login_url":"https://dash.test/x"}`, 40 * time.Second, 40 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := newLoginBackend()
			backend.create = loginReply{status: 200, body: tc.create}
			backend.polls = []loginReply{{status: 202, body: `{"confirmed":false,"status":"waiting"}`}}
			h := newLoginHarness(t, backend)
			h.flow.wait = tc.wait
			start := h.clock.at

			_, err := h.browser()
			if err == nil || !strings.Contains(err.Error(), "timed out") {
				t.Fatalf("err = %v; want a timeout", err)
			}
			if got := h.clock.at.Sub(start); got != tc.want {
				t.Errorf("waited %v; want %v", got, tc.want)
			}
			if want := int(tc.want/(5*time.Second)) - 1; backend.pollCalls != want {
				t.Errorf("polled %d times; want %d", backend.pollCalls, want)
			}
		})
	}
}

func TestBrowserLogin_ThrottleNeverWaitsPastTheDeadline(t *testing.T) {
	backend := newLoginBackend()
	backend.create = loginReply{status: 200, body: `{"code":"c0de","expires_in":60,"login_url":"https://dash.test/x"}`}
	backend.polls = []loginReply{{status: 429, body: `{"error":"rate limit exceeded","retry_after":3600}`}}
	h := newLoginHarness(t, backend)
	start := h.clock.at

	out, err := h.browser()
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v; want a timeout", err)
	}
	if got := h.clock.at.Sub(start); got != 60*time.Second {
		t.Errorf("waited %v; want the 60s deadline", got)
	}
	if !strings.Contains(out, "checking again in 55 seconds") || strings.Contains(out, "minutes") {
		t.Errorf("the announced wait must be the capped one:\n%s", out)
	}
}

func TestBrowserLogin_ShortRetryAfterKeepsTheInterval(t *testing.T) {
	backend := newLoginBackend()
	backend.polls = []loginReply{
		{status: 429, body: `{"error":"rate limit exceeded","retry_after":1}`},
		{status: 200, body: `{"confirmed":true,"token":"jwt-1","email":"a@b.dz"}`},
	}
	h := newLoginHarness(t, backend)

	out, err := h.browser()
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if len(h.clock.slept) != 2 || h.clock.slept[1] != 5*time.Second {
		t.Errorf("slept %v; want a throttled poll no sooner than a normal one", h.clock.slept)
	}
	if !strings.Contains(out, "checking again in 5 seconds") {
		t.Errorf("the announced wait must be the one used:\n%s", out)
	}
}

// A poll started just before the deadline must not outlive it.
func TestBrowserLogin_PollRequestEndsAtTheDeadline(t *testing.T) {
	backend := newLoginBackend()
	backend.create = loginReply{status: 200, body: `{"code":"c0de","login_url":"https://dash.test/x"}`}
	backend.polls = []loginReply{{hang: true}}
	h := newLoginHarness(t, backend)
	h.flow.wait = 5*time.Second + 50*time.Millisecond
	h.flow.timeout = 3 * time.Second

	start := time.Now()
	_, err := h.browser()
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v; want a timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Errorf("took %v; the last poll ran past the deadline", elapsed)
	}
}

func TestBrowserLogin_OtherRefusalStops(t *testing.T) {
	backend := newLoginBackend()
	backend.polls = []loginReply{{status: 403, body: `{"error":"forbidden here"}`}}
	h := newLoginHarness(t, backend)

	_, err := h.browser()
	if err == nil || !strings.Contains(err.Error(), "forbidden here") {
		t.Errorf("err = %v; want the server's message", err)
	}
	if backend.pollCalls != 1 {
		t.Errorf("polled %d times; want 1", backend.pollCalls)
	}
}

func TestBrowserLogin_ApprovedWithoutToken(t *testing.T) {
	backend := newLoginBackend()
	backend.polls = []loginReply{{status: 200, body: `{"confirmed":true}`}}
	h := newLoginHarness(t, backend)

	_, err := h.browser()
	if err == nil || !strings.Contains(err.Error(), "sent no session") {
		t.Errorf("err = %v; want the no-session error", err)
	}
	if backend.pollCalls != 1 {
		t.Errorf("polled %d times; want 1", backend.pollCalls)
	}
	h.assertNothingSaved()
}

func TestLogin_PrivacyConsent(t *testing.T) {
	required := loginReply{status: 200, body: `{"id":"u1","email":"a@b.dz","name":"A","privacy_consent_required":true}`}
	cases := []struct {
		name        string
		me          loginReply
		accept      loginReply
		flag        bool
		answer      string
		wantErr     string
		wantAsked   int
		wantAccepts int
	}{
		{"accepted", required, loginReply{status: 200, body: `{}`}, false, "y", "", 1, 1},
		{"flag", required, loginReply{status: 200, body: `{}`}, true, "", "", 0, 1},
		{"declined", required, loginReply{status: 200, body: `{}`}, false, "n", "Privacy Policy must be accepted", 1, 0},
		{"accept fails", required, loginReply{status: 500, body: `{"error":"could not save"}`}, false, "yes", "could not save", 1, 1},
		{"not required", loginReply{status: 200, body: `{"id":"u1","email":"a@b.dz","name":"A","privacy_consent_required":false}`}, loginReply{}, false, "", "", 0, 0},
		{"older backend", loginReply{status: 200, body: `{"id":"u1","email":"a@b.dz","name":"A"}`}, loginReply{}, false, "", "", 0, 0},
		{"account unreadable", loginReply{status: 503, body: `{"error":"database unavailable"}`}, loginReply{}, false, "", "database unavailable", 0, 0},
		{"account malformed", loginReply{status: 200, body: `<html>captive portal</html>`}, loginReply{}, false, "", "Could not check your account", 0, 0},
		{"account truncated", loginReply{status: 200, body: `{"id":"u1","email":"a@b.dz","privacy_consent_required":tr`}, loginReply{}, false, "", "Could not check your account", 0, 0},
		{"account without id", loginReply{status: 200, body: `{"email":"a@b.dz","privacy_consent_required":false}`}, loginReply{}, false, "", "Could not check your account", 0, 0},
	}
	paths := map[string]func(h *loginHarness) (string, error){
		"browser": (*loginHarness).browser,
		"email":   (*loginHarness).email,
	}
	for path, run := range paths {
		for _, tc := range cases {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				backend := newLoginBackend()
				backend.me, backend.accept = tc.me, tc.accept
				h := newLoginHarness(t, backend)
				h.flow.acceptPrivacy, h.answer = tc.flag, tc.answer

				out, err := run(h)
				if h.asked != tc.wantAsked || len(backend.acceptAuth) != tc.wantAccepts {
					t.Errorf("asked %d, accept calls %d; want %d, %d", h.asked, len(backend.acceptAuth), tc.wantAsked, tc.wantAccepts)
				}
				if backend.meCalls != 1 {
					t.Errorf("read the account %d times; want once", backend.meCalls)
				}
				if tc.wantErr != "" {
					if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
						t.Fatalf("err = %v; want %q", err, tc.wantErr)
					}
					h.assertNothingSaved()
					return
				}
				if err != nil {
					t.Fatalf("login: %v", err)
				}
				h.assertLoggedIn("a@b.dz")
				if tc.wantAccepts == 1 && len(backend.acceptAuth) == 1 {
					if backend.acceptAuth[0] != "Bearer jwt-1" || backend.acceptBody[0] != `{"privacy_version":"1.0"}` {
						t.Errorf("accept sent auth match %v, body %s", backend.acceptAuth[0] == "Bearer jwt-1", backend.acceptBody[0])
					}
					if !strings.Contains(out, privacyConsentText()) {
						t.Errorf("the consent text was not shown:\n%s", out)
					}
				}
			})
		}
	}
}

func TestLogin_ConsentWithoutTerminal(t *testing.T) {
	required := loginReply{status: 200, body: `{"id":"u1","email":"a@b.dz","name":"A","privacy_consent_required":true}`}
	notRequired := loginReply{status: 200, body: `{"id":"u1","email":"a@b.dz","name":"A"}`}
	paths := map[string]func(h *loginHarness) (string, error){
		"browser": (*loginHarness).browser,
		"email":   (*loginHarness).email,
	}
	for path, run := range paths {
		t.Run(path+"/refused at once", func(t *testing.T) {
			backend := newLoginBackend()
			backend.me = required
			h := newLoginHarness(t, backend)
			forceStdin(t, false)

			_, err := run(h)
			if err == nil || !strings.Contains(err.Error(), "--accept-privacy") {
				t.Errorf("err = %v; want the --accept-privacy hint", err)
			}
			if h.asked != 0 || len(backend.acceptAuth) != 0 {
				t.Errorf("asked %d, accept calls %d; want neither", h.asked, len(backend.acceptAuth))
			}
			h.assertNothingSaved()
		})
		t.Run(path+"/flag", func(t *testing.T) {
			backend := newLoginBackend()
			backend.me = required
			h := newLoginHarness(t, backend)
			forceStdin(t, false)
			h.flow.acceptPrivacy = true

			if _, err := run(h); err != nil {
				t.Fatalf("login: %v", err)
			}
			if h.asked != 0 || len(backend.acceptAuth) != 1 {
				t.Errorf("asked %d, accept calls %d; want 0, 1", h.asked, len(backend.acceptAuth))
			}
			h.assertLoggedIn("a@b.dz")
		})
		t.Run(path+"/not required", func(t *testing.T) {
			backend := newLoginBackend()
			backend.me = notRequired
			h := newLoginHarness(t, backend)
			forceStdin(t, false)

			if _, err := run(h); err != nil {
				t.Fatalf("login: %v", err)
			}
			h.assertLoggedIn("a@b.dz")
		})
	}
}

// completeLogin bounds both account calls, so a hung server ends the login.
func TestLogin_AccountCallsTimeOut(t *testing.T) {
	cases := map[string]func(b *loginBackend){
		"me": func(b *loginBackend) { b.me = loginReply{hang: true} },
		"accept": func(b *loginBackend) {
			b.me = loginReply{status: 200, body: `{"id":"u1","email":"a@b.dz","privacy_consent_required":true}`}
			b.accept = loginReply{hang: true}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			backend := newLoginBackend()
			setup(backend)
			h := newLoginHarness(t, backend)
			h.flow.timeout = 50 * time.Millisecond
			h.answer = "y"

			done := make(chan error, 1)
			go func() { _, err := h.email(); done <- err }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("a hung account call must fail the login")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("login hung on the account call")
			}
			h.assertNothingSaved()
		})
	}
}

func TestEmailLogin_RefusalShowsServerMessage(t *testing.T) {
	for _, reply := range []loginReply{
		{status: 429, header: "60", body: `{"error":"rate limit exceeded","retry_after":60}`},
		{status: 503, header: "60", body: `{"error":"rate limit exceeded"}`},
	} {
		backend := newLoginBackend()
		backend.login = reply
		h := newLoginHarness(t, backend)

		out, err := h.email()
		if err == nil {
			t.Fatal("a refused login must fail")
		}
		msg := err.Error() + out
		if !strings.Contains(msg, "rate limit exceeded") || !strings.Contains(msg, "try again in 1 minute") {
			t.Errorf("%d: got %q; want the server's message and wait", reply.status, msg)
		}
		if strings.Contains(msg, "session expired") {
			t.Errorf("%d: a refusal reads as a dead session: %q", reply.status, msg)
		}
		if backend.meCalls != 0 {
			t.Error("a refused login must not go on to read the account")
		}
		h.assertNothingSaved()
	}
}

func TestEmailLogin_WrongPassword(t *testing.T) {
	backend := newLoginBackend()
	backend.login = loginReply{status: 401, body: `{"error":"invalid email or password"}`}
	h := newLoginHarness(t, backend)

	_, err := h.email()
	if err == nil || !strings.Contains(err.Error(), "invalid email or password") || strings.Contains(err.Error(), "session expired") {
		t.Errorf("err = %v; want the server's message", err)
	}
}

func TestLoginCommand_RefusalExitsNonZero(t *testing.T) {
	backend := newLoginBackend()
	backend.create = loginReply{status: 503, header: "60", body: `{"error":"too many CLI logins in progress; try again in a few minutes","retry_after":60}`}
	ts := backend.serve(t)
	t.Setenv("GHAYMA_CONFIG", filepath.Join(t.TempDir(), "cli.json"))
	t.Setenv("GHAYMA_API_HOST", ts.URL)
	orig := defaultLoginFlow
	defaultLoginFlow.openBrowser = func(string) { t.Error("the browser must not open") }
	t.Cleanup(func() { defaultLoginFlow = orig })

	out := runCLI(t, t.TempDir(), "login")
	if lastExitCode != 1 {
		t.Errorf("exit code %d; want 1", lastExitCode)
	}
	if !strings.Contains(out, "too many CLI logins in progress; try again in a few minutes") || strings.Count(out, "try again") != 1 {
		t.Errorf("output lacks the server's message, or names the wait twice:\n%s", out)
	}
	if backend.pollCalls != 0 {
		t.Errorf("polled %d times; want none", backend.pollCalls)
	}
}

func TestLoginCommand_AcceptPrivacyFlagReachesTheFlow(t *testing.T) {
	backend := newLoginBackend()
	backend.me = loginReply{status: 200, body: `{"id":"u1","email":"a@b.dz","privacy_consent_required":true}`}
	ts := backend.serve(t)
	cfgPath := filepath.Join(t.TempDir(), "cli.json")
	t.Setenv("GHAYMA_CONFIG", cfgPath)
	t.Setenv("GHAYMA_API_HOST", ts.URL)
	forceStdin(t, false)
	clock := &fakeClock{at: time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)}
	orig := defaultLoginFlow
	defaultLoginFlow.now, defaultLoginFlow.sleep = clock.now, clock.sleep
	defaultLoginFlow.openBrowser = func(string) {}
	defaultLoginFlow.askPrivacy = func() (string, error) {
		t.Error("--accept-privacy must answer the question")
		return "", nil
	}
	t.Cleanup(func() { defaultLoginFlow = orig })

	out := runCLI(t, t.TempDir(), "login", "--accept-privacy")
	if lastExitCode != 0 {
		t.Fatalf("exit code %d; want 0:\n%s", lastExitCode, out)
	}
	if len(backend.acceptAuth) != 1 || backend.acceptBody[0] != `{"privacy_version":"1.0"}` {
		t.Errorf("accept calls %v; want one with version 1.0", backend.acceptBody)
	}
	if loginAcceptPrivacy {
		t.Error("resetCommandFlags must clear --accept-privacy")
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil || !strings.Contains(string(raw), "gh_minted") {
		t.Errorf("the login was not saved: %v", err)
	}
}

func TestRegister_WithoutTerminalFailsAtOnce(t *testing.T) {
	forceStdin(t, false)
	t.Setenv("GHAYMA_CONFIG", filepath.Join(t.TempDir(), "cli.json"))
	t.Setenv("GHAYMA_API_HOST", "http://127.0.0.1:1")

	out := runCLI(t, t.TempDir(), "register")
	if lastExitCode != 1 || !strings.Contains(out, "--accept-privacy") {
		t.Errorf("exit %d, output:\n%s\nwant exit 1 and the --accept-privacy hint", lastExitCode, out)
	}
	if strings.Contains(out, "Full Name") {
		t.Errorf("register prompted before refusing:\n%s", out)
	}
}

// feedPrompts points register's prompts at input for one test, so they never
// read the test binary's own stdin.
func feedPrompts(t *testing.T, input string) {
	t.Helper()
	orig := promptStdin
	promptStdin = io.NopCloser(strings.NewReader(input))
	t.Cleanup(func() { promptStdin = orig })
}

// registerStub answers POST /auth/register and returns the bodies it got.
func registerStub(t *testing.T) *[]map[string]string {
	t.Helper()
	var mu sync.Mutex
	var bodies []map[string]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/auth/register" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"user":{"id":"u1","email":"a@b.dz","name":"A"},"message":"Check your inbox to verify your email."}`)
	}))
	t.Cleanup(ts.Close)
	t.Setenv("GHAYMA_CONFIG", filepath.Join(t.TempDir(), "cli.json"))
	t.Setenv("GHAYMA_API_HOST", ts.URL)
	return &bodies
}

func TestRegister_AcceptPrivacyWithoutTerminal(t *testing.T) {
	forceStdin(t, false)
	feedPrompts(t, "")
	bodies := registerStub(t)

	out := runCLI(t, t.TempDir(), "register", "--accept-privacy")
	if strings.Contains(out, "Run again in a terminal") || lastExitCode != 0 {
		t.Errorf("--accept-privacy must get past the no-terminal check; exit %d:\n%s", lastExitCode, out)
	}
	if len(*bodies) != 1 || (*bodies)[0]["privacy_version"] != "1.0" {
		t.Errorf("register calls %v; want one with privacy_version 1.0", *bodies)
	}
}

func TestRegister_DeclineExitsNonZero(t *testing.T) {
	forceStdin(t, true)
	feedPrompts(t, "")
	bodies := registerStub(t)

	out := runCLI(t, t.TempDir(), "register")
	if lastExitCode != 1 || !strings.Contains(out, "Registration cancelled: the Privacy Policy must be accepted.") {
		t.Errorf("exit %d, output:\n%s\nwant exit 1 and the cancellation", lastExitCode, out)
	}
	if len(*bodies) != 0 {
		t.Errorf("a declined registration reached the server: %v", *bodies)
	}
}

func TestLoginAcceptPrivacyFlagWired(t *testing.T) {
	f := loginCmd.Flags().Lookup("accept-privacy")
	if f == nil {
		t.Fatal("login must expose --accept-privacy, as register does")
	}
	if f.DefValue != "false" {
		t.Errorf("--accept-privacy default = %q; must be false so consent is asked", f.DefValue)
	}
}

func TestWaitText(t *testing.T) {
	for d, want := range map[time.Duration]string{
		500 * time.Millisecond: "1 second",
		time.Second:            "1 second",
		17 * time.Second:       "17 seconds",
		60 * time.Second:       "1 minute",
		90 * time.Second:       "2 minutes",
	} {
		if got := waitText(d); got != want {
			t.Errorf("waitText(%v) = %q; want %q", d, got, want)
		}
	}
}
