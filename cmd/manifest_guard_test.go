package cmd

import (
	"strings"
	"testing"
)

// TestRejectManifest pins the interim guard: a workspace manifest is refused by
// commands that read one site_id straight from the file, a per-app config is
// not. Without it `site use` at a linked root would rewrite the manifest as a
// per-app file and lose every other site (2026-08-16 review catch).
func TestRejectManifest(t *testing.T) {
	manifest := []byte(`{"project_id":"p","sites":[{"site_id":"a","root_directory":"apps/a"}]}`)
	perApp := []byte(`{"project_id":"p","site_id":"a"}`)

	if err := rejectManifest(perApp, "ghayma env"); err != nil {
		t.Fatalf("per-app config must pass: %v", err)
	}
	err := rejectManifest(manifest, "ghayma site use")
	if err == nil {
		t.Fatal("manifest must be refused")
	}
	for _, want := range []string{"workspace manifest", "ghayma site use", "app's own directory"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

// TestSiteScopedCommandsGuardManifests pins the wiring: env goes through
// localSiteConfig, and domain add / site use call rejectManifest before they
// touch site_id.
func TestSiteScopedCommandsGuardManifests(t *testing.T) {
	env := readCmdSource(t, "env.go")
	if strings.Contains(env, "projectID, siteID, _, err := localConfig()") || strings.Contains(env, "projectID, siteID, name, err := localConfig()") {
		t.Error("env.go must read its site through localSiteConfig, not localConfig")
	}
	if strings.Count(env, `localSiteConfig("ghayma env")`) < 4 {
		t.Error("env.go: all four env subcommands must use localSiteConfig")
	}
	for _, file := range []string{"domain.go", "site.go"} {
		if !strings.Contains(readCmdSource(t, file), "rejectManifest(data,") {
			t.Errorf("%s must guard against a workspace manifest before using site_id", file)
		}
	}
	site := readCmdSource(t, "site.go")
	if strings.Index(site, `rejectManifest(data, "ghayma site use")`) > strings.Index(site, "writeProjectConfigUpdate(") {
		t.Error("site use must reject a manifest before the write-back that would destroy it")
	}
}
