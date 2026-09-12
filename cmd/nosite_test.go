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

	"paas-cli/internal/api"
)

// Projects without a site (2026-09-12). Two halves are tested here: init/link
// can now produce a site-less config, and every site-scoped command has to meet
// one with a single clean line and a non-zero exit instead of a wrong endpoint,
// a bogus "(unnamed)" error, or a nil deref.

// siteLessJSON is what init --no-site writes: project fields, no site keys.
const siteLessJSON = `{"project_id":"p1","name":"taarefni","slug":"taarefni","framework":"auto"}`

// legacyMainJSON is what init wrote for the lazily-created main site before
// this change: site_name without a site_id. It still means "the main site".
const legacyMainJSON = `{"project_id":"p1","name":"taarefni","slug":"taarefni","site_name":"main","framework":"nextjs"}`

// siteLessDir materializes a directory holding a site-less project config.
func siteLessDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{projectConfigName: siteLessJSON})
	return dir
}

// noRequestsStub fails the test if any API call goes out — the site-less
// answer is decided from the config, so nothing should reach the network.
func noRequestsStub(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected API call: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// --- config shape -----------------------------------------------------------

// TestHasSite pins the rule that separates the two states: only the fully empty
// triple means "no site". A legacy config carrying site_name "main" with no
// site_id still means the main site (D5) — reading it as site-less would send
// every one of those projects down the no-site path.
func TestHasSite(t *testing.T) {
	cases := []struct {
		name  string
		entry SiteEntry
		want  bool
	}{
		{"empty", SiteEntry{}, false},
		{"legacy lazy main", SiteEntry{SiteName: "main"}, true},
		{"id only", SiteEntry{SiteID: "s1"}, true},
		{"slug only", SiteEntry{SiteSlug: "taarefni"}, true},
		{"full", SiteEntry{SiteID: "s1", SiteName: "main", SiteSlug: "taarefni"}, true},
	}
	for _, c := range cases {
		if got := hasSite(c.entry); got != c.want {
			t.Errorf("hasSite(%s) = %v; want %v", c.name, got, c.want)
		}
	}
}

// --- init flags -------------------------------------------------------------

// TestValidateInitSiteFlags: --no-site and a site name / a domain are
// contradictory, and the refusal has to land before anything is created.
func TestValidateInitSiteFlags(t *testing.T) {
	if err := validateInitSiteFlags(false, "admin", "example.com"); err != nil {
		t.Errorf("without --no-site the other flags are fine, got %v", err)
	}
	if err := validateInitSiteFlags(true, "", ""); err != nil {
		t.Errorf("--no-site alone is fine, got %v", err)
	}
	for _, c := range []struct{ site, domain, want string }{
		{"admin", "", "--site"},
		{"", "example.com", "--domain"},
		{"admin", "example.com", "--site or --domain"},
	} {
		err := validateInitSiteFlags(true, c.site, c.domain)
		if err == nil {
			t.Fatalf("--no-site --site %q --domain %q must be refused", c.site, c.domain)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("error %q should name %q", err, c.want)
		}
	}
}

// TestResolveInitSiteMode: the flags answer the question without asking it;
// only a bare init reaches the picker.
func TestResolveInitSiteMode(t *testing.T) {
	asked := 0
	orig := promptSiteModeFn
	t.Cleanup(func() { promptSiteModeFn = orig })
	promptSiteModeFn = func() (initSiteMode, error) { asked++; return siteModeDeploy, nil }

	if mode, _ := resolveInitSiteMode(true, ""); mode != siteModeNoSite {
		t.Errorf("--no-site = %v; want siteModeNoSite", mode)
	}
	if mode, _ := resolveInitSiteMode(false, "admin"); mode != siteModeDeploy {
		t.Errorf("--site admin = %v; want siteModeDeploy", mode)
	}
	if asked != 0 {
		t.Errorf("the question was asked %d times with a flag set; want 0", asked)
	}
	if _, err := resolveInitSiteMode(false, ""); err != nil {
		t.Fatalf("bare init: %v", err)
	}
	if asked != 1 {
		t.Errorf("bare init asked %d times; want 1", asked)
	}
}

// TestSiteModeLabels pins the two answers the picker offers, in order.
func TestSiteModeLabels(t *testing.T) {
	if len(siteModeLabels) != 2 {
		t.Fatalf("got %d answers; want 2", len(siteModeLabels))
	}
	if siteModeLabels[0] != "Deploy an app or site from this directory" {
		t.Errorf("answer 1 = %q", siteModeLabels[0])
	}
	if siteModeLabels[1] != "No site — only databases, storage and auth (mobile app, external backend)" {
		t.Errorf("answer 2 = %q", siteModeLabels[1])
	}
}

// --- init: the create-new no-site path --------------------------------------

// initStub answers exactly what a create-new init needs and fails the test on
// anything that would create a site or attach a domain.
func initStub(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var paths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects":
			io.WriteString(w, `[]`)
		case r.URL.Path == "/api/v1/billing/plans":
			io.WriteString(w, `{"fixed_plans":[{"slug":"hobby","display_name":"Hobby","price_dzd_per_month":2500,"points":10}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/projects":
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"id":"p1","name":"taarefni","slug":"taarefni","framework":"auto"}`)
		default:
			t.Errorf("a site-less init must not call %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)
	return ts, &paths
}

// forceProjectName substitutes the project-name prompt.
func forceProjectName(t *testing.T, name string) {
	t.Helper()
	orig := promptProjectNameFn
	t.Cleanup(func() { promptProjectNameFn = orig })
	promptProjectNameFn = func() (string, error) { return name, nil }
}

// forceSiteMode substitutes the "How will you use this project?" question and
// records how often it was asked.
func forceSiteMode(t *testing.T, mode initSiteMode) *int {
	t.Helper()
	asked := 0
	orig := promptSiteModeFn
	t.Cleanup(func() { promptSiteModeFn = orig })
	promptSiteModeFn = func() (initSiteMode, error) { asked++; return mode, nil }
	return &asked
}

// readWrittenConfig reads the config init/link wrote, both decoded and raw:
// the raw form is how "the key is absent" is asserted.
func readWrittenConfig(t *testing.T, dir string) (ProjectConfig, string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, projectConfigName))
	if err != nil {
		t.Fatalf("expected %s written: %v", projectConfigName, err)
	}
	var cfg ProjectConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("config not valid JSON: %v", err)
	}
	return cfg, string(data)
}

