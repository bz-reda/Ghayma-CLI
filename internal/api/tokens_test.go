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

	created, err := newTestClient(ts.URL).CreateAPIToken(CreateAPITokenInput{Name: "ghayma-cli@mac", Scope: "full", ExpiresInDays: 365})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/tokens" {
		t.Errorf("request = %s %s; want POST /api/v1/tokens", gotMethod, gotPath)
	}
	if _, sent := gotBody["project_ids"]; sent {
		t.Errorf("body = %v; project_ids must be omitted for an unrestricted token", gotBody)
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

	created, err := newTestClient(ts.URL).CreateAPIToken(CreateAPITokenInput{Name: "n", Scope: "full"})
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

	_, err := newTestClient(ts.URL).CreateAPIToken(CreateAPITokenInput{Name: "n", Scope: "full", ExpiresInDays: 365})
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
	var gotMethod, gotPath, gotQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.RawQuery
		io.WriteString(w, `[
		  {"id":"tok-1","name":"ghayma-cli@mac","token_prefix":"gh_dead","scope":"full","expires_at":"2027-08-16T10:00:00Z","created_at":"2026-08-16T10:00:00Z"},
		  {"id":"tok-2","name":"ci","token_prefix":"gh_beef","scope":"full","expires_at":null,"created_at":"2026-08-01T10:00:00Z"}
		]`)
	}))
	defer ts.Close()

	tokens, err := newTestClient(ts.URL).ListAPITokens(false)
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/api/v1/tokens" {
		t.Errorf("request = %s %s; want GET /api/v1/tokens", gotMethod, gotPath)
	}
	if gotQuery != "" {
		t.Errorf("query = %q; want none unless revoked tokens are asked for", gotQuery)
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

// TestCreateAPIToken_WithProjects pins the project restriction: project_ids go
// out as given (slug or id) and the resolved projects come back.
func TestCreateAPIToken_WithProjects(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"id":"tok-2","name":"ci","token":"gh_cafe","scope":"deploy,databases","expires_at":null,
		  "project_ids":["p-uuid-1"],"projects":[{"id":"p-uuid-1","slug":"shop"}]}`)
	}))
	defer ts.Close()

	created, err := newTestClient(ts.URL).CreateAPIToken(CreateAPITokenInput{
		Name: "ci", Scope: "deploy,databases", ExpiresInDays: 90, ProjectIDs: []string{"shop"},
	})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	ids, _ := gotBody["project_ids"].([]any)
	if len(ids) != 1 || ids[0] != "shop" {
		t.Errorf("body project_ids = %v; want [shop]", gotBody["project_ids"])
	}
	if created.Scope != "deploy,databases" {
		t.Errorf("Scope = %q; want deploy,databases", created.Scope)
	}
	if len(created.Projects) != 1 || created.Projects[0].Slug != "shop" || created.Projects[0].ID != "p-uuid-1" {
		t.Errorf("Projects = %+v; want the resolved shop project", created.Projects)
	}
	if len(created.ProjectIDs) != 1 || created.ProjectIDs[0] != "p-uuid-1" {
		t.Errorf("ProjectIDs = %v; want [p-uuid-1]", created.ProjectIDs)
	}
}

// TestCreateAPIToken_WiderThanCaller surfaces the subset refusal with its
// machine code, so the command can print the server's message verbatim.
func TestCreateAPIToken_WiderThanCaller(t *testing.T) {
	ts := jsonStatusServer(t, http.StatusForbidden, `{"error":"a token cannot grant a scope it does not hold","code":"scope_exceeds_token"}`)

	_, err := newTestClient(ts.URL).CreateAPIToken(CreateAPITokenInput{Name: "n", Scope: "full"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden || apiErr.Code != "scope_exceeds_token" {
		t.Fatalf("err = %#v; want *APIError{403, scope_exceeds_token}", err)
	}
	if apiErr.Message != "a token cannot grant a scope it does not hold" {
		t.Errorf("Message = %q; want the server's message", apiErr.Message)
	}
}

// TestListAPITokens_IncludeRevoked asks for revoked rows only when told to and
// decodes the lifecycle and restriction fields.
func TestListAPITokens_IncludeRevoked(t *testing.T) {
	var gotQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		io.WriteString(w, `[{"id":"tok-3","name":"old","token_prefix":"gh_0ld","scope":"deploy","expires_at":null,
		  "last_used_at":"2026-09-20T08:00:00Z","revoked_at":"2026-09-21T08:00:00Z","created_at":"2026-09-01T08:00:00Z",
		  "project_ids":["p1"],"projects":[{"id":"p1","slug":"shop"}]}]`)
	}))
	defer ts.Close()

	tokens, err := newTestClient(ts.URL).ListAPITokens(true)
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	if gotQuery != "include_revoked=1" {
		t.Errorf("query = %q; want include_revoked=1", gotQuery)
	}
	if len(tokens) != 1 {
		t.Fatalf("got %d tokens; want 1", len(tokens))
	}
	tok := tokens[0]
	if tok.RevokedAt == nil || tok.LastUsedAt == nil {
		t.Errorf("RevokedAt/LastUsedAt = %v/%v; want both set", tok.RevokedAt, tok.LastUsedAt)
	}
	if len(tok.Projects) != 1 || tok.Projects[0].Slug != "shop" || len(tok.ProjectIDs) != 1 {
		t.Errorf("projects = %+v / %v; want shop", tok.Projects, tok.ProjectIDs)
	}
}

// TestRotateAPIToken_RoundTrip pins POST /tokens/:id/rotate: the expiry goes
// out, the new secret comes back once.
func TestRotateAPIToken_RoundTrip(t *testing.T) {
	var (
		gotMethod, gotPath string
		gotBody            map[string]any
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		io.WriteString(w, `{"id":"tok-9","name":"ci","token":"gh_new","scope":"deploy","expires_at":"2026-12-23T00:00:00Z","project_ids":[],"projects":[]}`)
	}))
	defer ts.Close()

	rotated, err := newTestClient(ts.URL).RotateAPIToken("tok-1", 90)
	if err != nil {
		t.Fatalf("RotateAPIToken: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/tokens/tok-1/rotate" {
		t.Errorf("request = %s %s; want POST /api/v1/tokens/tok-1/rotate", gotMethod, gotPath)
	}
	if days, _ := gotBody["expires_in_days"].(float64); days != 90 {
		t.Errorf("body expires_in_days = %v; want 90", gotBody["expires_in_days"])
	}
	if rotated.ID != "tok-9" || rotated.Token != "gh_new" {
		t.Errorf("rotated = %+v; want tok-9/gh_new", rotated)
	}
}

// TestRotateAPIToken_Revoked surfaces the 409 a revoked token answers.
func TestRotateAPIToken_Revoked(t *testing.T) {
	ts := jsonStatusServer(t, http.StatusConflict, `{"error":"token is revoked","code":"token_revoked"}`)

	_, err := newTestClient(ts.URL).RotateAPIToken("tok-1", 90)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict || apiErr.Code != "token_revoked" {
		t.Errorf("err = %#v; want *APIError{409, token_revoked}", err)
	}
}
