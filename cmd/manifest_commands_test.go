package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The command-level tests below drive the REAL cobra tree against a stub API,
// because the manifest work is mostly wiring: which site id ends up in the
// request path, which file gets rewritten, what the user is told. Unit-testing
// the helpers alone would have missed every bug this feature can produce
// (2026-08-16).

// cliHome points the CLI config at a temp home holding a logged-in session, so
// config.Load() finds a token and the stub API host. USERPROFILE goes with HOME
// or the Windows CI job breaks (see TestHomeFixturesAlsoSetUSERPROFILE).
func cliHome(t *testing.T, apiHost string) {
	t.Helper()
	home := t.TempDir()
	cfg := `{"api_host":"` + apiHost + `","token":"test-token","user_id":"u1","email":"dev@example.com"}`
	if err := os.WriteFile(filepath.Join(home, ".paas-cli.json"), []byte(cfg), 0600); err != nil {
		t.Fatalf("write cli config: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

// resetCommandFlags clears the flag state these tests set. Cobra binds flags to
// package-level vars that outlive one Execute, so a --site from one test would
// otherwise leak into the next.
func resetCommandFlags() {
	deploySite, deployProd = "", false
	envSite, domainSite = "", ""
}

// runCLI executes `ghayma <args...>` in dir and returns everything it printed.
// Commands report failures on stdout rather than returning errors, so the
// output IS the assertion surface.
func runCLI(t *testing.T, dir string, args ...string) string {
	t.Helper()
	t.Chdir(dir)

	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		out, _ := io.ReadAll(r)
		done <- string(out)
	}()

	rootCmd.SetArgs(args)
	execErr := rootCmd.Execute()

	w.Close()
	os.Stdout = orig
	out := <-done
	r.Close()

	rootCmd.SetArgs(nil)
	resetCommandFlags()

	if execErr != nil {
		out += "\nexecute error: " + execErr.Error()
	}
	return out
}

// sitesStub answers the site listing (and records every path it served) so the
// tests can assert which site id a command addressed.
func sitesStub(t *testing.T, sites string) (*httptest.Server, *[]string) {
	t.Helper()
	var paths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/env"):
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"env_vars":{"API_URL":"https://example.test"},"build_time_keys":[]}`)
		case strings.HasSuffix(r.URL.Path, "/sites"):
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, sites)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)
	return ts, &paths
}

// manifestFixture materializes the two-site workspace on disk.
func manifestFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"pnpm-workspace.yaml":                  realPnpmWorkspaceYAML,
		"apps/taarefni/package.json":           `{}`,
		"apps/taarefni/src/app/page.tsx":       "// deep",
		"apps/taarefni-admin/package.json":     `{}`,
		"apps/taarefni-admin/src/app/page.tsx": "// deep",
		projectConfigName:                      manifestJSON,
	})
	return root
}

// --- project-scoped commands ------------------------------------------------

// TestProjectScopedCommand_FromNestedDir: `site list` is about the PROJECT, so
// it must work from wherever the user happens to be inside a linked workspace —
// including a directory the manifest doesn't list at all.
func TestProjectScopedCommand_FromNestedDir(t *testing.T) {
	ts, paths := sitesStub(t, `[{"id":"s1","name":"main","slug":"taarefni","status":"running"},{"id":"s2","name":"admin","slug":"taarefni-admin","status":"running"}]`)
	cliHome(t, ts.URL)
	root := manifestFixture(t)
	writeFiles(t, root, map[string]string{"apps/newapp/package.json": `{}`})

	for _, dir := range []string{
		filepath.Join(root, "apps", "taarefni", "src", "app"),
		filepath.Join(root, "apps", "newapp"), // not in the manifest, same project
	} {
		out := runCLI(t, dir, "site", "list")
		if !strings.Contains(out, "taarefni-admin") {
			t.Errorf("site list from %s printed %q; want the project's sites", dir, out)
		}
	}
	for _, got := range *paths {
		if got != "/api/v1/projects/p1/sites" {
			t.Errorf("requested %q; want the workspace project's sites", got)
		}
	}
	if len(*paths) != 2 {
		t.Errorf("served %d requests; want one per run", len(*paths))
	}
}

// --- env --------------------------------------------------------------------

// TestEnvList_SiteFlagAtWorkspaceRoot: env is SITE-scoped, so at a workspace
// root it must address the site --site names — not the project-wide endpoint,
// which would write one app's variables onto whichever site the server picks.
func TestEnvList_SiteFlagAtWorkspaceRoot(t *testing.T) {
	ts, paths := sitesStub(t, `[]`)
	cliHome(t, ts.URL)
	root := manifestFixture(t)
	forceStdin(t, false)
	noPrompt(t)

	out := runCLI(t, root, "env", "list", "--site", "taarefni-admin")
	if !strings.Contains(out, "API_URL") {
		t.Errorf("env list printed %q; want the stub's variables", out)
	}
	if len(*paths) != 1 || !strings.Contains((*paths)[0], "/sites/s2/env") {
		t.Errorf("requested %v; want the admin site's env endpoint (site s2)", *paths)
	}
}

// TestEnvList_NoSiteAtWorkspaceRootIsRefused: several sites, no TTY, no --site
// ⇒ name the flag instead of guessing a site.
func TestEnvList_NoSiteAtWorkspaceRootIsRefused(t *testing.T) {
	ts, paths := sitesStub(t, `[]`)
	cliHome(t, ts.URL)
	root := manifestFixture(t)
	forceStdin(t, false)
	noPrompt(t)

	out := runCLI(t, root, "env", "list")
	if !strings.Contains(out, "--site") {
		t.Errorf("output %q should tell the user to pass --site", out)
	}
	if len(*paths) != 0 {
		t.Errorf("no request should have gone out, got %v", *paths)
	}
}

// TestEnvList_FromAppDirNeedsNoFlag: inside an app the directory pins the site,
// exactly as it did before the manifest existed.
func TestEnvList_FromAppDirNeedsNoFlag(t *testing.T) {
	ts, paths := sitesStub(t, `[]`)
	cliHome(t, ts.URL)
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"pnpm-workspace.yaml":                realPnpmWorkspaceYAML,
		"apps/taarefni/package.json":         `{}`,
		"apps/taarefni/" + projectConfigName: perAppJSON,
	})
	forceStdin(t, true)
	noPrompt(t)

	runCLI(t, filepath.Join(root, "apps", "taarefni"), "env", "list")
	if len(*paths) != 1 || !strings.Contains((*paths)[0], "/sites/s1/env") {
		t.Errorf("requested %v; want this directory's own site (s1), with no question asked", *paths)
	}
}

// --- domain create ----------------------------------------------------------

// TestDomainCreate_SiteFlagAtWorkspaceRoot: a domain is attached to ONE site,
// so --site has to reach the request body.
func TestDomainCreate_SiteFlagAtWorkspaceRoot(t *testing.T) {
	var body map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(ts.Close)
	cliHome(t, ts.URL)
	root := manifestFixture(t)
	forceStdin(t, false)
	noPrompt(t)

	runCLI(t, root, "domain", "create", "admin.example.com", "--site", "admin")
	if body["site_id"] != "s2" || body["project_id"] != "p1" {
		t.Errorf("posted %v; want the admin site of the workspace project", body)
	}
}

// --- site create ------------------------------------------------------------

// createSiteStub answers POST /sites with a freshly created site.
func createSiteStub(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/sites") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"id":"s3","name":"api","slug":"taarefni-api","status":"created"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// forceNewSiteDirPrompt substitutes the directory picker shown after a site is
// created and records what it was offered.
func forceNewSiteDirPrompt(t *testing.T, fn func(slug string, dirs []string) (int, error)) {
	t.Helper()
	orig := promptNewSiteDirFn
	t.Cleanup(func() { promptNewSiteDirFn = orig })
	promptNewSiteDirFn = fn
}

// TestSiteCreate_AppendsToManifest: a site created from a linked workspace is
// useless until the manifest says which directory builds it, so offer to map it
// right there. Everything already in the file must survive the rewrite.
func TestSiteCreate_AppendsToManifest(t *testing.T) {
	cliHome(t, createSiteStub(t).URL)
	root := manifestFixture(t)
	writeFiles(t, root, map[string]string{"apps/taarefni-api/package.json": `{}`})
	forceStdin(t, true)

	var offered []string
	forceNewSiteDirPrompt(t, func(slug string, dirs []string) (int, error) {
		offered = dirs
		for i, d := range dirs {
			if d == "apps/taarefni-api" {
				return i, nil
			}
		}
		t.Fatalf("apps/taarefni-api was not offered (got %v)", dirs)
		return 0, nil
	})

	out := runCLI(t, root, "site", "create", "api")
	if !strings.Contains(out, "apps/taarefni-api") {
		t.Errorf("output %q should confirm the directory it mapped", out)
	}
	for _, taken := range []string{"apps/taarefni", "apps/taarefni-admin"} {
		for _, got := range offered {
			if got == taken {
				t.Errorf("directory %s is already mapped and must not be offered", taken)
			}
		}
	}

	data, err := os.ReadFile(filepath.Join(root, projectConfigName))
	if err != nil {
		t.Fatal(err)
	}
	var manifest ProjectManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("manifest is no longer valid JSON: %v", err)
	}
	if manifest.ProjectID != "p1" || manifest.Name != "taarefni" || manifest.Slug != "taarefni" {
		t.Errorf("project fields = %+v; want them untouched", manifest)
	}
	if len(manifest.Sites) != 3 {
		t.Fatalf("got %d sites; want the two existing ones plus the new one", len(manifest.Sites))
	}
	if manifest.Sites[0].SiteID != "s1" || manifest.Sites[1].SiteID != "s2" {
		t.Errorf("existing entries changed: %+v", manifest.Sites[:2])
	}
	added := manifest.Sites[2]
	if added.SiteID != "s3" || added.SiteSlug != "taarefni-api" || added.SiteName != "api" {
		t.Errorf("appended entry = %+v; want the created site", added)
	}
	if added.RootDirectory != "apps/taarefni-api" {
		t.Errorf("root_directory = %q; want the chosen directory", added.RootDirectory)
	}
	if added.Upload != uploadModeApp {
		t.Errorf("upload = %q; want the mode its siblings use (%q)", added.Upload, uploadModeApp)
	}
}

// TestSiteCreate_ManifestSkippedWithoutTTY: no terminal ⇒ no picker, no guess,
// and the manifest is left exactly as it was.
func TestSiteCreate_ManifestSkippedWithoutTTY(t *testing.T) {
	cliHome(t, createSiteStub(t).URL)
	root := manifestFixture(t)
	writeFiles(t, root, map[string]string{"apps/taarefni-api/package.json": `{}`})
	forceStdin(t, false)
	forceNewSiteDirPrompt(t, func(slug string, dirs []string) (int, error) {
		t.Fatal("the directory picker must not run without a terminal")
		return 0, nil
	})

	before, err := os.ReadFile(filepath.Join(root, projectConfigName))
	if err != nil {
		t.Fatal(err)
	}
	out := runCLI(t, root, "site", "create", "api")
	if !strings.Contains(out, projectConfigName) || !strings.Contains(out, "non-interactive") {
		t.Errorf("output %q should say how to add the site by hand", out)
	}

	after, err := os.ReadFile(filepath.Join(root, projectConfigName))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("manifest was rewritten without a choice:\n%s", after)
	}
}

// TestSiteCreate_PerAppConfigIsNotTouched: outside a workspace manifest the
// command keeps its old shape — nothing to map, nothing to rewrite.
func TestSiteCreate_PerAppConfigIsNotTouched(t *testing.T) {
	cliHome(t, createSiteStub(t).URL)
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{projectConfigName: perAppJSON})
	forceStdin(t, true)
	forceNewSiteDirPrompt(t, func(slug string, dirs []string) (int, error) {
		t.Fatal("a per-app config has no manifest to append to")
		return 0, nil
	})

	out := runCLI(t, dir, "site", "create", "api")
	if !strings.Contains(out, "created") {
		t.Errorf("output %q should report the created site", out)
	}
	data, _ := os.ReadFile(filepath.Join(dir, projectConfigName))
	if string(data) != perAppJSON {
		t.Errorf("per-app config was rewritten:\n%s", data)
	}
}

// --- site use ---------------------------------------------------------------

// perAppWithBuildConfig is a per-app config carrying the keys `site use` used to
// destroy: it round-tripped through the narrow ProjectConfig struct, which has
// no dockerfile_path and no crons, so switching sites silently deleted a
// project's custom Dockerfile and its whole cron schedule (2026-08-16).
const perAppWithBuildConfig = `{
  "project_id": "p1",
  "name": "taarefni",
  "slug": "taarefni",
  "framework": "nextjs",
  "site_id": "s1",
  "site_name": "main",
  "site_slug": "taarefni",
  "dockerfile_path": "docker/Dockerfile.web",
  "build_command": "pnpm build",
  "crons": [{"name":"nightly","schedule":"0 3 * * *","command":"node scripts/cron.js"}]
}`

func TestSiteUse_KeepsDockerfileAndCrons(t *testing.T) {
	ts, _ := sitesStub(t, `[{"id":"s2","name":"admin","slug":"taarefni-admin","status":"running"}]`)
	cliHome(t, ts.URL)

	dir := t.TempDir()
	// Legacy filename on purpose: switching sites must not migrate the project
	// onto .ghayma.json and strand teammates on the old CLI.
	cfgPath := filepath.Join(dir, legacyProjectConfigName)
	if err := os.WriteFile(cfgPath, []byte(perAppWithBuildConfig), 0644); err != nil {
		t.Fatal(err)
	}

	out := runCLI(t, dir, "site", "use", "taarefni-admin")
	if !strings.Contains(out, "switched to") {
		t.Fatalf("site use did not switch: %s", out)
	}

	if _, err := os.Stat(filepath.Join(dir, projectConfigName)); err == nil {
		t.Error("site use must write back to the file it read, not create .ghayma.json")
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("rewritten config is not valid JSON: %v", err)
	}
	if got["dockerfile_path"] != "docker/Dockerfile.web" {
		t.Errorf("dockerfile_path = %v; want it preserved", got["dockerfile_path"])
	}
	if got["build_command"] != "pnpm build" {
		t.Errorf("build_command = %v; want it preserved", got["build_command"])
	}
	crons, ok := got["crons"].([]any)
	if !ok || len(crons) != 1 {
		t.Fatalf("crons = %v; want the one job preserved", got["crons"])
	}
	if job, _ := crons[0].(map[string]any); job["schedule"] != "0 3 * * *" {
		t.Errorf("cron job = %v; want it preserved verbatim", crons[0])
	}
	if got["site_id"] != "s2" || got["site_slug"] != "taarefni-admin" {
		t.Errorf("site keys = %v/%v; want the newly selected site", got["site_id"], got["site_slug"])
	}
}

// TestSiteUse_RefusesManifest: at a workspace root there is no single active
// site to pin, and rewriting the manifest through a per-app struct would delete
// every other site. The message has to say what to do instead.
func TestSiteUse_RefusesManifest(t *testing.T) {
	ts, _ := sitesStub(t, `[]`)
	cliHome(t, ts.URL)

	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"pnpm-workspace.yaml":              realPnpmWorkspaceYAML,
		"apps/taarefni/package.json":       `{}`,
		"apps/taarefni-admin/package.json": `{}`,
		projectConfigName:                  manifestJSON,
	})

	out := runCLI(t, root, "site", "use", "taarefni-admin")
	for _, want := range []string{"workspace manifest", "--site", "root_directory"} {
		if !strings.Contains(out, want) {
			t.Errorf("refusal %q should mention %q", out, want)
		}
	}
	if strings.Contains(out, "next release") {
		t.Error("the refusal is now the permanent behavior, not a placeholder")
	}
	data, err := os.ReadFile(filepath.Join(root, projectConfigName))
	if err != nil {
		t.Fatal(err)
	}
	if !isManifest(data) {
		t.Fatal("the manifest must be left untouched")
	}
}

// manifestJSON is the two-site workspace the command tests run against.
const manifestJSON = `{
  "project_id": "p1",
  "name": "taarefni",
  "slug": "taarefni",
  "sites": [
    {"site_id":"s1","site_name":"main","site_slug":"taarefni","root_directory":"apps/taarefni","upload":"app"},
    {"site_id":"s2","site_name":"admin","site_slug":"taarefni-admin","root_directory":"apps/taarefni-admin","upload":"app"}
  ]
}`

// TestSiteUse_InManifestMappedDirExplains: inside an app directory that has no
// per-app file but is mapped by the workspace manifest, `site use` must say the
// manifest already decides this directory's site — not "run 'ghayma init'",
// which is where the root refusal's advice used to dead-end (2026-08-16 review).
func TestSiteUse_InManifestMappedDirExplains(t *testing.T) {
	ts, _ := sitesStub(t, `[]`)
	cliHome(t, ts.URL)
	root := manifestFixture(t)

	out := runCLI(t, filepath.Join(root, "apps", "taarefni-admin", "src"), "site", "use", "taarefni")
	for _, want := range []string{"taarefni-admin", "manifest", "root_directory"} {
		if !strings.Contains(out, want) {
			t.Errorf("site use in a manifest-mapped dir should mention %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "ghayma init") {
		t.Errorf("site use in a manifest-mapped dir must not send the user to 'ghayma init':\n%s", out)
	}
}

// TestFindProjectConfigUp_StopsAtRepoBoundary: a config ABOVE the repository
// belongs to some other project; the walk must not adopt it.
func TestFindProjectConfigUp_StopsAtRepoBoundary(t *testing.T) {
	outer := t.TempDir()
	writeFiles(t, outer, map[string]string{
		projectConfigName:            `{"project_id":"someone-else"}`,
		"repo/.git/HEAD":             "ref: refs/heads/main",
		"repo/apps/web/package.json": `{}`,
	})
	if _, err := findProjectConfigUp(filepath.Join(outer, "repo", "apps", "web")); err == nil {
		t.Fatal("walk crossed the repo's .git boundary and adopted an unrelated project's config")
	}
	// Inside the repo the walk still works.
	writeFiles(t, outer, map[string]string{"repo/" + projectConfigName: manifestJSON})
	if got, err := findProjectConfigUp(filepath.Join(outer, "repo", "apps", "web")); err != nil || !strings.HasSuffix(got, filepath.Join("repo", projectConfigName)) {
		t.Fatalf("expected the repo's own manifest, got %q, %v", got, err)
	}
}