// assertNoSiteKeys: a site-less config must not carry the keys at all, so a
// later CLI reading it can't mistake an empty string for a site.
func assertNoSiteKeys(t *testing.T, raw string) {
	t.Helper()
	for _, key := range []string{"site_id", "site_name", "site_slug"} {
		if strings.Contains(raw, key) {
			t.Errorf("site-less config still carries %q:\n%s", key, raw)
		}
	}
}

// TestInit_NoSiteChoice_WritesConfigWithoutSiteKeys drives the real command:
// answering "no site" must create the project with framework auto, write a
// config with no site keys, never call CreateSite or AddDomain, and close with
// the database/storage/auth next steps.
func TestInit_NoSiteChoice_WritesConfigWithoutSiteKeys(t *testing.T) {
	ts, paths := initStub(t)
	cliHome(t, ts.URL)
	forceProjectName(t, "taarefni")
	asked := forceSiteMode(t, siteModeNoSite)
	dir := t.TempDir()

	out := runCLI(t, dir, "init", "--billing-account", "ba1", "--plan", "hobby")

	if *asked != 1 {
		t.Errorf("the site question was asked %d times; want exactly once", *asked)
	}
	if strings.Contains(out, "Detected framework") {
		t.Errorf("a site-less project builds nothing, so the framework line is noise:\n%s", out)
	}
	if !strings.Contains(out, "✅ Project 'taarefni' created (slug: taarefni) — no site") {
		t.Errorf("output missing the no-site confirmation:\n%s", out)
	}
	if !strings.Contains(out, "Next: ghayma db create <name>   ghayma storage create <name>   ghayma auth create <name>") {
		t.Errorf("output missing the next-steps line:\n%s", out)
	}
	if !strings.Contains(out, "Add a site later with: ghayma site create <name>") {
		t.Errorf("output missing the add-a-site-later line:\n%s", out)
	}

	cfg, raw := readWrittenConfig(t, dir)
	assertNoSiteKeys(t, raw)
	if cfg.ProjectID != "p1" || cfg.Framework != "auto" {
		t.Errorf("config = %+v; want project p1 with framework auto", cfg)
	}
	for _, got := range *paths {
		if strings.Contains(got, "/sites") || strings.Contains(got, "/domains") {
			t.Errorf("a site-less init called %s", got)
		}
	}
}

