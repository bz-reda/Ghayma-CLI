package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFindProjectConfig covers the dual-read resolution order: the new
// .ghayma.json wins, .espacetech.json is the legacy fallback, and a missing
// config returns an os.IsNotExist-compatible error.
func TestFindProjectConfig(t *testing.T) {
	t.Run("legacy only resolves to .espacetech.json", func(t *testing.T) {
		dir := t.TempDir()
		legacy := filepath.Join(dir, legacyProjectConfigName)
		if err := os.WriteFile(legacy, []byte(`{"project_id":"p1"}`), 0644); err != nil {
			t.Fatal(err)
		}
		got, err := findProjectConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != legacy {
			t.Fatalf("expected %q, got %q", legacy, got)
		}
	})

	t.Run("new only resolves to .ghayma.json", func(t *testing.T) {
		dir := t.TempDir()
		newPath := filepath.Join(dir, projectConfigName)
		if err := os.WriteFile(newPath, []byte(`{"project_id":"p1"}`), 0644); err != nil {
			t.Fatal(err)
		}
		got, err := findProjectConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != newPath {
			t.Fatalf("expected %q, got %q", newPath, got)
		}
	})

	t.Run("both present prefers .ghayma.json", func(t *testing.T) {
		dir := t.TempDir()
		newPath := filepath.Join(dir, projectConfigName)
		legacy := filepath.Join(dir, legacyProjectConfigName)
		if err := os.WriteFile(newPath, []byte(`{"project_id":"new"}`), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(legacy, []byte(`{"project_id":"old"}`), 0644); err != nil {
			t.Fatal(err)
		}
		got, err := findProjectConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != newPath {
			t.Fatalf("expected new config %q to win, got %q", newPath, got)
		}
	})

	t.Run("neither present returns IsNotExist error", func(t *testing.T) {
		dir := t.TempDir()
		_, err := findProjectConfig(dir)
		if err == nil {
			t.Fatal("expected error when no config present")
		}
		if !os.IsNotExist(err) {
			t.Fatalf("expected os.IsNotExist-compatible error, got %v", err)
		}
	})
}

// TestProjectConfigWritePath pins that new projects always write .ghayma.json.
func TestProjectConfigWritePath(t *testing.T) {
	dir := t.TempDir()
	got := projectConfigWritePath(dir)
	if !strings.HasSuffix(got, projectConfigName) {
		t.Fatalf("expected write path to end in %q, got %q", projectConfigName, got)
	}
	if filepath.Base(got) != ".ghayma.json" {
		t.Fatalf("expected basename .ghayma.json, got %q", filepath.Base(got))
	}
}

// TestFindProjectConfig_BackCompat is the customer-breaking gate: a project
// dir containing ONLY the legacy .espacetech.json must still resolve and load.
func TestFindProjectConfig_BackCompat(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, ".espacetech.json")
	if err := os.WriteFile(legacy, []byte(`{"project_id":"legacy-proj","name":"Legacy"}`), 0644); err != nil {
		t.Fatal(err)
	}

	path, err := findProjectConfig(dir)
	if err != nil {
		t.Fatalf("back-compat broken: legacy .espacetech.json did not resolve: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read resolved legacy config: %v", err)
	}
	if !strings.Contains(string(data), "legacy-proj") {
		t.Fatalf("resolved config did not contain expected contents: %s", data)
	}
}

// TestFindProjectConfigUp covers the upward lookup the project-scoped commands
// use. `ghayma db list` from apps/web/src is the same project as from the
// workspace root, and telling the user to cd first was the old behavior nobody
// wanted (2026-08-16). Both config shapes carry project_id, so either one ends
// the walk.
func TestFindProjectConfigUp(t *testing.T) {
	t.Run("a config in the directory itself wins", func(t *testing.T) {
		root := t.TempDir()
		writeFiles(t, root, map[string]string{
			projectConfigName:               `{"project_id":"root"}`,
			"apps/web/" + projectConfigName: `{"project_id":"app"}`,
			"apps/web/src/keep-the-dir.txt": "",
		})
		got, err := findProjectConfigUp(filepath.Join(root, "apps", "web"))
		if err != nil {
			t.Fatalf("findProjectConfigUp: %v", err)
		}
		if got != filepath.Join(root, "apps", "web", projectConfigName) {
			t.Errorf("got %q; want the config in the directory itself", got)
		}
	})

	t.Run("walks up to a per-app config", func(t *testing.T) {
		root := t.TempDir()
		writeFiles(t, root, map[string]string{
			"apps/web/" + legacyProjectConfigName: `{"project_id":"legacy-app"}`,
			"apps/web/src/app/page.tsx":           "// deep",
		})
		data, err := readProjectConfigUp(filepath.Join(root, "apps", "web", "src", "app"))
		if err != nil {
			t.Fatalf("readProjectConfigUp: %v", err)
		}
		if !strings.Contains(string(data), "legacy-app") {
			t.Errorf("read %s; want the ancestor per-app config (legacy name included)", data)
		}
	})

	t.Run("walks up to a workspace manifest", func(t *testing.T) {
		root := t.TempDir()
		writeFiles(t, root, map[string]string{
			projectConfigName:           `{"project_id":"p1","sites":[{"site_id":"s1","root_directory":"apps/web"}]}`,
			"apps/newapp/package.json":  `{}`,
			"apps/web/src/app/page.tsx": "// deep",
		})
		// An unlinked sibling directory still belongs to the workspace's project.
		data, err := readProjectConfigUp(filepath.Join(root, "apps", "newapp"))
		if err != nil {
			t.Fatalf("readProjectConfigUp: %v", err)
		}
		if !isManifest(data) || !strings.Contains(string(data), "p1") {
			t.Errorf("read %s; want the root manifest", data)
		}
	})

	t.Run("nothing anywhere is an IsNotExist error", func(t *testing.T) {
		root := t.TempDir()
		writeFiles(t, root, map[string]string{"apps/web/package.json": `{}`})
		_, err := findProjectConfigUp(filepath.Join(root, "apps", "web"))
		if err == nil {
			t.Fatal("expected an error when no config exists anywhere above")
		}
		if !os.IsNotExist(err) {
			t.Errorf("err = %v; want an os.IsNotExist-compatible error", err)
		}
	})
}
