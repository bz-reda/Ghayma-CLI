package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"paas-cli/internal/api"
	"paas-cli/internal/config"
)

// rotateStub serves the site view and the rotate route, recording every
// "METHOD /path" it was asked for — the assertion surface for which request
// the command actually made. The rotate route answers with the given status.
func rotateStub(t *testing.T, status int, body string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/rotate") {
			w.WriteHeader(status)
			io.WriteString(w, body)
			return
		}
		io.WriteString(w, `{"connections":[
			{"site_id":"s1","site_slug":"main","kind":"database","resource_id":"d1","resource_name":"pg-main","level":"connect"},
			{"site_id":"s1","site_slug":"main","kind":"bucket","resource_id":"b1","resource_name":"uploads","level":"read-write"}],
			"available":[{"kind":"database","resource_id":"d2","resource_name":"pg-analytics","levels":["read-only","connect"]}]}`)
	}))
	t.Cleanup(ts.Close)
	return ts, &seen
}

func rotateTarget(apiHost string) (*api.Client, *connectionTarget) {
	client := api.NewClient(&config.Config{APIHost: apiHost, Token: "test-token"})
	return client, &connectionTarget{ProjectID: "p1", ProjectName: "shop", Site: api.Site{ID: "s1", Slug: "main"}}
}

// TestConnectionsRotate_AuthIsRefusedBeforeAnyRequest drives the real command
// tree from a directory with no project config: the refusal has to be the kind
// check, not a resolution failure, and the API must never be touched.
func TestConnectionsRotate_AuthIsRefusedBeforeAnyRequest(t *testing.T) {
	ts, seen := rotateStub(t, http.StatusNoContent, "")
	cliHome(t, ts.URL)

	out := runCLI(t, t.TempDir(), "connections", "rotate", "auth", "shop")

	if !strings.Contains(out, "database and bucket connections") || !strings.Contains(out, "no per-connection credential") {
		t.Errorf("output = %q; want the local refusal for an auth app", out)
	}
	if !strings.Contains(out, "ghayma auth rotate-keys shop") {
		t.Errorf("output = %q; want the command that does rotate an auth app's keys", out)
	}
	if strings.Contains(out, "project config") {
		t.Errorf("output = %q; the kind check must come before site resolution", out)
	}
	if len(*seen) != 0 {
		t.Errorf("requests = %v; want none before the refusal", *seen)
	}
	if lastExitCode != 1 {
		t.Errorf("exit code = %d; want 1", lastExitCode)
	}
}

// TestRotateConnection_PostsTheRotateRoute pins method and path for both kinds
// that have a per-connection credential.
func TestRotateConnection_PostsTheRotateRoute(t *testing.T) {
	cases := []struct {
		kind, name, want string
	}{
		{"database", "pg-main", "POST /api/v1/projects/p1/sites/s1/connections/database/d1/rotate"},
		{"bucket", "uploads", "POST /api/v1/projects/p1/sites/s1/connections/bucket/b1/rotate"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			code := stubExit(t)
			ts, seen := rotateStub(t, http.StatusNoContent, "")
			client, target := rotateTarget(ts.URL)

			out := captureStdout(t, func() {
				rotateConnection(client, target, tc.kind, tc.name, true, func() string {
					t.Fatal("--yes must not prompt")
					return ""
				})
			})

			if len(*seen) != 2 || (*seen)[1] != tc.want {
				t.Fatalf("requests = %v; want the site view then %q", *seen, tc.want)
			}
			if !strings.Contains(out, "✅ Rotated the credential of "+kindLabel(tc.kind)+" '"+tc.name+"' for 'main'") {
				t.Errorf("output = %q; want the success line", out)
			}
			if !strings.Contains(out, "ghayma env pull") {
				t.Errorf("output = %q; want the env pull hint", out)
			}
			if *code != -1 {
				t.Errorf("exit code = %d; want no exit on success", *code)
			}
		})
	}
}

// TestRotateConnection_DeclinedConfirmSendsNoRequest: the prompt is the last
// gate before a credential changes, so "n" must leave the server alone.
func TestRotateConnection_DeclinedConfirmSendsNoRequest(t *testing.T) {
	ts, seen := rotateStub(t, http.StatusNoContent, "")
	client, target := rotateTarget(ts.URL)

	out := captureStdout(t, func() {
		rotateConnection(client, target, "database", "pg-main", false, func() string { return "n" })
	})

	if !strings.Contains(out, "Cancelled") {
		t.Errorf("output = %q; want the cancellation", out)
	}
	for _, got := range *seen {
		if strings.HasSuffix(got, "/rotate") {
			t.Errorf("requests = %v; a declined confirm must not rotate", *seen)
		}
	}
}

