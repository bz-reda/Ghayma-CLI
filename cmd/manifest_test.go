package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFiles materializes a temp tree from forward-slash relative paths so the
// workspace fixtures below read like the repos they stand for (and stay
// Windows-safe on the CI matrix).
func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

// realPnpmWorkspaceYAML mirrors the shape of the user's actual workspace file:
// a quoted glob, a bare glob, a long `#` comment block between keys, and two
// further top-level keys. The minimal parser must survive all of it.
const realPnpmWorkspaceYAML = `packages:
  - 'apps/*'
  - packages/*

# Seven fixes to @react-pdf/textkit, all for Arabic.
#
# 1. reorderLine read the wrong map.
# 2. slice$1 began each slice at glyphIndexAt.
patchedDependencies:
  '@react-pdf/textkit@6.3.0': apps/taarefni/patches/textkit.patch

allowUnusedPatches: true
`

// forceStdin pins the TTY probe for a test so the resolver's interactive vs
// non-interactive branches are both reachable without a terminal.
func forceStdin(t *testing.T, tty bool) {
	t.Helper()
	orig := stdinIsTerminalFn
	t.Cleanup(func() { stdinIsTerminalFn = orig })
	stdinIsTerminalFn = func() bool { return tty }
}

// forceSitePrompt substitutes the manifest site picker and records its inputs.
func forceSitePrompt(t *testing.T, fn func(verb string, sites []SiteEntry) (int, error)) {
	t.Helper()
	orig := promptManifestSiteFn
	t.Cleanup(func() { promptManifestSiteFn = orig })
	promptManifestSiteFn = fn
}

// noPrompt fails the test if the picker is reached at all.
func noPrompt(t *testing.T) {
	t.Helper()
	forceSitePrompt(t, func(verb string, sites []SiteEntry) (int, error) {
		t.Fatalf("the site picker must not run here (verb %q, %d sites)", verb, len(sites))
		return 0, nil
	})
}

// TestIsManifest: the root manifest and a per-app config share a filename, so
// the shape probe is what keeps a per-app config from being read as a manifest
// (and vice versa). Only a `sites` ARRAY counts.
func TestIsManifest(t *testing.T) {
	cases := []struct {
		name string
		json string
		want bool
	}{
		{"per-app config", `{"project_id":"p1","name":"taarefni","site_slug":"main"}`, false},
		{"manifest", `{"project_id":"p1","sites":[{"site_slug":"main","root_directory":"apps/taarefni"}]}`, true},
		{"empty sites array", `{"project_id":"p1","sites":[]}`, true},
		{"sites object", `{"project_id":"p1","sites":{"main":"apps/x"}}`, false},
		{"sites string", `{"project_id":"p1","sites":"main"}`, false},
		{"sites null", `{"project_id":"p1","sites":null}`, false},
		{"not json", `nope`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isManifest([]byte(tc.json)); got != tc.want {
				t.Errorf("isManifest(%s) = %v; want %v", tc.json, got, tc.want)
			}
		})
	}
}

// TestFindWorkspaceRoot: a workspace root is turbo.json OR pnpm-workspace.yaml
// OR a package.json carrying `workspaces` — the pnpm-only repos that today's
// turbo-only detection cannot see are the whole point.
func TestFindWorkspaceRoot(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		found bool
	}{
		{"turbo", map[string]string{"turbo.json": `{}`, "apps/web/package.json": `{}`}, true},
		{"pnpm", map[string]string{"pnpm-workspace.yaml": realPnpmWorkspaceYAML, "apps/web/package.json": `{}`}, true},
		{"npm workspaces array", map[string]string{"package.json": `{"workspaces":["apps/*"]}`, "apps/web/package.json": `{}`}, true},
		{"npm workspaces object", map[string]string{"package.json": `{"workspaces":{"packages":["apps/*"]}}`, "apps/web/package.json": `{}`}, true},
		{"plain package.json", map[string]string{"package.json": `{"name":"solo"}`, "apps/web/package.json": `{}`}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFiles(t, root, tc.files)

			for _, from := range []string{root, filepath.Join(root, "apps", "web")} {
				got := findWorkspaceRoot(from)
				if tc.found && got != root {
					t.Errorf("findWorkspaceRoot(%s) = %q; want the workspace root %q", from, got, root)
				}
				if !tc.found && got == root {
					t.Errorf("findWorkspaceRoot(%s) = %q; want no workspace root", from, got)
				}
			}
		})
	}
}