// TestInit_NoSiteFlag_SkipsTheQuestion: --no-site answers it for CI.
func TestInit_NoSiteFlag_SkipsTheQuestion(t *testing.T) {
	ts, _ := initStub(t)
	cliHome(t, ts.URL)
	forceProjectName(t, "taarefni")
	asked := forceSiteMode(t, siteModeDeploy) // would pick "deploy a site" if reached
	dir := t.TempDir()

	out := runCLI(t, dir, "init", "--no-site", "--billing-account", "ba1", "--plan", "hobby")

	if *asked != 0 {
		t.Errorf("--no-site must not ask; asked %d times", *asked)
	}
	if !strings.Contains(out, "— no site") {
		t.Errorf("--no-site did not take the site-less path:\n%s", out)
	}
	_, raw := readWrittenConfig(t, dir)
	assertNoSiteKeys(t, raw)
}

// TestInit_NoSiteWithSiteFlagIsRefused: the contradiction is caught before any
// API call — nothing is created, nothing is written.
func TestInit_NoSiteWithSiteFlagIsRefused(t *testing.T) {
	cliHome(t, noRequestsStub(t).URL)
	dir := t.TempDir()

	out := runCLI(t, dir, "init", "--no-site", "--site", "admin")
	if !strings.Contains(out, "--no-site cannot be combined with --site") {
		t.Errorf("output %q should refuse the flag combination", out)
	}
	if lastExitCode != 1 {
		t.Errorf("exit code = %d; want 1 — these flags exist for scripts", lastExitCode)
	}
	if _, err := os.Stat(filepath.Join(dir, projectConfigName)); err == nil {
		t.Error("a refused init must not write a config")
	}
}

// TestInit_NoSiteWithDomainFlagIsRefused mirrors it for --domain.
func TestInit_NoSiteWithDomainFlagIsRefused(t *testing.T) {
	cliHome(t, noRequestsStub(t).URL)
	dir := t.TempDir()

	out := runCLI(t, dir, "init", "--no-site", "--domain", "example.com")
	if !strings.Contains(out, "--no-site cannot be combined with --domain") {
		t.Errorf("output %q should refuse the flag combination", out)
	}
}

// --- link / attach ----------------------------------------------------------

// TestSiteChoiceIndex pins the picker's index arithmetic: two fixed items come
// before the sites, and each maps onto its own sentinel.
func TestSiteChoiceIndex(t *testing.T) {
	if got := siteChoiceIndex(0); got != createNewIdx {
		t.Errorf("row 0 = %d; want createNewIdx (%d)", got, createNewIdx)
	}
	if got := siteChoiceIndex(1); got != noSiteIdx {
		t.Errorf("row 1 = %d; want noSiteIdx (%d)", got, noSiteIdx)
	}
	if createNewIdx == noSiteIdx {
		t.Error("the two fixed items must have distinct sentinels")
	}
	if got := siteChoiceIndex(2); got != 0 {
		t.Errorf("row 2 = %d; want sites[0]", got)
	}
	if got := siteChoiceIndex(5); got != 3 {
		t.Errorf("row 5 = %d; want sites[3]", got)
	}
}

// TestAttach_NoSiteItem_WritesConfigWithoutSiteKeys: picking "— No site" in the
// chooser links the directory to the project alone.
func TestAttach_NoSiteItem_WritesConfigWithoutSiteKeys(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("the no-site answer must not %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"id":"s1","name":"main","slug":"taarefni","status":"running"}]`))
	}))
	defer ts.Close()

	orig := promptSiteChoiceFn
	defer func() { promptSiteChoiceFn = orig }()
	promptSiteChoiceFn = func(sites []api.Site) (int, error) { return noSiteIdx, nil }

	dir := t.TempDir()
	project := &api.Project{ID: "p1", Name: "taarefni", Slug: "taarefni", Framework: "nextjs"}
	if err := attachToExistingProject(attachTestClient(ts.URL), project, dir, false); err != nil {
		t.Fatalf("attachToExistingProject: %v", err)
	}

	cfg, raw := readWrittenConfig(t, dir)
	assertNoSiteKeys(t, raw)
	if cfg.ProjectID != "p1" {
		t.Errorf("config = %+v; want project p1", cfg)
	}
}