// TestRotateConnection_NotConnectedIsNoChange: a resource the project has but
// this app does not hold is answered locally, the way disconnect does.
func TestRotateConnection_NotConnectedIsNoChange(t *testing.T) {
	ts, seen := rotateStub(t, http.StatusNoContent, "")
	client, target := rotateTarget(ts.URL)

	out := captureStdout(t, func() {
		rotateConnection(client, target, "database", "pg-analytics", true, readAnswer)
	})

	if !strings.Contains(out, "is not connected to 'main' — nothing to rotate") {
		t.Errorf("output = %q; want the no-change line", out)
	}
	if len(*seen) != 1 {
		t.Errorf("requests = %v; want only the site view", *seen)
	}
}

// TestRotateConnection_RendersServerRefusals covers the two answers this route
// has of its own: a connection with no credential to rotate, and an engine
// that cannot be reached right now.
func TestRotateConnection_RendersServerRefusals(t *testing.T) {
	t.Run("409 shared credential", func(t *testing.T) {
		code := stubExit(t)
		ts, _ := rotateStub(t, http.StatusConflict, `{"error":"connection uses the service credential"}`)
		client, target := rotateTarget(ts.URL)

		out := captureStdout(t, func() {
			rotateConnection(client, target, "database", "pg-main", true, readAnswer)
		})

		if !strings.Contains(out, "shared credential") || !strings.Contains(out, "ghayma db rotate pg-main") {
			t.Errorf("output = %q; want the shared-credential sentence and the db rotate hint", out)
		}
		if *code != 1 {
			t.Errorf("exit code = %d; want 1", *code)
		}
	})

	t.Run("503 engine unreachable", func(t *testing.T) {
		code := stubExit(t)
		ts, _ := rotateStub(t, http.StatusServiceUnavailable, `{"error":"database is stopped; retrying once it is running converges"}`)
		client, target := rotateTarget(ts.URL)

		out := captureStdout(t, func() {
			rotateConnection(client, target, "bucket", "uploads", true, readAnswer)
		})

		if !strings.Contains(out, "database is stopped; retrying once it is running converges") {
			t.Errorf("output = %q; want the server's own message", out)
		}
		if !strings.Contains(out, "run the same command again") {
			t.Errorf("output = %q; want the retry suggestion", out)
		}
		if *code != 1 {
			t.Errorf("exit code = %d; want 1", *code)
		}
	})
}

func TestRotateFailure_MapsStatusesAndFallsBack(t *testing.T) {
	got := rotateFailure(&api.APIError{Status: http.StatusConflict, Message: "shared"}, "bucket", "uploads")
	if !strings.Contains(got, "ghayma storage rotate uploads") {
		t.Errorf("409 for a bucket = %q; want the storage rotate hint", got)
	}
	got = rotateFailure(&api.APIError{Status: http.StatusServiceUnavailable}, "database", "pg-main")
	if strings.Contains(got, "HTTP 503") || !strings.Contains(got, "could not be reached") {
		t.Errorf("a 503 with no body = %q; want a sentence, not the bare status", got)
	}
	got = rotateFailure(&api.APIError{Status: http.StatusNotFound, Message: "not connected"}, "database", "pg-main")
	if !strings.Contains(got, "nothing to rotate") {
		t.Errorf("404 = %q", got)
	}
	got = rotateFailure(&api.APIError{Status: http.StatusInternalServerError, Message: "boom"}, "database", "pg-main")
	if !strings.Contains(got, "Failed to rotate: boom") {
		t.Errorf("an unmapped status = %q; want the server's message", got)
	}
}

func TestConfirmRotate_YesSkipsAndAnswersGate(t *testing.T) {
	if !confirmRotate("database", "pg", "main", true, func() string {
		t.Fatal("must not prompt with --yes")
		return ""
	}) {
		t.Error("--yes must confirm")
	}
	out := captureStdout(t, func() {
		if confirmRotate("database", "pg", "main", false, func() string { return "n" }) {
			t.Error("'n' must cancel")
		}
		if !confirmRotate("database", "pg", "main", false, func() string { return "Y" }) {
			t.Error("'Y' must confirm")
		}
	})
	for _, want := range []string{"pods restart", "ghayma env pull", "database 'pg'", "'main'"} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt = %q; must mention %q", out, want)
		}
	}
}
