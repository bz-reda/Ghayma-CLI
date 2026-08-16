package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHomeFixturesAlsoSetUSERPROFILE pins the Windows-safe fixture shape:
// os.UserHomeDir reads USERPROFILE there, so any test that redirects HOME
// must redirect USERPROFILE too. A HOME-only fixture kept the Windows CI job
// red on every main run for six weeks (2026-07-01 → 2026-08-16).
func TestHomeFixturesAlsoSetUSERPROFILE(t *testing.T) {
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
		if strings.Contains(s, `Setenv("HOME"`) && !strings.Contains(s, `Setenv("USERPROFILE"`) {
			t.Errorf("%s redirects HOME without USERPROFILE — the fixture is not Windows-safe", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