// TestAttach_NoSiteFlag_AsksNothing: link --no-site skips the chooser and the
// site listing entirely.
func TestAttach_NoSiteFlag_AsksNothing(t *testing.T) {
	ts := noRequestsStub(t)

	orig := promptSiteChoiceFn
	defer func() { promptSiteChoiceFn = orig }()
	promptSiteChoiceFn = func(sites []api.Site) (int, error) {
		t.Error("--no-site must not show the site chooser")
		return 0, nil
	}

	dir := t.TempDir()
	project := &api.Project{ID: "p1", Name: "taarefni", Slug: "taarefni", Framework: "nextjs"}
	if err := attachToExistingProject(attachTestClient(ts.URL), project, dir, true); err != nil {
		t.Fatalf("attachToExistingProject(--no-site): %v", err)
	}
	_, raw := readWrittenConfig(t, dir)
	assertNoSiteKeys(t, raw)
}

// --- resolvers --------------------------------------------------------------

// TestResolveSiteContext_SiteLessConfig: the resolver reports a site-less
// config as a state, not a failure — no error, no invented site, NoSite set.
func TestResolveSiteContext_SiteLessConfig(t *testing.T) {
	dir := siteLessDir(t)
	noPrompt(t)

	ctx, err := resolveSiteContext(dir, "", "deploy")
	if err != nil {
		t.Fatalf("resolveSiteContext on a site-less config: %v", err)
	}
	if !ctx.NoSite {
		t.Error("NoSite = false; want true for a config with no site keys")
	}
	if ctx.Site.SiteID != "" || ctx.Site.SiteName != "" {
		t.Errorf("Site = %+v; want nothing invented", ctx.Site)
	}
	if ctx.ProjectID != "p1" {
		t.Errorf("ProjectID = %q; want p1 — the project still resolves", ctx.ProjectID)
	}
}

// TestResolveSiteContext_SiteLessConfigWithSiteFlag: --site on a site-less
// config pins nothing, so the flag guard must stay quiet instead of claiming
// the directory is linked to site "(unnamed)".
func TestResolveSiteContext_SiteLessConfigWithSiteFlag(t *testing.T) {
	dir := siteLessDir(t)
	noPrompt(t)

	ctx, err := resolveSiteContext(dir, "admin", "deploy")
	if err != nil {
		t.Fatalf("resolveSiteContext with --site: %v", err)
	}
	if !ctx.NoSite {
		t.Error("NoSite = false; want true")
	}
}

// TestCheckSiteFlag_NoSiteEntry: the same rule at the guard itself.
func TestCheckSiteFlag_NoSiteEntry(t *testing.T) {
	if err := checkSiteFlag(SiteEntry{}, "admin", "deploy"); err != nil {
		t.Errorf("an entry naming no site guards nothing, got %v", err)
	}
	if err := checkSiteFlag(SiteEntry{SiteSlug: "taarefni"}, "admin", "deploy"); err == nil {
		t.Error("a pinned directory must still refuse a --site naming another site")
	}
}

// TestResolveSiteContext_LegacyMainStillResolves (D5): a config carrying
// site_name "main" with no site_id keeps meaning the main site.
func TestResolveSiteContext_LegacyMainStillResolves(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{projectConfigName: legacyMainJSON})
	noPrompt(t)

	ctx, err := resolveSiteContext(dir, "", "deploy")
	if err != nil {
		t.Fatalf("resolveSiteContext on a legacy main config: %v", err)
	}
	if ctx.NoSite {
		t.Error("NoSite = true; a legacy lazy-main config is NOT site-less")
	}
	if ctx.Site.SiteName != "main" {
		t.Errorf("SiteName = %q; want main", ctx.Site.SiteName)
	}
}

// TestLocalConfigIsSiteLess: only a per-app config with no site answers true —
// a workspace manifest pins no single site by design and must not be mistaken
// for a site-less project.
func TestLocalConfigIsSiteLess(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"site-less", siteLessJSON, true},
		{"legacy main", legacyMainJSON, false},
		{"per-app", perAppJSON, false},
		{"manifest", manifestJSON, false},
	}
	for _, c := range cases {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{projectConfigName: c.content})
		t.Chdir(dir)
		if got := localConfigIsSiteLess(); got != c.want {
			t.Errorf("localConfigIsSiteLess(%s) = %v; want %v", c.name, got, c.want)
		}
	}
}

// --- site-scoped commands on a site-less project ----------------------------

