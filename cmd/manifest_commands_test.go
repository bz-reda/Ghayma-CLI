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
	for _, want := range []string{"workspace manifest", "--site", "ghayma site use"} {
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
