package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestCreateAPIToken_RoundTrip pins the mint call login makes: POST /tokens
// with name/scope/expires_in_days, and the raw gh_ token read back out of the
// 201 (the only time the server ever returns it).
func TestCreateAPIToken_RoundTrip(t *testing.T) {
	var (
		gotMethod, gotPath string
		gotBody            map[string]any
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"id":"tok-1","name":"ghayma-cli@mac","token":"gh_deadbeef","scope":"full","expires_at":"2027-08-16T10:00:00Z"}`)
	}))
	defer ts.Close()

	created, err := newTestClient(ts.URL).CreateAPIToken("ghayma-cli@mac", "full", 365)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/tokens" {
		t.Errorf("request = %s %s; want POST /api/v1/tokens", gotMethod, gotPath)
	}
	if gotBody["name"] != "ghayma-cli@mac" || gotBody["scope"] != "full" {
		t.Errorf("body = %v; want the name and scope", gotBody)
	}
	if days, _ := gotBody["expires_in_days"].(float64); days != 365 {
		t.Errorf("body expires_in_days = %v; want 365", gotBody["expires_in_days"])
	}
	if created.ID != "tok-1" || created.Token != "gh_deadbeef" || created.Name != "ghayma-cli@mac" {
		t.Errorf("created = %+v; want tok-1/gh_deadbeef", created)
	}
	if created.ExpiresAt == nil || !created.ExpiresAt.Equal(time.Date(2027, 8, 16, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("created.ExpiresAt = %v; want 2027-08-16T10:00:00Z", created.ExpiresAt)
	}
}

// TestCreateAPIToken_NullExpiry covers a never-expiring token: expires_at is
// null on the wire, so the field has to be a pointer.
func TestCreateAPIToken_NullExpiry(t *testing.T) {
	ts := jsonStatusServer(t, http.StatusCreated, `{"id":"tok-1","name":"n","token":"gh_x","expires_at":null}`)

	created, err := newTestClient(ts.URL).CreateAPIToken("n", "full", 0)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if created.ExpiresAt != nil {
		t.Errorf("ExpiresAt = %v; want nil for a null expiry", created.ExpiresAt)
	}
}

// TestCreateAPIToken_QuotaError pins the 10-token ceiling: the server's
// message must reach the user so login can fall back to the session.
func TestCreateAPIToken_QuotaError(t *testing.T) {
	ts := jsonStatusServer(t, http.StatusBadRequest, `{"error":"maximum 10 API tokens per account"}`)

	_, err := newTestClient(ts.URL).CreateAPIToken("n", "full", 365)
	if err == nil {
		t.Fatal("want an error when the token quota is full")
	}
	if !strings.Contains(err.Error(), "maximum 10 API tokens") {
		t.Errorf("error = %q; want the server message", err)
	}
}

// TestListAPITokens_RoundTrip pins the GET used to find and drop this
// machine's previous token before minting a new one.
func TestListAPITokens_RoundTrip(t *testing.T) {
	var gotMethod, gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		io.WriteString(w, `[
		  {"id":"tok-1","name":"ghayma-cli@mac","token_prefix":"gh_dead","scope":"full","expires_at":"2027-08-16T10:00:00Z","created_at":"2026-08-16T10:00:00Z"},
		  {"id":"tok-2","name":"ci","token_prefix":"gh_beef","scope":"full","expires_at":null,"created_at":"2026-08-01T10:00:00Z"}
		]`)
	}))
	defer ts.Close()

	tokens, err := newTestClient(ts.URL).ListAPITokens()
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/api/v1/tokens" {
		t.Errorf("request = %s %s; want GET /api/v1/tokens", gotMethod, gotPath)
	}
	if len(tokens) != 2 {
		t.Fatalf("got %d tokens; want 2", len(tokens))
	}
	if tokens[0].ID != "tok-1" || tokens[0].Name != "ghayma-cli@mac" || tokens[0].TokenPrefix != "gh_dead" {
		t.Errorf("tokens[0] = %+v; want the parsed cli token", tokens[0])
	}
	if tokens[1].ExpiresAt != nil {
		t.Errorf("tokens[1].ExpiresAt = %v; want nil", tokens[1].ExpiresAt)
	}
}

// TestDeleteAPIToken_RoundTrip pins the revoke logout performs.
func TestDeleteAPIToken_RoundTrip(t *testing.T) {
	var gotMethod, gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		io.WriteString(w, `{"message":"token revoked"}`)
	}))
	defer ts.Close()

	if err := newTestClient(ts.URL).DeleteAPIToken("tok-1"); err != nil {
		t.Fatalf("DeleteAPIToken: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/api/v1/tokens/tok-1" {
		t.Errorf("request = %s %s; want DELETE /api/v1/tokens/tok-1", gotMethod, gotPath)
	}
}

// TestDeleteAPIToken_NotFound surfaces a 404 so logout can treat an
// already-revoked token as "nothing to do" rather than a failure.
func TestDeleteAPIToken_NotFound(t *testing.T) {
	ts := jsonStatusServer(t, http.StatusNotFound, `{"error":"token not found"}`)

	err := newTestClient(ts.URL).DeleteAPIToken("gone")
	if err == nil {
		t.Fatal("want an error on 404")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Errorf("err = %v; want *APIError with status 404", err)
	}
}
