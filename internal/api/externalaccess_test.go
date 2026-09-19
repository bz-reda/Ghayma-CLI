package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recorder serves one canned body and records the method, path and body of
// every request — the assertion surface for "which route did the client call".
func recorder(t *testing.T, status int, body string) (*httptest.Server, *[]string, *[]string) {
	t.Helper()
	var calls, bodies []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.Path)
		bodies = append(bodies, string(raw))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(ts.Close)
	return ts, &calls, &bodies
}

// TestExternalAccess_Routes pins every route of the family: the kind decides
// the path segment, and a database and a bucket must land on different ones.
func TestExternalAccess_Routes(t *testing.T) {
	cases := []struct {
		name string
		call func(*Client) error
		want string
	}{
		{"list database", func(c *Client) error {
			_, err := c.ListExternalAccess("p1", "database", "d1")
			return err
		}, "GET /api/v1/projects/p1/databases/d1/access"},
		{"list bucket", func(c *Client) error {
			_, err := c.ListExternalAccess("p1", "bucket", "b1")
			return err
		}, "GET /api/v1/projects/p1/buckets/b1/access"},
		{"create", func(c *Client) error {
			_, err := c.CreateExternalAccess("p1", "database", "d1", ExternalAccessRequest{Name: "metabase"})
			return err
		}, "POST /api/v1/projects/p1/databases/d1/access"},
		{"revoke", func(c *Client) error {
			return c.RevokeExternalAccess("p1", "bucket", "b1", "x1")
		}, "DELETE /api/v1/projects/p1/buckets/b1/access/x1"},
		{"rotate", func(c *Client) error {
			_, err := c.RotateExternalAccess("p1", "database", "d1", "x1")
			return err
		}, "POST /api/v1/projects/p1/databases/d1/access/x1/rotate"},
		{"allowlist", func(c *Client) error {
			_, err := c.SetExternalAccessAllowlist("p1", "database", "d1", "x1", []string{"10.0.0.0/8"})
			return err
		}, "PUT /api/v1/projects/p1/databases/d1/access/x1/allowlist"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// An empty object suits every envelope in the family: the listing
			// reads a missing "access" as no rows, the rest as no fields.
			ts, calls, _ := recorder(t, http.StatusOK, `{}`)
			if err := tc.call(newTestClient(ts.URL)); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if len(*calls) != 1 || (*calls)[0] != tc.want {
				t.Errorf("calls = %v; want %q", *calls, tc.want)
			}
		})
	}
}

// TestExternalAccess_AuthAppHasNoRoute: an auth app is not a kind here, so the
// client must refuse it rather than build a nonsense path and call it.
func TestExternalAccess_AuthAppHasNoRoute(t *testing.T) {
	ts, calls, _ := recorder(t, http.StatusOK, `{}`)
	client := newTestClient(ts.URL)

	if _, err := client.ListExternalAccess("p1", "auth_app", "a1"); err == nil || !strings.Contains(err.Error(), "no external access") {
		t.Errorf("list err = %v; want the refusal", err)
	}
	if err := client.RevokeExternalAccess("p1", "auth_app", "a1", "x1"); err == nil {
		t.Error("revoke must refuse an auth app")
	}
	if len(*calls) != 0 {
		t.Errorf("calls = %v; want none", *calls)
	}
}