// TestWorkspaceAppDirs_Pnpm: expand the pnpm globs, keep only real packages
// (a directory with a package.json), and return sorted forward-slash paths.
func TestWorkspaceAppDirs_Pnpm(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"pnpm-workspace.yaml":        realPnpmWorkspaceYAML,
		"package.json":               `{"name":"taarefni-workspace"}`,
		"apps/taarefni/package.json": `{}`,
		"apps/planify/package.json":  `{}`,
		"apps/scratch/README.md":     "no package.json here",
		"packages/ui/package.json":   `{}`,
		"docs/index.md":              "not a workspace member",
	})

	got := workspaceAppDirs(root)
	want := []string{"apps/planify", "apps/taarefni", "packages/ui"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("workspaceAppDirs = %v; want %v", got, want)
	}
}

// TestWorkspaceAppDirs_PackageJSON: npm/yarn style globs, plus the apps/* +
// packages/* fallback for a turbo repo that declares no globs anywhere.
func TestWorkspaceAppDirs_PackageJSON(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"package.json":              `{"workspaces":["services/*"]}`,
		"services/api/package.json": `{}`,
		"apps/web/package.json":     `{}`,
	})
	if got := workspaceAppDirs(root); strings.Join(got, ",") != "services/api" {
		t.Errorf("workspaceAppDirs = %v; want [services/api] (declared globs win)", got)
	}

	turbo := t.TempDir()
	writeFiles(t, turbo, map[string]string{
		"turbo.json":               `{"tasks":{}}`,
		"apps/web/package.json":    `{}`,
		"packages/ui/package.json": `{}`,
	})
	if got := workspaceAppDirs(turbo); strings.Join(got, ",") != "apps/web,packages/ui" {
		t.Errorf("workspaceAppDirs(turbo) = %v; want the apps/*, packages/* fallback", got)
	}
}

// TestDefaultUploadMode pins the behavior-preserving default (2026-08-16): a
// turbo repo already uploads the whole workspace today, a pnpm-only repo
// already uploads just the app dir. The manifest must not change either.
func TestDefaultUploadMode(t *testing.T) {
	turbo := t.TempDir()
	writeFiles(t, turbo, map[string]string{"turbo.json": `{}`, "pnpm-workspace.yaml": realPnpmWorkspaceYAML})
	if got := defaultUploadMode(turbo); got != uploadModeWorkspace {
		t.Errorf("defaultUploadMode(turbo) = %q; want %q", got, uploadModeWorkspace)
	}

	pnpm := t.TempDir()
	writeFiles(t, pnpm, map[string]string{"pnpm-workspace.yaml": realPnpmWorkspaceYAML})
	if got := defaultUploadMode(pnpm); got != uploadModeApp {
		t.Errorf("defaultUploadMode(pnpm) = %q; want %q", got, uploadModeApp)
	}
}

// --- resolveSiteContext -----------------------------------------------------

const perAppJSON = `{"project_id":"p1","name":"taarefni","slug":"taarefni","site_id":"s1","site_name":"main","site_slug":"taarefni","framework":"nextjs"}`

