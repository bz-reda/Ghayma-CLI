package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Site protection client. These pin the wire contract of Ghayma-backend #520.

const protectionBase = "/api/v1/projects/p1/sites/s1/protection"

const exampleView = `{
  "available": true,
  "enabled": true,
  "effective": true,
  "environment": "staging",
  "emails": [{"id": "e1", "email": "ana@example.com", "has_account": true, "created_at": "2026-11-14T10:21:33Z"}],
  "tokens": [{"id": "t1", "name": "CI", "hint": "gpb_…Xy3Q", "created_at": "2026-11-14T10:22:01Z"}],
  "limits": {"emails": 50, "tokens": 5}
}`

func TestGetProtection_DecodesTheView(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != protectionBase {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		io.WriteString(w, exampleView)
	}))
	defer ts.Close()

	p, err := newTestClient(ts.URL).GetProtection("p1", "s1")
	if err != nil {
		t.Fatalf("GetProtection: %v", err)
	}
	if !p.Available || !p.Enabled || !p.Effective || p.Environment != "staging" {
		t.Errorf("flags = %+v", p)
	}
	if p.Limits != (ProtectionLimits{Emails: 50, Tokens: 5}) {
		t.Errorf("limits = %+v", p.Limits)
	}
	wantEmail := ProtectionEmail{ID: "e1", Email: "ana@example.com", HasAccount: true, CreatedAt: time.Date(2026, 11, 14, 10, 21, 33, 0, time.UTC)}
	if len(p.Emails) != 1 || !p.Emails[0].CreatedAt.Equal(wantEmail.CreatedAt) ||
		p.Emails[0].ID != wantEmail.ID || p.Emails[0].Email != wantEmail.Email || !p.Emails[0].HasAccount {
		t.Errorf("emails = %+v", p.Emails)
	}
	wantToken := ProtectionToken{ID: "t1", Name: "CI", Hint: "gpb_…Xy3Q", CreatedAt: time.Date(2026, 11, 14, 10, 22, 1, 0, time.UTC)}
	if len(p.Tokens) != 1 || !p.Tokens[0].CreatedAt.Equal(wantToken.CreatedAt) ||
		p.Tokens[0].ID != wantToken.ID || p.Tokens[0].Name != wantToken.Name || p.Tokens[0].Hint != wantToken.Hint {
		t.Errorf("tokens = %+v", p.Tokens)
	}
}

// An empty list on the wire must decode as an empty slice, not nil.
func TestGetProtection_EmptyListsAreNotNil(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"available":true,"emails":[],"tokens":[],"limits":{"emails":50,"tokens":5}}`)
	}))
	defer ts.Close()

	p, err := newTestClient(ts.URL).GetProtection("p1", "s1")
	if err != nil {
		t.Fatalf("GetProtection: %v", err)
	}
	if p.Emails == nil || len(p.Emails) != 0 || p.Tokens == nil || len(p.Tokens) != 0 {
		t.Errorf("emails = %#v, tokens = %#v; want empty non-nil slices", p.Emails, p.Tokens)
	}
}

func TestSetProtection_SendsEnabled(t *testing.T) {
	var raw string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != protectionBase {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		raw = string(b)
		io.WriteString(w, exampleView)
	}))
	defer ts.Close()

	p, err := newTestClient(ts.URL).SetProtection("p1", "s1", true)
	if err != nil {
		t.Fatalf("SetProtection: %v", err)
	}
	if raw != `{"enabled":true}` {
		t.Errorf("body = %s", raw)
	}
	if !p.Enabled {
		t.Errorf("view = %+v; want the server's answer decoded", p)
	}

	if _, err := newTestClient(ts.URL).SetProtection("p1", "s1", false); err != nil {
		t.Fatalf("SetProtection: %v", err)
	}
	if raw != `{"enabled":false}` {
		t.Errorf("body = %s; false must be on the wire", raw)
	}
}

func TestSetProtection_CarriesRouteApplyFailed(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, `{"error":"protection is saved but the route could not be applied","code":"route_apply_failed"}`)
	}))
	defer ts.Close()

	_, err := newTestClient(ts.URL).SetProtection("p1", "s1", true)
	apiErr := asAPIError(t, err)
	if apiErr.Status != http.StatusBadGateway || apiErr.Code != CodeRouteApplyFailed || apiErr.Message == "" {
		t.Errorf("err = %+v; want the 502, its code and its sentence", apiErr)
	}
}

func TestAddProtectionEmail_PostsAndCarriesCodes(t *testing.T) {
	var body map[string]string
	status, answer := http.StatusCreated, exampleView
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != protectionBase+"/emails" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(status)
		io.WriteString(w, answer)
	}))
	defer ts.Close()
	c := newTestClient(ts.URL)

	p, err := c.AddProtectionEmail("p1", "s1", "ana@example.com")
	if err != nil {
		t.Fatalf("AddProtectionEmail: %v", err)
	}
	if body["email"] != "ana@example.com" {
		t.Errorf("body = %v", body)
	}
	if len(p.Emails) != 1 {
		t.Errorf("view = %+v; want the server's answer decoded", p)
	}

	for _, tc := range []struct {
		status int
		code   string
	}{
		{http.StatusConflict, CodeEmailExists},
		{http.StatusUnprocessableEntity, CodeInvalidEmail},
	} {
		status, answer = tc.status, `{"error":"refused","code":"`+tc.code+`"}`
		_, err := c.AddProtectionEmail("p1", "s1", "ana@example.com")
		apiErr := asAPIError(t, err)
		if apiErr.Status != tc.status || apiErr.Code != tc.code {
			t.Errorf("err = %+v; want %d %s", apiErr, tc.status, tc.code)
		}
	}
}

func TestRemoveProtectionEmail_DeletesByID(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != protectionBase+"/emails/e1" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		io.WriteString(w, `{"available":true,"emails":[],"tokens":[]}`)
	}))
	defer ts.Close()

	p, err := newTestClient(ts.URL).RemoveProtectionEmail("p1", "s1", "e1")
	if err != nil {
		t.Fatalf("RemoveProtectionEmail: %v", err)
	}
	if !p.Available || len(p.Emails) != 0 {
		t.Errorf("view = %+v", p)
	}
}

func TestCreateBypassToken_ReturnsSecretAndView(t *testing.T) {
	var body map[string]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != protectionBase+"/tokens" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"token":"gpb_abc","view":`+exampleView+`}`)
	}))
	defer ts.Close()

	out, err := newTestClient(ts.URL).CreateBypassToken("p1", "s1", "CI")
	if err != nil {
		t.Fatalf("CreateBypassToken: %v", err)
	}
	if body["name"] != "CI" {
		t.Errorf("body = %v", body)
	}
	if out.Token != "gpb_abc" || out.View == nil || len(out.View.Tokens) != 1 {
		t.Errorf("out = %+v", out)
	}
}

