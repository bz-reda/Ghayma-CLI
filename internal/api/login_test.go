package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"
)

func jsonReply(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	io.WriteString(w, body)
}

func asAPIError(t *testing.T, err error) *APIError {
	t.Helper()
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v (%T); want *APIError", err, err)
	}
	return apiErr
}

func TestLogin_RefusalCarriesServerMessage(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		header    string
		body      string
		wantMsg   string
		wantRetry time.Duration
	}{
		{"rate limit body", http.StatusTooManyRequests, "", `{"error":"rate limit exceeded","retry_after":42}`, "rate limit exceeded", 42 * time.Second},
		{"unavailable header only", http.StatusServiceUnavailable, "30", `{"error":"try again later"}`, "try again later", 30 * time.Second},
		{"wrong password", http.StatusUnauthorized, "", `{"error":"invalid email or password"}`, "invalid email or password", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.header != "" {
					w.Header().Set("Retry-After", tc.header)
				}
				jsonReply(w, tc.status, tc.body)
			}))
			defer ts.Close()

			_, err := newTestClient(ts.URL).Login("a@b.dz", "pw")
			if errors.Is(err, ErrUnauthorized) || strings.Contains(err.Error(), "session expired") {
				t.Fatalf("login refusal reads as a dead session: %v", err)
			}
			apiErr := asAPIError(t, err)
			if apiErr.Status != tc.status || apiErr.Message != tc.wantMsg || apiErr.RetryAfter != tc.wantRetry {
				t.Errorf("got status %d message %q retry %v; want %d %q %v", apiErr.Status, apiErr.Message, apiErr.RetryAfter, tc.status, tc.wantMsg, tc.wantRetry)
			}
		})
	}
}

func TestRetryAfter_BodyWinsThenHeaderSecondsThenDate(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name   string
		header string
		body   string
		min    time.Duration
		max    time.Duration
	}{
		{"body", "5", `{"error":"x","retry_after":60}`, 60 * time.Second, 60 * time.Second},
		{"header seconds", "7", `{"error":"x"}`, 7 * time.Second, 7 * time.Second},
		{"header date", now.Add(90 * time.Second).UTC().Format(http.TimeFormat), `{"error":"x"}`, 80 * time.Second, 91 * time.Second},
		{"none", "", `{"error":"x"}`, 0, 0},
		{"junk", "soon", `{"error":"x","retry_after":"later"}`, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.header != "" {
					w.Header().Set("Retry-After", tc.header)
				}
				jsonReply(w, http.StatusTooManyRequests, tc.body)
			}))
			defer ts.Close()

			_, err := newTestClient(ts.URL).CreateCLISession(context.Background())
			apiErr := asAPIError(t, err)
			if apiErr.Message != "x" {
				t.Errorf("message = %q; want x", apiErr.Message)
			}
			if apiErr.RetryAfter < tc.min || apiErr.RetryAfter > tc.max {
				t.Errorf("retry = %v; want within [%v, %v]", apiErr.RetryAfter, tc.min, tc.max)
			}
		})
	}
}

func TestCreateCLISession(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/auth/cli/session" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		jsonReply(w, http.StatusOK, `{"code":"c0de","expires_in":300,"login_url":"https://dash.test/auth/cli?code=c0de"}`)
	}))
	defer ts.Close()

	s, err := newTestClient(ts.URL).CreateCLISession(context.Background())
	if err != nil {
		t.Fatalf("CreateCLISession: %v", err)
	}
	if s.Code != "c0de" || s.ExpiresIn != 300 || s.LoginURL != "https://dash.test/auth/cli?code=c0de" {
		t.Errorf("session = %+v", s)
	}
}

