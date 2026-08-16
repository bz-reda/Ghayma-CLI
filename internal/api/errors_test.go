package api

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"paas-cli/internal/config"
)

// jsonStatusServer answers every request with the given status and body.
func jsonStatusServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// authedReads are the client calls that used to decode straight into their
// result and drop both the HTTP status and the decode error on the floor.
var authedReads = map[string]func(*Client) error{
	"ListProjects": func(c *Client) error {
		projects, err := c.ListProjects()
		if err == nil && len(projects) > 0 {
			return nil
		}
		return err
	},
	"GetDeployment":            func(c *Client) error { _, err := c.GetDeployment("d1"); return err },
	"GetDeploymentLogs":        func(c *Client) error { _, err := c.GetDeploymentLogs("d1"); return err },
	"ListDomains":              func(c *Client) error { _, err := c.ListDomains("p1"); return err },
	"GetEnvVarsSnapshotBySite": func(c *Client) error { _, err := c.GetEnvVarsSnapshotBySite("p1", "s1"); return err },
	"ListDeployments":          func(c *Client) error { _, err := c.ListDeployments("p1"); return err },
	"ListDatabases":            func(c *Client) error { _, err := c.ListDatabases(); return err },
	"ListBuckets":              func(c *Client) error { _, err := c.ListBuckets(); return err },
	"ListAuthApps":             func(c *Client) error { _, err := c.ListAuthApps(); return err },
	"ListAuthAppUsers":         func(c *Client) error { _, _, err := c.ListAuthAppUsers("a1"); return err },
}

// TestListProjects_UnauthorizedIsErrUnauthorized is THE regression pin for the
// 2026-08-16 incident: the stored 7-day JWT expired, /api/v1/projects answered
// 401 {"error":"invalid token"}, and ListProjects returned (empty, nil) — so
// `ghayma link` told the user they owned no projects.
func TestListProjects_UnauthorizedIsErrUnauthorized(t *testing.T) {
	ts := jsonStatusServer(t, http.StatusUnauthorized, `{"error":"invalid token"}`)

	projects, err := newTestClient(ts.URL).ListProjects()
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v; want ErrUnauthorized", err)
	}
	if projects != nil {
		t.Errorf("projects = %+v; want nil on 401", projects)
	}
	if !strings.Contains(err.Error(), "ghayma login") {
		t.Errorf("error %q should tell the user how to recover", err)
	}
}

// TestAuthedReads_Map401 extends the pin to every read that shared the bug.
func TestAuthedReads_Map401(t *testing.T) {
	for _, body := range []string{
		`{"error":"invalid token"}`,     // expired/garbage JWT
		`{"error":"invalid API token"}`, // unknown PAT
		`{"error":"API token has expired"}`,
	} {
		ts := jsonStatusServer(t, http.StatusUnauthorized, body)
		client := newTestClient(ts.URL)
		for name, call := range authedReads {
			if err := call(client); !errors.Is(err, ErrUnauthorized) {
				t.Errorf("%s on 401 %s: err = %v; want ErrUnauthorized", name, body, err)
			}
		}
	}
}

// TestAuthedReads_SurfaceServerError proves a non-2xx never reads as an empty
// account: the server's own message comes back instead.
func TestAuthedReads_SurfaceServerError(t *testing.T) {
	ts := jsonStatusServer(t, http.StatusInternalServerError, `{"error":"boom"}`)
	client := newTestClient(ts.URL)

	projects, err := client.ListProjects()
	if err == nil {
		t.Fatal("ListProjects on 500: want an error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %q; want the server message", err)
	}
	if len(projects) != 0 {
		t.Errorf("projects = %+v; want none on 500", projects)
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %T; want *APIError", err)
	}
	if apiErr.Status != http.StatusInternalServerError {
		t.Errorf("APIError.Status = %d; want 500", apiErr.Status)
	}

	for name, call := range authedReads {
		if err := call(client); err == nil {
			t.Errorf("%s on 500: want an error, got nil", name)
		}
	}
}

// TestAPIError_FallsBackToStatus covers a non-2xx with no {"error":...} body.
func TestAPIError_FallsBackToStatus(t *testing.T) {
	ts := jsonStatusServer(t, http.StatusBadGateway, `<html>nginx</html>`)

	_, err := newTestClient(ts.URL).ListProjects()
	if err == nil {
		t.Fatal("want an error on 502")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("error = %q; want the HTTP status in the message", err)
	}
}

// TestListProjects_ParsesArray is the happy path: a 200 array still decodes.
func TestListProjects_ParsesArray(t *testing.T) {
	ts := jsonStatusServer(t, http.StatusOK,
		`[{"id":"p1","name":"Forge","slug":"forge","framework":"nextjs"},{"id":"p2","name":"Leadzy","slug":"leadzy"}]`)

	projects, err := newTestClient(ts.URL).ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 2 || projects[0].Slug != "forge" || projects[1].ID != "p2" {
		t.Errorf("projects = %+v; want the two parsed projects", projects)
	}
}

// TestListProjects_DecodeErrorSurfaces pins the other half of the swallow: a
// 200 whose body isn't the expected shape must be an error, not an empty list.
func TestListProjects_DecodeErrorSurfaces(t *testing.T) {
	ts := jsonStatusServer(t, http.StatusOK, `{"projects": "not-an-array"}`)

	if _, err := newTestClient(ts.URL).ListProjects(); err == nil {
		t.Fatal("want a decode error on a mis-shaped 200 body")
	}
}

// TestAuthRequest_SendsBearer pins which credential goes on the wire: the
// long-lived PAT when stored, the session JWT otherwise, and never the legacy
// users.api_token UUID (which the API rejects).
func TestAuthRequest_SendsBearer(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.Config
		want string
	}{
		{"api token preferred", config.Config{Token: "jwt-x", APIToken: "gh_x"}, "Bearer gh_x"},
		{"session jwt fallback", config.Config{Token: "jwt-x"}, "Bearer jwt-x"},
		{"legacy uuid ignored", config.Config{Token: "jwt-x", APIToken: "3f7c1e02-1b6d-4c8a-9a11-2f0e5d6b7c88"}, "Bearer jwt-x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Get("Authorization")
				io.WriteString(w, `[]`)
			}))
			defer ts.Close()

			cfg := tc.cfg
			cfg.APIHost = ts.URL
			if _, err := NewClient(&cfg).ListProjects(); err != nil {
				t.Fatalf("ListProjects: %v", err)
			}
			if got != tc.want {
				t.Errorf("Authorization = %q; want %q", got, tc.want)
			}
		})
	}
}

// TestDeployUpload_Bearer covers the upload path, which builds its own request
// and so needs the same credential choice and 401 mapping.
func TestDeployUpload_Bearer(t *testing.T) {
	var got string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"invalid token"}`)
	}))
	defer ts.Close()

	cfg := &config.Config{APIHost: ts.URL, Token: "jwt-x", APIToken: "gh_x"}
	_, err := NewClient(cfg).Deploy("p1", "s1", t.TempDir(), "msg", false, "", "", DeployBuildConfig{}, nil)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Deploy err = %v; want ErrUnauthorized", err)
	}
	if got != "Bearer gh_x" {
		t.Errorf("upload Authorization = %q; want Bearer gh_x", got)
	}
}