// TestResolveSiteContext_PerAppPnpmOnly: a pnpm-only repo keeps today's
// semantics exactly — the app directory is the upload root and no
// root_directory is sent. Widening monorepo detection here would silently
// switch these repos to whole-workspace uploads (2026-08-16).
func TestResolveSiteContext_PerAppPnpmOnly(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"pnpm-workspace.yaml":                realPnpmWorkspaceYAML,
		"apps/taarefni/package.json":         `{}`,
		"apps/taarefni/" + projectConfigName: perAppJSON,
	})
	noPrompt(t)

	appDir := filepath.Join(root, "apps", "taarefni")
	ctx, err := resolveSiteContext(appDir, "", "deploy")
	if err != nil {
		t.Fatalf("resolveSiteContext: %v", err)
	}
	if ctx.SourceDir != appDir {
		t.Errorf("SourceDir = %q; want the app dir %q", ctx.SourceDir, appDir)
	}
	if ctx.RootDirectory != "" {
		t.Errorf("RootDirectory = %q; want empty (pnpm-only repos upload the app dir)", ctx.RootDirectory)
	}
	if ctx.ProjectID != "p1" || ctx.Site.SiteID != "s1" || ctx.Site.Framework != "nextjs" {
		t.Errorf("ctx = %+v; want the per-app project/site/framework", ctx)
	}
	if ctx.FromManifest {
		t.Error("FromManifest must be false for a per-app config")
	}
}

// TestResolveSiteContext_PerAppTurbo: in a turbo repo the app dir config still
// uploads the workspace and sends root_directory — unchanged from today.
func TestResolveSiteContext_PerAppTurbo(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"turbo.json":                    `{}`,
		"apps/web/package.json":         `{}`,
		"apps/web/" + projectConfigName: perAppJSON,
	})
	noPrompt(t)

	appDir := filepath.Join(root, "apps", "web")
	ctx, err := resolveSiteContext(appDir, "", "deploy")
	if err != nil {
		t.Fatalf("resolveSiteContext: %v", err)
	}
	if ctx.SourceDir != root {
		t.Errorf("SourceDir = %q; want the turbo root %q", ctx.SourceDir, root)
	}
	if ctx.RootDirectory != "apps/web" {
		t.Errorf("RootDirectory = %q; want apps/web", ctx.RootDirectory)
	}
}

// TestResolveSiteContext_PerAppRootDirectoryFromConfig: a config that already
// carries root_directory keeps pointing at that subdir from where it lives.
func TestResolveSiteContext_PerAppRootDirectoryFromConfig(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		projectConfigName:       `{"project_id":"p1","name":"taarefni","root_directory":"apps/web"}`,
		"apps/web/package.json": `{}`,
	})
	noPrompt(t)

	ctx, err := resolveSiteContext(root, "", "deploy")
	if err != nil {
		t.Fatalf("resolveSiteContext: %v", err)
	}
	if ctx.SourceDir != root || ctx.RootDirectory != "apps/web" {
		t.Errorf("SourceDir/RootDirectory = %q/%q; want %q/apps/web", ctx.SourceDir, ctx.RootDirectory, root)
	}
	if !ctx.RootFromConfig {
		t.Error("RootFromConfig should be true so deploy keeps printing the '(from config)' headline")
	}
}

// manifestWorkspace writes a pnpm workspace with a root manifest listing two
// sites, both in the given upload mode.
func manifestWorkspace(t *testing.T, upload string) string {
	t.Helper()
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"pnpm-workspace.yaml":              realPnpmWorkspaceYAML,
		"apps/taarefni/package.json":       `{}`,
		"apps/taarefni-admin/package.json": `{}`,
		projectConfigName: `{"project_id":"p1","name":"taarefni","slug":"taarefni","sites":[
			{"site_id":"s1","site_name":"main","site_slug":"taarefni","root_directory":"apps/taarefni","upload":"` + upload + `","framework":"nextjs"},
			{"site_id":"s2","site_name":"admin","site_slug":"taarefni-admin","root_directory":"apps/taarefni-admin","upload":"` + upload + `"}
		]}`,
	})
	return root
}