func TestCreateCLISession_Refusals(t *testing.T) {
	for _, tc := range []struct {
		status int
		msg    string
	}{
		{http.StatusTooManyRequests, "too many pending CLI logins from your network; finish one or wait a few minutes"},
		{http.StatusServiceUnavailable, "too many CLI logins in progress; try again in a few minutes"},
	} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "60")
			body, _ := json.Marshal(map[string]any{"error": tc.msg, "retry_after": 60})
			jsonReply(w, tc.status, string(body))
		}))
		_, err := newTestClient(ts.URL).CreateCLISession(context.Background())
		ts.Close()

		apiErr := asAPIError(t, err)
		if apiErr.Status != tc.status || apiErr.Message != tc.msg || apiErr.RetryAfter != time.Minute {
			t.Errorf("%d: got %+v", tc.status, apiErr)
		}
	}
}

func TestPollCLISession(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr bool
		want    CLISessionPoll
	}{
		{"waiting", http.StatusAccepted, `{"confirmed":false,"status":"waiting"}`, false, CLISessionPoll{Status: http.StatusAccepted}},
		{"approved", http.StatusOK, `{"confirmed":true,"token":"jwt-1","email":"a@b.dz"}`, false, CLISessionPoll{Status: http.StatusOK, Confirmed: true, Token: "jwt-1", Email: "a@b.dz"}},
		{"unknown", http.StatusNotFound, `{"error":"session not found or expired"}`, true, CLISessionPoll{}},
		{"expired", http.StatusGone, `{"error":"session expired"}`, true, CLISessionPoll{}},
		{"throttled", http.StatusTooManyRequests, `{"error":"rate limit exceeded","retry_after":12}`, true, CLISessionPoll{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/auth/cli/session/c0de" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				jsonReply(w, tc.status, tc.body)
			}))
			defer ts.Close()

			got, err := newTestClient(ts.URL).PollCLISession(context.Background(), "c0de")
			if tc.wantErr {
				apiErr := asAPIError(t, err)
				if apiErr.Status != tc.status {
					t.Errorf("status = %d; want %d", apiErr.Status, tc.status)
				}
				if tc.status == http.StatusTooManyRequests && apiErr.RetryAfter != 12*time.Second {
					t.Errorf("retry = %v; want 12s", apiErr.RetryAfter)
				}
				return
			}
			if err != nil {
				t.Fatalf("PollCLISession: %v", err)
			}
			if *got != tc.want {
				t.Errorf("poll = %+v; want %+v", *got, tc.want)
			}
		})
	}
}

// hungServer never answers. Its handlers end when the test does, so a call
// that ignores its deadline fails the test instead of hanging the run.
func hungServer(t *testing.T) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The server only notices a dropped client once the body is read.
		io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(ts.Close)
	t.Cleanup(func() { close(release) })
	return ts
}

