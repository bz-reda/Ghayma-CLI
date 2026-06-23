package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteProjectConfigUpdate_InPlace is the regression gate for the
// silent-migration bug: `site use` is an update to an EXISTING project, so its
// write-back (writeProjectConfigUpdate) must target the same file it read. A
// legacy .espacetech.json project must stay on .espacetech.json — writing a new
// .ghayma.json would strand teammates still on the old CLI reading the stale
// legacy file.
func TestWriteProjectConfigUpdate_InPlace(t *testing.T) {
	t.Run("legacy project updates .espacetech.json in place", func(t *testing.T) {
		dir := t.TempDir()
		legacy := filepath.Join(dir, legacyProjectConfigName)
		newPath := filepath.Join(dir, projectConfigName)
		if err := os.WriteFile(legacy, []byte(`{"project_id":"legacy-proj","name":"Legacy"}`), 0644); err != nil {
			t.Fatal(err)
		}

		cfg := ProjectConfig{ProjectID: "legacy-proj", Name: "Legacy", SiteSlug: "admin", SiteID: "s1", SiteName: "Admin"}
		if err := writeProjectConfigUpdate(dir, cfg); err != nil {
			t.Fatalf("writeProjectConfigUpdate failed: %v", err)
		}

		// The new .ghayma.json must NOT have been created (no silent migration).
		if _, err := os.Stat(newPath); !os.IsNotExist(err) {
			t.Fatalf("silent migration: %s was created (err=%v)", projectConfigName, err)
		}

		// The legacy file must reflect the update.
		data, err := os.ReadFile(legacy)
		if err != nil {
			t.Fatalf("failed to read legacy config: %v", err)
		}
		var got ProjectConfig
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("legacy config not valid JSON: %v", err)
		}
		if got.SiteSlug != "admin" || got.SiteID != "s1" || got.SiteName != "Admin" {
			t.Fatalf("legacy config not updated in place: %+v", got)
		}
	})

	t.Run("ghayma project updates .ghayma.json in place", func(t *testing.T) {
		dir := t.TempDir()
		newPath := filepath.Join(dir, projectConfigName)
		legacy := filepath.Join(dir, legacyProjectConfigName)
		if err := os.WriteFile(newPath, []byte(`{"project_id":"gh-proj","name":"Gh"}`), 0644); err != nil {
			t.Fatal(err)
		}

		cfg := ProjectConfig{ProjectID: "gh-proj", Name: "Gh", SiteSlug: "api", SiteID: "s2", SiteName: "API"}
		if err := writeProjectConfigUpdate(dir, cfg); err != nil {
			t.Fatalf("writeProjectConfigUpdate failed: %v", err)
		}

		// No stray legacy file should appear.
		if _, err := os.Stat(legacy); !os.IsNotExist(err) {
			t.Fatalf("unexpected %s created (err=%v)", legacyProjectConfigName, err)
		}

		data, err := os.ReadFile(newPath)
		if err != nil {
			t.Fatalf("failed to read ghayma config: %v", err)
		}
		if !strings.Contains(string(data), `"site_slug": "api"`) {
			t.Fatalf("ghayma config not updated in place: %s", data)
		}
	})

	t.Run("missing config returns IsNotExist error", func(t *testing.T) {
		dir := t.TempDir()
		err := writeProjectConfigUpdate(dir, ProjectConfig{ProjectID: "x"})
		if err == nil {
			t.Fatal("expected error when no config present")
		}
		if !os.IsNotExist(err) {
			t.Fatalf("expected os.IsNotExist-compatible error, got %v", err)
		}
	})
}

// TestSiteUse_WritesInPlace_SourcePin guards against a regression where the
// `site use` write-back goes back to projectConfigWritePath (which always
// returns .ghayma.json and would silently migrate legacy projects). It pins
// that the command's write resolves the existing path (writeProjectConfigUpdate,
// which uses findProjectConfig) and does NOT call projectConfigWritePath.
func TestSiteUse_WritesInPlace_SourcePin(t *testing.T) {
	src, err := os.ReadFile("site.go")
	if err != nil {
		t.Fatalf("failed to read site.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "writeProjectConfigUpdate(") {
		t.Fatal("site use must write back via writeProjectConfigUpdate (update-in-place)")
	}
	if strings.Contains(s, "projectConfigWritePath(") {
		t.Fatal("site use must NOT use projectConfigWritePath — that silently migrates legacy projects to .ghayma.json")
	}
}