// TestResolveSiteContext_ManifestSiteFlag: --site picks the entry, and the
// upload mode decides what gets tarred vs what gets built.
func TestResolveSiteContext_ManifestSiteFlag(t *testing.T) {
	t.Run("app mode uploads just the app dir", func(t *testing.T) {
		root := manifestWorkspace(t, uploadModeApp)
		noPrompt(t)

		ctx, err := resolveSiteContext(root, "taarefni-admin", "deploy")
		if err != nil {
			t.Fatalf("resolveSiteContext: %v", err)
		}
		wantDir := filepath.Join(root, "apps", "taarefni-admin")
		if ctx.SourceDir != wantDir || ctx.RootDirectory != "" {
			t.Errorf("SourceDir/RootDirectory = %q/%q; want %q/\"\"", ctx.SourceDir, ctx.RootDirectory, wantDir)
		}
		if ctx.Site.SiteID != "s2" || !ctx.FromManifest {
			t.Errorf("ctx = %+v; want site s2 from the manifest", ctx)
		}
	})

	t.Run("workspace mode uploads the root", func(t *testing.T) {
		root := manifestWorkspace(t, uploadModeWorkspace)
		noPrompt(t)

		ctx, err := resolveSiteContext(root, "admin", "deploy") // by site NAME
		if err != nil {
			t.Fatalf("resolveSiteContext: %v", err)
		}
		if ctx.SourceDir != root || ctx.RootDirectory != "apps/taarefni-admin" {
			t.Errorf("SourceDir/RootDirectory = %q/%q; want %q/apps/taarefni-admin", ctx.SourceDir, ctx.RootDirectory, root)
		}
	})
}

// TestResolveSiteContext_ManifestSingleSite: one site ⇒ no question asked.
func TestResolveSiteContext_ManifestSingleSite(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"pnpm-workspace.yaml":        realPnpmWorkspaceYAML,
		"apps/taarefni/package.json": `{}`,
		projectConfigName:            `{"project_id":"p1","name":"taarefni","sites":[{"site_id":"s1","site_slug":"taarefni","root_directory":"apps/taarefni","upload":"app"}]}`,
	})
	noPrompt(t)
	forceStdin(t, true)

	ctx, err := resolveSiteContext(root, "", "deploy")
	if err != nil {
		t.Fatalf("resolveSiteContext: %v", err)
	}
	if ctx.Site.SiteID != "s1" {
		t.Errorf("site = %q; want the only site s1", ctx.Site.SiteID)
	}
}

// TestResolveSiteContext_ManifestPrompts: several sites on a TTY ⇒ ask, with
// the calling verb in the label.
func TestResolveSiteContext_ManifestPrompts(t *testing.T) {
	root := manifestWorkspace(t, uploadModeApp)
	forceStdin(t, true)

	var gotVerb string
	var gotSites []SiteEntry
	forceSitePrompt(t, func(verb string, sites []SiteEntry) (int, error) {
		gotVerb, gotSites = verb, sites
		return 1, nil
	})

	ctx, err := resolveSiteContext(root, "", "deploy")
	if err != nil {
		t.Fatalf("resolveSiteContext: %v", err)
	}
	if gotVerb != "deploy" {
		t.Errorf("prompt verb = %q; want deploy", gotVerb)
	}
	if len(gotSites) != 2 {
		t.Fatalf("prompt got %d sites; want 2", len(gotSites))
	}
	if ctx.Site.SiteID != "s2" {
		t.Errorf("chosen site = %q; want the picked s2", ctx.Site.SiteID)
	}
}

