package cmd

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// quietLogsSignals keeps `db logs` from installing a real Ctrl-C handler.
func quietLogsSignals(t *testing.T) {
	t.Helper()
	orig := dbLogsStopFn
	t.Cleanup(func() { dbLogsStopFn = orig })
	dbLogsStopFn = func(context.CancelFunc) func() { return func() {} }
}

func newLogsStub(t *testing.T, status int, body string, gotQuery *string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/databases":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"databases":[{"id":"d1","name":"main","type":"postgres","status":"running"}]}`)
		case "/api/v1/databases/d1/logs":
			*gotQuery = r.URL.RawQuery
			if status != 200 {
				w.Header().Set("Content-Type", "application/json")
			}
			w.WriteHeader(status)
			io.WriteString(w, body)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestDBLogs_PrintsTheTail(t *testing.T) {
	var q string
	quietLogsSignals(t)
	ts := newLogsStub(t, 200, "2026-10-08T09:00:00Z database system is ready\n", &q)
	cliHome(t, ts.URL)
	out := runCLI(t, linkedDir(t), "db", "logs", "main", "-n", "50")
	if q != "tail=50" || !strings.Contains(out, "database system is ready") {
		t.Fatalf("q=%q out=%s", q, out)
	}
	if strings.Contains(out, "stream ended") || lastExitCode != 0 {
		t.Fatalf("a plain tail is not a follow: exit=%d out=%s", lastExitCode, out)
	}
}

func TestDBLogs_ClampsLinesAndSendsFollow(t *testing.T) {
	var q string
	quietLogsSignals(t)
	ts := newLogsStub(t, 200, "", &q)
	cliHome(t, ts.URL)
	errOut := captureStderr(t, func() { runCLI(t, linkedDir(t), "db", "logs", "main", "-n", "5000", "-f") })
	if q != "follow=1&tail=1000" {
		t.Fatalf("q=%q", q)
	}
	if !strings.Contains(errOut, "Run the command again to keep following") || lastExitCode != 0 {
		t.Fatalf("a follow the server ended must say so on stderr: exit=%d stderr=%s", lastExitCode, errOut)
	}
}

func TestDBLogs_NoPod(t *testing.T) {
	var q string
	quietLogsSignals(t)
	ts := newLogsStub(t, 409, `{"error":"the database has no running pod","code":"no_pod"}`, &q)
	cliHome(t, ts.URL)
	out := runCLI(t, linkedDir(t), "db", "logs", "main")
	if lastExitCode != 1 || !strings.Contains(out, "not running yet") || !strings.Contains(out, "ghayma db info main") {
		t.Fatalf("exit=%d out=%s", lastExitCode, out)
	}
}

// Ctrl-C ends a follow quietly, before or after the stream opens: no failure,
// no "stream ended" note.
func TestDBLogs_InterruptEndsFollowQuietly(t *testing.T) {
	for _, opened := range []bool{false, true} {
		t.Run(map[bool]string{false: "before the stream", true: "mid-stream"}[opened], func(t *testing.T) {
			ready := make(chan struct{})
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/databases" {
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, `{"databases":[{"id":"d1","name":"main","type":"postgres","status":"running"}]}`)
					return
				}
				if opened {
					io.WriteString(w, "2026-10-08T09:00:00Z checkpoint complete\n")
					w.(http.Flusher).Flush()
				}
				close(ready)
				<-r.Context().Done()
			}))
			t.Cleanup(ts.Close)
			orig := dbLogsStopFn
			t.Cleanup(func() { dbLogsStopFn = orig })
			dbLogsStopFn = func(cancel context.CancelFunc) func() {
				go func() {
					<-ready
					if opened {
						// Let the client read the headers and the first line.
						time.Sleep(100 * time.Millisecond)
					}
					cancel()
				}()
				return func() {}
			}
			cliHome(t, ts.URL)
			var out string
			errOut := captureStderr(t, func() { out = runCLI(t, linkedDir(t), "db", "logs", "main", "-f") })
			if strings.Contains(out, "❌") || strings.Contains(out+errOut, "stream ended") || lastExitCode != 0 {
				t.Fatalf("exit=%d out=%s stderr=%s", lastExitCode, out, errOut)
			}
			if opened && !strings.Contains(out, "checkpoint complete") {
				t.Fatalf("the line sent before Ctrl-C must be printed: out=%q", out)
			}
		})
	}
}
