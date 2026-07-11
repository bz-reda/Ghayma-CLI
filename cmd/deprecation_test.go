package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"paas-cli/internal/config"
)

// withFakeHome runs fn with HOME pointing at a temp dir so config read/write
// goes to a fresh location. Restores the original HOME on return.
func withFakeHome(t *testing.T, fn func(home string)) {
	t.Helper()
	dir := t.TempDir()
	orig := os.Getenv("HOME")
	if err := os.Setenv("HOME", dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Setenv("HOME", orig) })
	fn(dir)
}

// captureStderr runs fn with os.Stderr redirected to a buffer and returns
// whatever was written.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	done := make(chan []byte)
	go func() {
		var buf bytes.Buffer
		io.Copy(&buf, r)
		done <- buf.Bytes()
	}()
	fn()
	w.Close()
	os.Stderr = orig
	return string(<-done)
}

func TestMaybeWarnDeprecated_PrintsOnFirstCall(t *testing.T) {
	withFakeHome(t, func(home string) {
		out := captureStderr(t, func() {
			maybeWarnDeprecated("site add", "site create", "a future release")
		})
		if !strings.Contains(out, "[deprecation]") {
			t.Errorf("expected deprecation marker, got %q", out)
		}
		if !strings.Contains(out, "'site add' is deprecated") {
			t.Errorf("missing old command name: %q", out)
		}
		if !strings.Contains(out, "removed in a future release") {
			t.Errorf("missing removal version: %q", out)
		}
		if !strings.Contains(out, "use 'site create' instead") {
			t.Errorf("missing replacement suggestion: %q", out)
		}

		// Ensure state was persisted under the nested cli key.
		raw, err := os.ReadFile(filepath.Join(home, ".paas-cli.json"))
		if err != nil {
			t.Fatalf("config not written: %v", err)
		}
		if !strings.Contains(string(raw), `"cli"`) || !strings.Contains(string(raw), `"deprecation_notices"`) {
			t.Errorf("expected nested cli.deprecation_notices in config, got: %s", raw)
		}
		if !strings.Contains(string(raw), `"site.add"`) {
			t.Errorf("expected site.add notice id in config, got: %s", raw)
		}
	})
}

func TestMaybeWarnDeprecated_SilentWithinInterval(t *testing.T) {
	withFakeHome(t, func(home string) {
		first := captureStderr(t, func() {
			maybeWarnDeprecated("domain add", "domain create", "a future release")
		})
		if !strings.Contains(first, "[deprecation]") {
			t.Fatalf("first call should have printed, got %q", first)
		}

		second := captureStderr(t, func() {
			maybeWarnDeprecated("domain add", "domain create", "a future release")
		})
		if second != "" {
			t.Errorf("second call within interval should be silent, got %q", second)
		}
	})
}

func TestMaybeWarnDeprecated_PrintsAgainAfterInterval(t *testing.T) {
	withFakeHome(t, func(home string) {
		// Seed a notice timestamp 8 days ago — outside the 7-day interval.
		cfg := config.Load()
		cfg.CLI.DeprecationNotices = map[string]string{
			"env.remove": time.Now().Add(-8 * 24 * time.Hour).UTC().Format(time.RFC3339),
		}
		cfg.Save()

		out := captureStderr(t, func() {
			maybeWarnDeprecated("env remove", "env delete", "a future release")
		})
		if !strings.Contains(out, "[deprecation]") {
			t.Errorf("expected re-warning after interval, got %q", out)
		}
	})
}

func TestMaybeWarnDeprecated_DifferentNoticesIndependent(t *testing.T) {
	withFakeHome(t, func(home string) {
		// Seed "site.add" as recently shown.
		cfg := config.Load()
		cfg.CLI.DeprecationNotices = map[string]string{
			"site.add": time.Now().UTC().Format(time.RFC3339),
		}
		cfg.Save()

		// Warning about a different alias should still fire.
		out := captureStderr(t, func() {
			maybeWarnDeprecated("domain add", "domain create", "a future release")
		})
		if !strings.Contains(out, "[deprecation]") {
			t.Errorf("expected warning for unrelated notice id, got %q", out)
		}
	})
}
