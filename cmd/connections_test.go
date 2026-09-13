package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"paas-cli/internal/api"
)

// captureStdout runs fn with os.Stdout redirected and returns what it printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

// stubExit swaps exitFn for one that records the code without exiting.
func stubExit(t *testing.T) *int {
	t.Helper()
	code := -1
	old := exitFn
	exitFn = func(c int) { code = c }
	t.Cleanup(func() { exitFn = old })
	return &code
}

func TestFailfPrintsAndExits1(t *testing.T) {
	code := stubExit(t)
	out := captureStdout(t, func() { failf("boom %d", 7) })
	if out != "❌ boom 7\n" || *code != 1 {
		t.Errorf("failf printed %q and exited %d; want the ❌ line and exit 1", out, *code)
	}
}

func TestPrintConnections_EmptyAndJSON(t *testing.T) {
	out := captureStdout(t, func() { printConnections("shop", nil, false) })
	if !strings.Contains(out, "No connections yet") || !strings.Contains(out, "ghayma connect") {
		t.Errorf("empty listing must say so and hint at connect, got %q", out)
	}

	rows := []api.Connection{{SiteID: "s1", SiteSlug: "main", Kind: "database", ResourceID: "d1", ResourceName: "pg", Level: "connect", CreatedAt: "2026-09-13T00:00:00Z"}}
	out = captureStdout(t, func() { printConnections("shop", rows, true) })
	var decoded []map[string]string
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("--json must print a JSON array, got %q: %v", out, err)
	}
	if decoded[0]["site_slug"] != "main" || decoded[0]["resource_name"] != "pg" || decoded[0]["created_at"] == "" {
		t.Errorf("--json must mirror the API rows, got %v", decoded)
	}

	out = captureStdout(t, func() { printConnections("shop", nil, true) })
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("--json with no rows must print [], got %q", out)
	}

	out = captureStdout(t, func() { printConnections("shop", rows, false) })
	if !strings.Contains(out, "shop") || !strings.Contains(out, "SITE") || !strings.Contains(out, "pg") {
		t.Errorf("table listing must name the project and render the table, got %q", out)
	}
}