// TestSiteScopedCommands_OnSiteLessConfig is the D4 sweep: every command that
// needs a site answers with the same single line and a non-zero exit, and none
// of them reaches for the API to find that out.
func TestSiteScopedCommands_OnSiteLessConfig(t *testing.T) {
	cases := []struct {
		name string
		args []string
		// files are written into the project directory first, for commands
		// that read one before they resolve a site.
		files map[string]string
	}{
		{name: "env list", args: []string{"env", "list"}},
		{name: "env set", args: []string{"env", "set", "API_URL=https://example.test"}},
		{name: "env delete", args: []string{"env", "delete", "API_URL"}},
		{name: "env import", args: []string{"env", "import", ".env"}, files: map[string]string{".env": "API_URL=https://example.test\n"}},
		{name: "domain create", args: []string{"domain", "create", "example.com"}},
		{name: "cron list", args: []string{"cron", "list"}},
		{name: "cron runs", args: []string{"cron", "runs", "nightly"}},
		{name: "cron trigger", args: []string{"cron", "trigger", "nightly"}},
		{name: "logs", args: []string{"logs"}},
		{name: "rollback", args: []string{"rollback"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cliHome(t, noRequestsStub(t).URL)
			forceStdin(t, false)
			noPrompt(t)

			dir := siteLessDir(t)
			if len(c.files) > 0 {
				writeFiles(t, dir, c.files)
			}
			out := runCLI(t, dir, c.args...)
			if !strings.Contains(out, noSiteMessage) {
				t.Errorf("output %q; want %q", out, noSiteMessage)
			}
			if lastExitCode != 1 {
				t.Errorf("exit code = %d; want 1", lastExitCode)
			}
		})
	}
}

// TestSiteUse_NoSitesInProject: `site use` is the recovery path, so it asks the
// API — and a project with no sites gets the site-less line instead of "site
// 'main' not found", which reads as a typo.
func TestSiteUse_NoSitesInProject(t *testing.T) {
	ts, _ := sitesStub(t, `[]`)
	cliHome(t, ts.URL)

	out := runCLI(t, siteLessDir(t), "site", "use", "main")
	if !strings.Contains(out, noSiteMessage) {
		t.Errorf("output %q; want %q", out, noSiteMessage)
	}
	if lastExitCode != 1 {
		t.Errorf("exit code = %d; want 1", lastExitCode)
	}
}

// TestSiteScale_NoSitesInProject: nothing to scale.
func TestSiteScale_NoSitesInProject(t *testing.T) {
	ts, _ := sitesStub(t, `[]`)
	cliHome(t, ts.URL)

	out := runCLI(t, siteLessDir(t), "site", "scale", "--tier", "b")
	if !strings.Contains(out, noSiteMessage) {
		t.Errorf("output %q; want %q", out, noSiteMessage)
	}
	if lastExitCode != 1 {
		t.Errorf("exit code = %d; want 1", lastExitCode)
	}
}

// TestProjectScopedCommands_WorkOnSiteLessConfig: the whole point of a
// site-less project is that db / storage / auth / points / site list still
// work. `site list` stands in for them — it is the one that reads site state.
func TestProjectScopedCommands_WorkOnSiteLessConfig(t *testing.T) {
	ts, paths := sitesStub(t, `[]`)
	cliHome(t, ts.URL)

	out := runCLI(t, siteLessDir(t), "site", "list")
	if !strings.Contains(out, "No sites found.") {
		t.Errorf("site list printed %q; want its own empty answer, unchanged", out)
	}
	if lastExitCode != 0 {
		t.Errorf("exit code = %d; want 0 — this command did not fail", lastExitCode)
	}
	if len(*paths) != 1 {
		t.Errorf("served %v; want the one site listing", *paths)
	}
}

// TestDeploy_SiteLessConfigSaysItCreatesMain: deploy keeps working — the
// platform materializes main — but the notice has to come before the upload,
// because nothing in the config ever mentioned a site.
func TestDeploy_SiteLessConfigSaysItCreatesMain(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound) // stop at the deploy call; the notice is what matters
	}))
	t.Cleanup(ts.Close)
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, siteLessDir(t), "deploy")
	notice := "ℹ️  This project has no site yet; deploying creates the site 'main'."
	if !strings.Contains(out, notice) {
		t.Errorf("output %q; want the notice %q", out, notice)
	}
	if i, j := strings.Index(out, notice), strings.Index(out, "🚀 Deploying"); i < 0 || j < 0 || i > j {
		t.Errorf("the notice must come before the deploy headline:\n%s", out)
	}
	if lastExitCode != 0 {
		t.Errorf("exit code = %d; deploy on a site-less project is not the refusal case", lastExitCode)
	}
}
