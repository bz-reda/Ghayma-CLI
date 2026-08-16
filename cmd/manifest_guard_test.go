package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestManifestHasNoActiveSite pins `site use`'s refusal: a workspace manifest
// is turned away, a per-app config is not. Without it `site use` at a linked
// root would rewrite the manifest as a per-app file and lose every other site
// (2026-08-16 review catch).
func TestManifestHasNoActiveSite(t *testing.T) {
	manifest := []byte(`{"project_id":"p","sites":[{"site_id":"a","root_directory":"apps/a"}]}`)
	perApp := []byte(`{"project_id":"p","site_id":"a"}`)

	if err := manifestHasNoActiveSite(perApp); err != nil {
		t.Fatalf("per-app config must pass: %v", err)
	}
	err := manifestHasNoActiveSite(manifest)
	if err == nil {
		t.Fatal("manifest must be refused")
	}
	for _, want := range []string{"workspace manifest", "--site", "ghayma site use"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

// TestSiteScopedCommandsUseTheResolver pins the wiring the interim guards stood
// in for: env's four subcommands and domain create resolve their site through
// resolveSiteContext (which is what makes --site work), and `site use` still
// refuses a manifest BEFORE the write-back that would destroy it.
func TestSiteScopedCommandsUseTheResolver(t *testing.T) {
	env := readCmdSource(t, "env.go")
	if strings.Count(env, "envSiteContext(") < 5 {
		t.Error("env.go: all four subcommands (plus the helper) must resolve the site through envSiteContext")
	}
	if !strings.Contains(env, "resolveSiteContext(") {
		t.Error("env.go must resolve its site through resolveSiteContext")
	}
	if !strings.Contains(readCmdSource(t, "domain.go"), "resolveSiteContext(") {
		t.Error("domain create must resolve its site through resolveSiteContext")
	}

	site := readCmdSource(t, "site.go")
	if !strings.Contains(site, "manifestHasNoActiveSite(data)") {
		t.Error("site use must refuse a workspace manifest before it touches site_id")
	}
	if strings.Index(site, "manifestHasNoActiveSite(data)") > strings.Index(site, "writeProjectConfigUpdate(") {
		t.Error("site use must reject a manifest before the write-back that would destroy it")
	}

	// The interim guard is gone: every site-scoped command now takes --site.
	for _, file := range []string{"env.go", "domain.go", "site.go", "manifest.go"} {
		if strings.Contains(readCmdSource(t, file), "rejectManifest(") {
			t.Errorf("%s still calls the interim rejectManifest guard", file)
		}
	}
}

// TestProjectScopedCommandsReadUpward pins that project-scoped commands resolve
// their config with readProjectConfigUp. An exact-directory read makes them
// fail from apps/web/src of a linked workspace — the directory people work in.
//
// The exact-directory read stays legitimate in exactly three places, and each
// one means "a config for THIS directory": init/link's already-linked checks,
// `site use`'s write-back path, and step 1 of resolveSiteContext.
func TestProjectScopedCommandsReadUpward(t *testing.T) {
	exactDirIsCorrect := map[string]bool{
		"init.go":          true, // "already initialized?" is about this directory
		"link.go":          true, // same, plus it writes here
		"link_manifest.go": true, // reads each app dir explicitly
		"delete.go":        true, // deletes the config file it found here
		"manifest.go":      true, // resolver step 1: a config for THIS directory
		"site.go":          true, // `site use` reads the file it writes back
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src := readCmdSource(t, name)
		if strings.Contains(src, `readProjectConfig(".")`) {
			t.Errorf("%s reads the config from the current directory only — use readProjectConfigUp", name)
		}
		if exactDirIsCorrect[name] {
			continue
		}
		if strings.Contains(src, `findProjectConfig(".")`) {
			t.Errorf("%s resolves the config in the current directory only — use findProjectConfigUp", name)
		}
	}

	// Guard the guard: the allow-list must name real files.
	for name := range exactDirIsCorrect {
		if _, err := os.Stat(filepath.Join(".", name)); err != nil {
			t.Errorf("allow-list names %s, which no longer exists", name)
		}
	}
}