// assertDeadlineEnds runs each call with a 50ms deadline and fails within 5s
// when one outlives it.
func assertDeadlineEnds(t *testing.T, calls map[string]func(ctx context.Context) error) {
	t.Helper()
	for name, call := range calls {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		done := make(chan error, 1)
		go func() { done <- call(ctx) }()
		select {
		case err := <-done:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("%s: err = %v; want the deadline", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("%s ran past a 50ms deadline", name)
		}
		cancel()
	}
}

// A server that never answers must not hang the login: the caller's deadline ends both calls.
func TestCLISessionCalls_HonourTheDeadline(t *testing.T) {
	c := newTestClient(hungServer(t).URL)
	assertDeadlineEnds(t, map[string]func(ctx context.Context) error{
		"create": func(ctx context.Context) error { _, err := c.CreateCLISession(ctx); return err },
		"poll":   func(ctx context.Context) error { _, err := c.PollCLISession(ctx, "c0de"); return err },
	})
}

func TestGetMe_PrivacyConsentRequired(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{"id":"u1","email":"a@b.dz","name":"A","privacy_consent_required":true}`, true},
		{`{"id":"u1","email":"a@b.dz","name":"A","privacy_consent_required":false}`, false},
		{`{"id":"u1","email":"a@b.dz","name":"A"}`, false},
	} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Authorization"); got != "Bearer jwt-fresh" {
				t.Errorf("auth header = %q; want the fresh session", got)
			}
			jsonReply(w, http.StatusOK, tc.body)
		}))
		me, err := newTestClient(ts.URL).GetMe(context.Background(), "jwt-fresh")
		ts.Close()
		if err != nil {
			t.Fatalf("GetMe: %v", err)
		}
		if me.ID != "u1" || me.PrivacyConsentRequired != tc.want {
			t.Errorf("%s: got %+v; want consent required %v", tc.body, me, tc.want)
		}
	}
}

func TestGetMe_RefusalCarriesServerMessage(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, http.StatusServiceUnavailable, `{"error":"database unavailable"}`)
	}))
	defer ts.Close()

	_, err := newTestClient(ts.URL).GetMe(context.Background(), "jwt-fresh")
	if apiErr := asAPIError(t, err); apiErr.Message != "database unavailable" {
		t.Errorf("message = %q", apiErr.Message)
	}
}

// A 200 that won't decode must not read as "no consent needed".
func TestGetMe_MalformedBodyIsAnError(t *testing.T) {
	for _, body := range []string{
		`<html>captive portal</html>`,
		`{"id":"u1","email":"a@b.dz","privacy_consent_required":tr`,
	} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			jsonReply(w, http.StatusOK, body)
		}))
		me, err := newTestClient(ts.URL).GetMe(context.Background(), "jwt-fresh")
		ts.Close()
		if err == nil {
			t.Errorf("%s: got %+v and no error; want the decode error", body, me)
		}
	}
}

func TestGetMe_RetriesTransportFailures(t *testing.T) {
	shrinkBackoff(t)
	st := &stubTransport{fn: func(attempt int, req *http.Request) (*http.Response, error) {
		if attempt == 1 {
			return nil, &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}
		}
		resp := stubResponse(http.StatusOK, req)
		resp.Body = io.NopCloser(strings.NewReader(`{"id":"u1","email":"a@b.dz"}`))
		return resp, nil
	}}
	me, err := stubClient(t, st).GetMe(context.Background(), "jwt-fresh")
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	if me.ID != "u1" || st.calls.Load() != 2 {
		t.Errorf("got %+v after %d calls; want u1 after one retry", me, st.calls.Load())
	}
}

func TestGetMeAndAcceptPrivacy_HonourTheDeadline(t *testing.T) {
	c := newTestClient(hungServer(t).URL)
	assertDeadlineEnds(t, map[string]func(ctx context.Context) error{
		"me":     func(ctx context.Context) error { _, err := c.GetMe(ctx, "jwt-fresh"); return err },
		"accept": func(ctx context.Context) error { return c.AcceptPrivacy(ctx, "jwt-fresh", "1.0") },
	})
}

func TestAcceptPrivacy(t *testing.T) {
	var gotAuth, gotPath, gotMethod string
	var gotBody map[string]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath, gotMethod = r.Header.Get("Authorization"), r.URL.Path, r.Method
		json.NewDecoder(r.Body).Decode(&gotBody)
		jsonReply(w, http.StatusOK, `{"privacy_version":"1.0","privacy_accepted_at":"2026-10-29T10:00:00Z"}`)
	}))
	defer ts.Close()

	if err := newTestClient(ts.URL).AcceptPrivacy(context.Background(), "jwt-fresh", "1.0"); err != nil {
		t.Fatalf("AcceptPrivacy: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/auth/privacy/accept" {
		t.Errorf("request = %s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer jwt-fresh" {
		t.Errorf("auth header = %q; want the fresh session, not the saved one", gotAuth)
	}
	if gotBody["privacy_version"] != "1.0" {
		t.Errorf("body = %v", gotBody)
	}
}

func TestAcceptPrivacy_Refusal(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, http.StatusBadRequest, `{"error":"unknown privacy policy version","code":"invalid_privacy_version"}`)
	}))
	defer ts.Close()

	err := newTestClient(ts.URL).AcceptPrivacy(context.Background(), "jwt-fresh", "9.9")
	apiErr := asAPIError(t, err)
	if apiErr.Status != http.StatusBadRequest || apiErr.Code != "invalid_privacy_version" || apiErr.Message != "unknown privacy policy version" {
		t.Errorf("got %+v", apiErr)
	}
}
