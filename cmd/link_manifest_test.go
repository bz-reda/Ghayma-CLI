package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"paas-cli/internal/api"
)

// TestAutoMapSites pins the mapping priorities used when linking a whole
// workspace: an app directory already carrying a per-app config for a site is
// the strongest signal (the user linked it themselves), a matching directory
// basename is the next, and anything left over is asked about rather than
// guessed. A directory can back at most one site.
func TestAutoMapSites(t *testing.T) {
	sites := []api.Site{
		{ID: "s1", Name: "main", Slug: "taarefni"},
		{ID: "s2", Name: "admin", Slug: "taarefni-admin"},
		{ID: "s3", Name: "planify", Slug: "planify"},
		{ID: "s4", Name: "orphan", Slug: "orphan"},
	}
	dirs := []string{"apps/planify", "apps/planify-admin", "apps/taarefni", "apps/taarefni-admin"}
	existing := map[string]perAppConfig{
		"apps/taarefni":      {SiteEntry: SiteEntry{SiteID: "s1"}},
		"apps/planify-admin": {SiteEntry: SiteEntry{SiteID: "s2"}},
	}

	mapped, unmatched, remaining := autoMapSites(sites, dirs, existing)

	want := map[string]string{
		"s1": "apps/taarefni",      // existing config
		"s2": "apps/planify-admin", // existing config beats the apps/taarefni-admin basename
		"s3": "apps/planify",       // basename == slug
	}
	for id, dir := range want {
		if mapped[id] != dir {
			t.Errorf("site %s mapped to %q; want %q", id, mapped[id], dir)
		}
	}
	if len(mapped) != len(want) {
		t.Errorf("mapped = %v; want exactly %v", mapped, want)
	}
	if len(unmatched) != 1 || unmatched[0].ID != "s4" {
		t.Errorf("unmatched = %+v; want just the orphan site", unmatched)
	}
	if strings.Join(remaining, ",") != "apps/taarefni-admin" {
		t.Errorf("remaining dirs = %v; want the one dir nothing claimed", remaining)
	}
}

// TestAutoMapSites_BasenameMatchesName: a site named "Admin" claims apps/admin
// even when its slug is namespaced by the project.
func TestAutoMapSites_BasenameMatchesName(t *testing.T) {
	sites := []api.Site{{ID: "s5", Name: "Admin", Slug: "taarefni-admin"}}
	mapped, unmatched, _ := autoMapSites(sites, []string{"apps/admin", "apps/web"}, nil)

	if mapped["s5"] != "apps/admin" {
		t.Errorf("site mapped to %q; want apps/admin (basename == site name)", mapped["s5"])
	}
	if len(unmatched) != 0 {
		t.Errorf("unmatched = %+v; want none", unmatched)
	}
}