func TestListExternalAccess_DecodesRowsAndEmptyIsNonNil(t *testing.T) {
	ts, _, _ := recorder(t, http.StatusOK, `{"access":[{"id":"x1","kind":"database","resource_id":"d1","name":"metabase","level":"read-only","allow_cidrs":["10.0.0.0/8"],"expires_at":"2026-12-01T00:00:00Z","created_at":"2026-09-19T10:00:00Z","active":true}]}`)
	rows, err := newTestClient(ts.URL).ListExternalAccess("p1", "database", "d1")
	if err != nil {
		t.Fatalf("ListExternalAccess: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "metabase" || rows[0].Level != "read-only" || !rows[0].Active {
		t.Fatalf("rows = %+v", rows)
	}
	if len(rows[0].AllowCIDRs) != 1 || rows[0].AllowCIDRs[0] != "10.0.0.0/8" {
		t.Errorf("allow_cidrs = %v", rows[0].AllowCIDRs)
	}
	if rows[0].ExpiresAt != "2026-12-01T00:00:00Z" {
		t.Errorf("expires_at = %q", rows[0].ExpiresAt)
	}

	empty, _, _ := recorder(t, http.StatusOK, `{}`)
	rows, err = newTestClient(empty.URL).ListExternalAccess("p1", "bucket", "b1")
	if err != nil || rows == nil || len(rows) != 0 {
		t.Errorf("rows, err = %v, %v; want an empty non-nil slice", rows, err)
	}
}

// TestCreateExternalAccess_BodyOmitsUnsetFields: an empty level, allowlist or
// expiry must NOT reach the server as a value — the server's own defaults (the
// kind's full level, any source, no expiry) are what an omitted field means.
func TestCreateExternalAccess_BodyOmitsUnsetFields(t *testing.T) {
	ts, _, bodies := recorder(t, http.StatusCreated, `{"access":{"id":"x1"},"credential":{"secret":"s"}}`)
	if _, err := newTestClient(ts.URL).CreateExternalAccess("p1", "database", "d1", ExternalAccessRequest{Name: "metabase"}); err != nil {
		t.Fatalf("CreateExternalAccess: %v", err)
	}
	body := (*bodies)[0]
	for _, absent := range []string{`"level"`, `"allow_cidrs"`, `"expires_at"`} {
		if strings.Contains(body, absent) {
			t.Errorf("body %s must not carry %s when it was not set", body, absent)
		}
	}
	if !strings.Contains(body, `"name":"metabase"`) {
		t.Errorf("body = %s; want the principal's name", body)
	}
}

func TestCreateExternalAccess_SendsEveryFieldAndReadsTheCredential(t *testing.T) {
	ts, _, bodies := recorder(t, http.StatusCreated, `{"access":{"id":"x1","name":"metabase","level":"read-only","allow_cidrs":["10.0.0.0/8"]},"credential":{"credential_ref":"c_1a2b3c4d","secret":"p4ssw0rd","uri":"postgresql://c_1a2b3c4d:p4ssw0rd@pg-d1.db.ghayma.tech:5432/db_x?sslmode=require"},"warning":"this credential is shown once and cannot be retrieved again"}`)

	out, err := newTestClient(ts.URL).CreateExternalAccess("p1", "database", "d1", ExternalAccessRequest{
		Name: "metabase", Level: "read-only", AllowCIDRs: []string{"10.0.0.0/8"}, ExpiresAt: "2026-12-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("CreateExternalAccess: %v", err)
	}

	var sent map[string]interface{}
	if err := json.Unmarshal([]byte((*bodies)[0]), &sent); err != nil {
		t.Fatalf("body %s: %v", (*bodies)[0], err)
	}
	if sent["level"] != "read-only" || sent["expires_at"] != "2026-12-01T00:00:00Z" {
		t.Errorf("body = %v", sent)
	}
	if cidrs, _ := sent["allow_cidrs"].([]interface{}); len(cidrs) != 1 || cidrs[0] != "10.0.0.0/8" {
		t.Errorf("body allow_cidrs = %v", sent["allow_cidrs"])
	}
	if out.Credential == nil || out.Credential.Secret != "p4ssw0rd" || out.Credential.Ref != "c_1a2b3c4d" {
		t.Fatalf("credential = %+v", out.Credential)
	}
	if !strings.HasPrefix(out.Credential.URI, "postgresql://") {
		t.Errorf("uri = %q", out.Credential.URI)
	}
	if out.Warning == "" || out.Access == nil || out.Access.ID != "x1" {
		t.Errorf("out = %+v; want the warning and the created row", out)
	}
}

// TestSetExternalAccessAllowlist_ClearSendsAnEmptyList: clearing is a `[]`, not
// an omitted key and not a null — the server replaces the whole list with what
// the body carries.
func TestSetExternalAccessAllowlist_ClearSendsAnEmptyList(t *testing.T) {
	ts, _, bodies := recorder(t, http.StatusOK, `{"access":{"id":"x1","allow_cidrs":[]}}`)
	row, err := newTestClient(ts.URL).SetExternalAccessAllowlist("p1", "database", "d1", "x1", nil)
	if err != nil {
		t.Fatalf("SetExternalAccessAllowlist: %v", err)
	}
	if got := strings.TrimSpace((*bodies)[0]); got != `{"allow_cidrs":[]}` {
		t.Errorf("body = %s; want an explicit empty list", got)
	}
	if len(row.AllowCIDRs) != 0 {
		t.Errorf("row = %+v; want a cleared allowlist", row)
	}
}

func TestRevokeExternalAccess_Accepts204(t *testing.T) {
	ts, _, _ := recorder(t, http.StatusNoContent, "")
	if err := newTestClient(ts.URL).RevokeExternalAccess("p1", "database", "d1", "x1"); err != nil {
		t.Errorf("a 204 must be success, got %v", err)
	}
}

// TestExternalAccess_CarriesStatusAndMessage: each command renders its own
// sentence per status, so both have to survive the client.
func TestExternalAccess_CarriesStatusAndMessage(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   string
	}{
		{http.StatusBadRequest, `{"error":"invalid CIDR: \"10.0.0.0/8x\""}`, `invalid CIDR: "10.0.0.0/8x"`},
		{http.StatusConflict, `{"error":"this external access has been revoked: x1"}`, "this external access has been revoked: x1"},
		{http.StatusServiceUnavailable, `{"error":"could not mint the credential; no external access was created"}`, "could not mint the credential; no external access was created"},
		{http.StatusNotFound, `{"error":"external access not found"}`, "external access not found"},
	} {
		ts, _, _ := recorder(t, tc.status, tc.body)
		_, err := newTestClient(ts.URL).CreateExternalAccess("p1", "database", "d1", ExternalAccessRequest{Name: "x"})
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("%d: err = %v (%T); want *APIError", tc.status, err, err)
		}
		if apiErr.Status != tc.status || apiErr.Message != tc.want {
			t.Errorf("%d: APIError = %+v; want status %d and %q", tc.status, apiErr, tc.status, tc.want)
		}
	}
}
