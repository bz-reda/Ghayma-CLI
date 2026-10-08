package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type modeStub struct {
	mu    sync.Mutex
	patch map[string]any
	calls []string
}

func newModeStub(t *testing.T, row string, status int, reply string) (*httptest.Server, *modeStub) {
	t.Helper()
	s := &modeStub{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.calls = append(s.calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/databases":
			io.WriteString(w, `{"databases":[`+row+`]}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/databases/d1/valkey-mode":
			raw, _ := io.ReadAll(r.Body)
			json.Unmarshal(raw, &s.patch)
			w.WriteHeader(status)
			io.WriteString(w, reply)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)
	return ts, s
}

const valkeyRow = `{"id":"d1","name":"cache","type":"valkey","status":"running","valkey_mode":"cache"}`

func TestDBMode_SwitchesAndSaysItRestarts(t *testing.T) {
	ts, s := newModeStub(t, valkeyRow, 200, `{"database":{"id":"d1","name":"cache","type":"valkey","status":"running","valkey_mode":"store"}}`)
	cliHome(t, ts.URL)
	out := runCLI(t, linkedDir(t), "db", "mode", "cache", "store")
	if s.patch["mode"] != "store" {
		t.Fatalf("patch = %v", s.patch)
	}
	if !strings.Contains(out, "restart") || !strings.Contains(out, "store") || lastExitCode != 0 {
		t.Fatalf("exit=%d out=%s", lastExitCode, out)
	}
}

func TestDBMode_NoChangeAndWrongEngine(t *testing.T) {
	ts, s := newModeStub(t, valkeyRow+`,{"id":"d2","name":"pg","type":"postgres","status":"running"}`, 200, `{}`)
	cliHome(t, ts.URL)
	out := runCLI(t, linkedDir(t), "db", "mode", "cache", "cache")
	if s.patch != nil || !strings.Contains(out, "already") || lastExitCode != 0 {
		t.Fatalf("exit=%d patch=%v out=%s", lastExitCode, s.patch, out)
	}
	out = runCLI(t, linkedDir(t), "db", "mode", "pg", "store")
	if s.patch != nil || lastExitCode != 1 || !strings.Contains(out, "Valkey") {
		t.Fatalf("exit=%d out=%s", lastExitCode, out)
	}
	before := len(s.calls)
	out = runCLI(t, linkedDir(t), "db", "mode", "cache", "persist")
	if s.patch != nil || lastExitCode != 1 || !strings.Contains(out, "cache or store") {
		t.Fatalf("exit=%d out=%s", lastExitCode, out)
	}
	if len(s.calls) != before {
		t.Fatalf("a bad mode is refused before any request, got %v", s.calls[before:])
	}
}

// A reply without valkey_mode reports the mode that was asked for.
func TestDBMode_ReplyWithoutModeFallsBackToTheRequest(t *testing.T) {
	ts, _ := newModeStub(t, valkeyRow, 200, `{"database":{"id":"d1","name":"cache","type":"valkey","status":"running"}}`)
	cliHome(t, ts.URL)
	out := runCLI(t, linkedDir(t), "db", "mode", "cache", "store")
	if lastExitCode != 0 || !strings.Contains(out, "✅ cache is in store mode: never evicts") {
		t.Fatalf("exit=%d out=%s", lastExitCode, out)
	}
}

func TestDBMode_NotRunningIsReworded(t *testing.T) {
	ts, _ := newModeStub(t, valkeyRow, 409, `{"error":"the database must be running to change its disk","code":"database_not_running"}`)
	cliHome(t, ts.URL)
	out := runCLI(t, linkedDir(t), "db", "mode", "cache", "store")
	if lastExitCode != 1 || strings.Contains(out, "disk") || !strings.Contains(out, "ghayma db start cache") {
		t.Fatalf("exit=%d out=%s", lastExitCode, out)
	}
}