// The secret exists only in this answer, so a missing view must not cost it.
func TestCreateBypassToken_NullView(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"token":"gpb_abc","view":null}`)
	}))
	defer ts.Close()

	out, err := newTestClient(ts.URL).CreateBypassToken("p1", "s1", "CI")
	if err != nil {
		t.Fatalf("CreateBypassToken: %v", err)
	}
	if out.Token != "gpb_abc" || out.View != nil {
		t.Errorf("out = %+v; want the token and a nil view", out)
	}
}

func TestRevokeBypassToken_DeletesByID(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != protectionBase+"/tokens/t1" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		io.WriteString(w, `{"available":true,"emails":[],"tokens":[]}`)
	}))
	defer ts.Close()

	p, err := newTestClient(ts.URL).RevokeBypassToken("p1", "s1", "t1")
	if err != nil {
		t.Fatalf("RevokeBypassToken: %v", err)
	}
	if !p.Available || len(p.Tokens) != 0 {
		t.Errorf("view = %+v", p)
	}
}

// A user-typed ID must stay one path segment.
func TestProtection_IDsAreEscaped(t *testing.T) {
	var paths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.EscapedPath())
		io.WriteString(w, `{}`)
	}))
	defer ts.Close()
	c := newTestClient(ts.URL)

	if _, err := c.RemoveProtectionEmail("p1", "s1", "../x"); err != nil {
		t.Fatalf("RemoveProtectionEmail: %v", err)
	}
	if _, err := c.RevokeBypassToken("p1", "s1", "a/b"); err != nil {
		t.Fatalf("RevokeBypassToken: %v", err)
	}
	want := []string{protectionBase + "/emails/..%2Fx", protectionBase + "/tokens/a%2Fb"}
	if len(paths) != 2 || paths[0] != want[0] || paths[1] != want[1] {
		t.Errorf("paths = %v; want %v", paths, want)
	}
}

func TestSite_DecodesProtectionFields(t *testing.T) {
	var on Site
	if err := json.Unmarshal([]byte(`{"id":"s1","protection_enabled":true,"protection_effective":true}`), &on); err != nil {
		t.Fatal(err)
	}
	if !on.ProtectionEnabled || !on.ProtectionEffective {
		t.Errorf("site = %+v; want both fields true", on)
	}

	var old Site
	if err := json.Unmarshal([]byte(`{"id":"s1"}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.ProtectionEnabled || old.ProtectionEffective {
		t.Errorf("site = %+v; an older server's site is unprotected", old)
	}
}

func TestProtection_OlderBackendIs404WithoutCode(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":"404 page not found"}`)
	}))
	defer ts.Close()

	_, err := newTestClient(ts.URL).GetProtection("p1", "s1")
	apiErr := asAPIError(t, err)
	if apiErr.Status != http.StatusNotFound || apiErr.Code != "" {
		t.Errorf("err = %+v; want a 404 with no code", apiErr)
	}
}