// TestResolveSiteContext_ManifestNonInteractive: no TTY and no --site ⇒ never
// guess; name the slugs the user can pass.
func TestResolveSiteContext_ManifestNonInteractive(t *testing.T) {
	root := manifestWorkspace(t, uploadModeApp)
	forceStdin(t, false)
	noPrompt(t)

	_, err := resolveSiteContext(root, "", "deploy")
	if err == nil {
		t.Fatal("want an error telling the user to pass --site")
	}
	for _, want := range []string{"--site", "taarefni", "taarefni-admin"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

// TestResolveSiteContext_UnknownSiteFlag: an unknown --site lists what exists.
func TestResolveSiteContext_UnknownSiteFlag(t *testing.T) {
	root := manifestWorkspace(t, uploadModeApp)
	noPrompt(t)

	_, err := resolveSiteContext(root, "nope", "deploy")
	if err == nil {
		t.Fatal("want an error for an unknown --site")
	}
	if !strings.Contains(err.Error(), "nope") || !strings.Contains(err.Error(), "taarefni-admin") {
		t.Errorf("error %q should name the bad slug and the available ones", err)
	}
}

// TestResolveSiteContext_SubdirListedInManifest: running from an app dir that
// the root manifest lists resolves that site silently — same feel as today's
// per-app config.
func TestResolveSiteContext_SubdirListedInManifest(t *testing.T) {
	root := manifestWorkspace(t, uploadModeWorkspace)
	noPrompt(t)
	forceStdin(t, true)

	ctx, err := resolveSiteContext(filepath.Join(root, "apps", "taarefni-admin"), "", "deploy")
	if err != nil {
		t.Fatalf("resolveSiteContext: %v", err)
	}
	if ctx.Site.SiteID != "s2" {
		t.Errorf("site = %q; want s2 (the dir the manifest maps)", ctx.Site.SiteID)
	}
	if ctx.SourceDir != root || ctx.RootDirectory != "apps/taarefni-admin" {
		t.Errorf("SourceDir/RootDirectory = %q/%q; want %q/apps/taarefni-admin", ctx.SourceDir, ctx.RootDirectory, root)
	}
}

// TestResolveSiteContext_SubdirNotListed: an unlisted directory is an error,
// never a guess.
func TestResolveSiteContext_SubdirNotListed(t *testing.T) {
	root := manifestWorkspace(t, uploadModeApp)
	writeFiles(t, root, map[string]string{"apps/planify/package.json": `{}`})
	noPrompt(t)
	forceStdin(t, true)

	_, err := resolveSiteContext(filepath.Join(root, "apps", "planify"), "", "deploy")
	if err == nil {
		t.Fatal("want a 'not listed' error")
	}
	if !strings.Contains(err.Error(), "not listed") || !strings.Contains(err.Error(), "ghayma link") {
		t.Errorf("error %q should say the dir isn't listed and point at 'ghayma link'", err)
	}
}

// TestResolveSiteContext_PerAppSiteFlagMismatch: inside an app dir the
// directory pins the site; a --site for another one must not silently deploy
// the wrong app.
func TestResolveSiteContext_PerAppSiteFlagMismatch(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"pnpm-workspace.yaml":                realPnpmWorkspaceYAML,
		"apps/taarefni/package.json":         `{}`,
		"apps/taarefni/" + projectConfigName: perAppJSON,
	})
	noPrompt(t)
	appDir := filepath.Join(root, "apps", "taarefni")

	if _, err := resolveSiteContext(appDir, "admin", "deploy"); err == nil {
		t.Fatal("want an error for a --site that doesn't match this directory")
	} else if !strings.Contains(err.Error(), "linked to site") {
		t.Errorf("error = %q; want the 'linked to site' explanation", err)
	}

	// The site this directory IS linked to stays accepted (by slug and by name).
	for _, flag := range []string{"taarefni", "main", "s1"} {
		if _, err := resolveSiteContext(appDir, flag, "deploy"); err != nil {
			t.Errorf("--site %s should match this directory's own site: %v", flag, err)
		}
	}
}

// TestResolveSiteContext_NoConfigAnywhere: nothing to resolve ⇒
// errNoProjectConfig, which is what keeps deploy's legacy turbo scan-and-pick
// fallback reachable.
func TestResolveSiteContext_NoConfigAnywhere(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"apps/web/package.json": `{}`})
	noPrompt(t)

	if _, err := resolveSiteContext(filepath.Join(root, "apps", "web"), "", "deploy"); err == nil {
		t.Fatal("want errNoProjectConfig")
	} else if !errors.Is(err, errNoProjectConfig) {
		t.Errorf("err = %v; want errNoProjectConfig", err)
	}
}
