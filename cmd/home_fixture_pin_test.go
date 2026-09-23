package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHomeFixturesAlsoSetUSERPROFILE pins the hermetic fixture shape. Two
// things outrank a redirected HOME:
//
//   - USERPROFILE, which is what os.UserHomeDir reads on Windows. A HOME-only
//     fixture kept the Windows CI job red on every main run for six weeks
//     (2026-07-01 → 2026-08-16).
//   - GHAYMA_CONFIG and GHAYMA_API_HOST, which bypass the home directory
//     altogether. An operator with a staging login exported would otherwise
//     run the suite against their real config file and real host — today that
//     surfaces as "Please login first" cascading into a panic in cmd.
//
// So any test file that redirects HOME must neutralise all three.
func TestHomeFixturesAlsoSetUSERPROFILE(t *testing.T) {
	// Built rather than written out so this file does not match its own
	// check — it names the variables without redirecting anything.
	setenv := func(name string) string { return `Setenv("` + name + `"` }
	root := filepath.Join("..")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == "vendor" || strings.HasPrefix(name, ".") && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		s := string(src)
		if !strings.Contains(s, setenv("HOME")) {
			return nil
		}
		if !strings.Contains(s, setenv("USERPROFILE")) {
			t.Errorf("%s redirects HOME without USERPROFILE — the fixture is not Windows-safe", path)
		}
		for _, env := range []string{"GHAYMA_CONFIG", "GHAYMA_API_HOST"} {
			if !strings.Contains(s, setenv(env)) {
				t.Errorf("%s redirects HOME without clearing %s — the fixture is not hermetic, %s overrides HOME", path, env, env)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