// TestBuildWorkspaceManifest: the manifest lists one entry per mapped site,
// writes the upload mode EXPLICITLY (so the file shows what will be uploaded
// rather than relying on a fallback), and carries an app's existing build
// settings across so linking never silently drops config-as-code.
func TestBuildWorkspaceManifest(t *testing.T) {
	project := &api.Project{ID: "p1", Name: "taarefni", Slug: "taarefni"}
	sites := []api.Site{
		{ID: "s1", Name: "main", Slug: "taarefni"},
		{ID: "s2", Name: "admin", Slug: "taarefni-admin"},
		{ID: "s3", Name: "ghost", Slug: "ghost"},
	}
	mapped := map[string]string{"s1": "apps/taarefni", "s2": "apps/taarefni-admin"}
	existing := map[string]perAppConfig{
		"apps/taarefni": {
			ProjectID: "p1",
			SiteEntry: SiteEntry{
				SiteID:          "s1",
				Framework:       "nextjs",
				DockerfilePath:  "docker/Dockerfile",
				BuildCommand:    "pnpm build",
				InstallCommand:  "pnpm install",
				StartCommand:    "pnpm start",
				OutputDirectory: ".next",
				Port:            3000,
				Crons:           json.RawMessage(`[{"name":"nightly","schedule":"0 3 * * *"}]`),
			},
		},
	}

	manifest := buildWorkspaceManifest(project, sites, mapped, existing, uploadModeApp)

	if manifest.ProjectID != "p1" || manifest.Name != "taarefni" || manifest.Slug != "taarefni" {
		t.Errorf("manifest project fields = %+v", manifest)
	}
	if len(manifest.Sites) != 2 {
		t.Fatalf("got %d entries; want 2 (the unmapped site is left out)", len(manifest.Sites))
	}

	main := manifest.Sites[0]
	if main.SiteID != "s1" || main.SiteName != "main" || main.SiteSlug != "taarefni" || main.RootDirectory != "apps/taarefni" {
		t.Errorf("main entry = %+v; want the site identity plus its directory", main)
	}
	if main.Upload != uploadModeApp {
		t.Errorf("upload = %q; want it written explicitly as %q", main.Upload, uploadModeApp)
	}
	if main.Framework != "nextjs" || main.BuildCommand != "pnpm build" || main.InstallCommand != "pnpm install" ||
		main.StartCommand != "pnpm start" || main.OutputDirectory != ".next" || main.Port != 3000 ||
		main.DockerfilePath != "docker/Dockerfile" || !strings.Contains(string(main.Crons), "nightly") {
		t.Errorf("main entry lost per-app settings: %+v", main)
	}

	admin := manifest.Sites[1]
	if admin.SiteID != "s2" || admin.RootDirectory != "apps/taarefni-admin" || admin.Framework != "" {
		t.Errorf("admin entry = %+v; want identity + dir and no invented build settings", admin)
	}

	// The written file must read back as a manifest, and unset per-site keys
	// must stay out of it.
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !isManifest(data) {
		t.Error("the written file must be recognized as a manifest")
	}
	for _, want := range []string{`"sites"`, `"root_directory": "apps/taarefni"`, `"upload": "app"`, `"crons"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("manifest json missing %s:\n%s", want, data)
		}
	}
	if strings.Count(string(data), `"framework"`) != 1 {
		t.Errorf("framework should only appear for the app that set it:\n%s", data)
	}
}

// TestSiteDirChoiceLabels: the fallback picker offers the remaining
// directories and a skip, in that order — a site nobody can place is dropped
// from the manifest, never guessed into the wrong directory.
func TestSiteDirChoiceLabels(t *testing.T) {
	labels := siteDirChoiceLabels([]string{"apps/web", "apps/admin"})
	if len(labels) != 3 {
		t.Fatalf("got %d labels; want 2 dirs + skip", len(labels))
	}
	if labels[0] != "apps/web" || labels[1] != "apps/admin" {
		t.Errorf("labels = %v; want the dirs in order", labels)
	}
	if !strings.Contains(labels[2], "skip") {
		t.Errorf("last label = %q; want the skip option", labels[2])
	}
}

// TestLinkModeLabels: the workspace-root question names both outcomes,
// including the file each one writes.
func TestLinkModeLabels(t *testing.T) {
	labels := linkModeLabels()
	if len(labels) != 2 {
		t.Fatalf("got %d labels; want 2", len(labels))
	}
	if !strings.Contains(labels[linkModeWholeProject], "Whole project") || !strings.Contains(labels[linkModeWholeProject], projectConfigName) {
		t.Errorf("whole-project label = %q", labels[linkModeWholeProject])
	}
	if !strings.Contains(labels[linkModeAppSubdir], "subdirectory") {
		t.Errorf("app-subdir label = %q", labels[linkModeAppSubdir])
	}
}

// TestLink_OffersWholeWorkspace pins the wiring: link asks the whole-project
// question at a workspace root, and asks it BEFORE the app-subdirectory prompt
// that used to be the only option there.
func TestLink_OffersWholeWorkspace(t *testing.T) {
	src := readCmdSource(t, "link.go")
	for _, want := range []string{"findWorkspaceRoot(", "linkWorkspaceRoot("} {
		if !strings.Contains(src, want) {
			t.Errorf("link.go should reference %q", want)
		}
	}
	if strings.Index(src, "linkWorkspaceRoot(") > strings.Index(src, "detectMonorepoAppSubdir(") {
		t.Error("the workspace-root branch must run before the app-subdirectory prompt")
	}
}
