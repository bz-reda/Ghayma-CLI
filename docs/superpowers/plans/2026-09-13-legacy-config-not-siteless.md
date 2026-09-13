# A config without site keys is not proof of a site-less project — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop the released site-scoped commands (`env *`, `domain create`, `logs`, `rollback`, `cron *`, the `deploy` notice) from treating every `.ghayma.json` without site keys as a site-less project. Decide from the project's live site list instead.

**Architecture:** Ghayma-CLI #38 (v0.9.0) introduced site-less projects and made `hasSite(entry)` — "none of site_id / site_name / site_slug is set" — the test for one. But every config written by `init` before 2026-07-23 has none of those keys either (`{"project_id","name","slug","framework"}`), so on those projects `env list` and friends now print `noSiteMessage` and exit 1 although the project has a `main` site. The offline signal keeps its meaning ("the config names no site"); the decision "this project has no site" moves to the live list, through two helpers next to `liveSiteFor` (already on this branch from the connections commands): `projectHasNoSites` for commands that only need the gate, `resolveSiteLess` for commands that need a site id.

**Tech Stack:** Go 1.25, Cobra; tests use the existing harness in `cmd/nosite_test.go` (`cliHome`, `runCLI`, `lastExitCode`, `siteLessDir`, `noPrompt`, `forceStdin`) and `sitesStub` from `cmd/manifest_commands_test.go`.

## Global Constraints

- Never put AI attribution in commits, PR text or files.
- This branch is stacked on `feat/connections-commands` (Ghayma-CLI #40); `liveSiteFor(sites []api.Site, siteFlag string) (*api.Site, error)` in `cmd/connections_resolve.go` already exists — reuse it, do not redefine it.
- Behaviour on a genuinely site-less project must stay exactly what #38 shipped: the same `noSiteMessage` line and exit 1 — now after ONE `GET /api/v1/projects/:id/sites` that returns `[]`.
- Tests must not hit the network: every command under test gets an `httptest` server through `cliHome(t, url)`.
- CI runs `gofmt -l`, `go vet ./...`, `go test ./...` on Linux, Windows and macOS.
- Comments minimal, explain why. Commit after every task with the message given. Do not push.

---

### Task 1: The two live helpers

**Files:**
- Modify: `cmd/nosite.go` (add the helpers; reword `localConfigIsSiteLess`'s comment)
- Test: `cmd/nosite_live_test.go`

**Interfaces:**
- Consumes: `(*api.Client).ListSites(projectID string) ([]api.Site, error)`, `liveSiteFor`, `errNoSite`.
- Produces:
  - `func projectHasNoSites(client *api.Client, projectID string) (bool, error)`
  - `func resolveSiteLess(client *api.Client, projectID, siteFlag string) (*api.Site, error)`

- [ ] **Step 1: Write the failing tests**

Create `cmd/nosite_live_test.go`:

```go
package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"paas-cli/internal/api"
	"paas-cli/internal/config"
)

// liveClient is an API client pointed at a stub that serves the given site
// list on GET /api/v1/projects/p1/sites and 404s everything else.
func liveClient(t *testing.T, sites string) *api.Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/p1/sites" {
			io.WriteString(w, sites)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(ts.Close)
	return api.NewClient(&config.Config{APIHost: ts.URL, Token: "t"})
}

func TestProjectHasNoSites(t *testing.T) {
	if none, err := projectHasNoSites(liveClient(t, `[]`), "p1"); err != nil || !none {
		t.Errorf("empty list: none=%v err=%v; want true, nil", none, err)
	}
	if none, err := projectHasNoSites(liveClient(t, `[{"id":"s1","name":"main","slug":"main"}]`), "p1"); err != nil || none {
		t.Errorf("one site: none=%v err=%v; want false, nil", none, err)
	}
	if _, err := projectHasNoSites(liveClient(t, `[]`), "p-missing"); err == nil {
		t.Error("a failed listing must return the error, not a verdict")
	}
}

func TestResolveSiteLess(t *testing.T) {
	if _, err := resolveSiteLess(liveClient(t, `[]`), "p1", ""); err != errNoSite {
		t.Errorf("no sites = %v; want errNoSite", err)
	}
	one := `[{"id":"s1","name":"main","slug":"main"}]`
	if s, err := resolveSiteLess(liveClient(t, one), "p1", ""); err != nil || s.ID != "s1" {
		t.Errorf("lone site: %v, %v", s, err)
	}
	two := `[{"id":"s1","name":"main","slug":"main"},{"id":"s2","name":"Admin","slug":"admin"}]`
	if s, err := resolveSiteLess(liveClient(t, two), "p1", "admin"); err != nil || s.ID != "s2" {
		t.Errorf("--site: %v, %v", s, err)
	}
	if _, err := resolveSiteLess(liveClient(t, two), "p1", ""); err == nil || !strings.Contains(err.Error(), "--site") {
		t.Errorf("several sites without --site must ask for it, got %v", err)
	}
	if _, err := resolveSiteLess(liveClient(t, one), "p-missing", ""); err == nil || !strings.Contains(err.Error(), "failed to list sites") {
		t.Errorf("a failed listing must say so, got %v", err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/ -run 'ProjectHasNoSites|ResolveSiteLess'`
Expected: compile errors (`undefined: projectHasNoSites`, `undefined: resolveSiteLess`)

- [ ] **Step 3: Add the helpers and reword the offline check**

In `cmd/nosite.go`, add `"paas-cli/internal/api"` to the imports and append at the end of the file:

```go
// projectHasNoSites is the live half of the site-less check. A config naming
// no site is either a project created without one or a config written before
// configs carried site keys (init before 2026-07-23), and only the project's
// site list tells them apart: no sites at all is the site-less project.
func projectHasNoSites(client *api.Client, projectID string) (bool, error) {
	sites, err := client.ListSites(projectID)
	if err != nil {
		return false, err
	}
	return len(sites) == 0, nil
}

// resolveSiteLess resolves a config that names no site through the live list:
// errNoSite when the project has none, else --site, the only site, or an
// error asking for --site.
func resolveSiteLess(client *api.Client, projectID, siteFlag string) (*api.Site, error) {
	sites, err := client.ListSites(projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to list sites: %v", err)
	}
	return liveSiteFor(sites, siteFlag)
}
```

Replace the comment above `localConfigIsSiteLess` (currently "reports whether the nearest project config is a per-app config naming no site — a project created without one. …") with:

```go
// localConfigIsSiteLess reports whether the nearest project config is a per-app
// config naming no site. That is NOT proof of a site-less project: configs
// written before 2026-07-23 carry no site keys either, so a command must
// confirm with projectHasNoSites / resolveSiteLess before telling the user.
// A workspace manifest answers false: it pins no single site by design, and
// the commands reading it that way are legitimately project-wide.
```

- [ ] **Step 4: Run the tests**

Run: `gofmt -l cmd/ && go vet ./cmd/ && go test ./cmd/ -run 'ProjectHasNoSites|ResolveSiteLess|LocalConfigIsSiteLess'`
Expected: gofmt lists nothing; `ok  	paas-cli/cmd`

- [ ] **Step 5: Commit**

```bash
git add cmd/nosite.go cmd/nosite_live_test.go
git commit -m "fix(nosite): decide site-less from the live site list"
```

---

### Task 2: `env` and `domain create` resolve the live site

**Files:**
- Modify: `cmd/env.go` — `envSiteContext` (lines 66-88) and its four callers (`env set` ~line 141, `env list` ~line 213, `runEnvDelete` ~line 249, `env import` ~line 372)
- Modify: `cmd/domain.go` lines 25-50
- Test: `cmd/nosite_live_test.go` (append)

**Interfaces:**
- Consumes: Task 1 helpers; `envSite`, `domainSite` (the commands' `--site` flags).
- Produces: `func envSiteContext(client *api.Client, verb string) (projectID, siteID, name string, err error)` — the client is now a parameter.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/nosite_live_test.go`:

```go
// legacyJSON is what init wrote before 2026-07-23: a project and nothing about
// a site. It must resolve to the project's live site, never to "no site yet".
const legacyJSON = `{"project_id":"p1","name":"taarefni","slug":"taarefni","framework":"nextjs"}`

// legacyStub serves the live site list plus whatever else a command needs,
// recording every path it answered.
func legacyStub(t *testing.T, routes map[string]string) (*httptest.Server, *[]string) {
	t.Helper()
	var paths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if r.URL.Path == "/api/v1/projects/p1/sites" {
			io.WriteString(w, `[{"id":"s1","name":"main","slug":"main"}]`)
			return
		}
		if body, ok := routes[r.Method+" "+r.URL.Path]; ok {
			io.WriteString(w, body)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":"unexpected `+r.Method+` `+r.URL.Path+`"}`)
	}))
	t.Cleanup(ts.Close)
	return ts, &paths
}

func legacyDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{projectConfigName: legacyJSON})
	return dir
}

func TestEnvList_LegacyConfigResolvesTheLiveSite(t *testing.T) {
	ts, paths := legacyStub(t, map[string]string{
		"GET /api/v1/projects/p1/sites/s1/env": `{"env_vars":{"API_URL":"https://example.test"},"build_time_keys":[]}`,
	})
	cliHome(t, ts.URL)
	forceStdin(t, false)
	noPrompt(t)

	out := runCLI(t, legacyDir(t), "env", "list")
	if strings.Contains(out, noSiteMessage) {
		t.Fatalf("a legacy config on a project with a site must not be called site-less:\n%s", out)
	}
	if !strings.Contains(out, "API_URL=https://example.test") {
		t.Errorf("output %q; want the site's variables", out)
	}
	if lastExitCode != 0 {
		t.Errorf("exit code = %d; want 0", lastExitCode)
	}
	if !contains(*paths, "GET /api/v1/projects/p1/sites/s1/env") {
		t.Errorf("served %v; want the live site's env read", *paths)
	}
}

func TestDomainCreate_LegacyConfigResolvesTheLiveSite(t *testing.T) {
	ts, paths := legacyStub(t, map[string]string{
		"POST /api/v1/projects/p1/sites/s1/domains": `{"ok":true}`,
	})
	cliHome(t, ts.URL)
	forceStdin(t, false)
	noPrompt(t)

	out := runCLI(t, legacyDir(t), "domain", "create", "example.com")
	if strings.Contains(out, noSiteMessage) {
		t.Fatalf("legacy config must not be site-less:\n%s", out)
	}
	if !contains(*paths, "POST /api/v1/projects/p1/sites/s1/domains") {
		t.Errorf("served %v; want the domain attached to the live site", *paths)
	}
}

func contains(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}
```

Before running, check the exact path `AddDomain` posts to (`grep -n '"/domains"\|/domains' internal/api/client.go`) and the exact response it accepts, and adjust the stub's route key and body to match what the client expects for success. Same for the env read path (`GetEnvVarsSnapshotBySite`). If a helper named `contains` already exists in package `cmd` tests (`grep -rn "func contains(" cmd/`), rename this one `servedPath`.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/ -run 'LegacyConfigResolvesTheLiveSite'`
Expected: both FAIL with `a legacy config on a project with a site must not be called site-less` (the output contains `noSiteMessage`).

- [ ] **Step 3: Change `envSiteContext` to take the client and resolve live**

In `cmd/env.go`, change the signature and the `NoSite` branch:

```go
func envSiteContext(client *api.Client, verb string) (projectID, siteID, name string, err error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", "", err
	}

	ctx, err := resolveSiteContext(cwd, envSite, verb)
	switch {
	case errors.Is(err, errAttachCancelled):
		return "", "", "", errors.New("Cancelled")
	case errors.Is(err, errNoProjectConfig):
		return "", "", "", errors.New("no project config found — run 'ghayma init' first")
	case err != nil:
		return "", "", "", err
	}
	// A config naming no site is either a site-less project or one written
	// before configs carried site keys; the live list decides, and --site
	// applies here because the offline resolver had nothing to match it to.
	if ctx.NoSite {
		site, err := resolveSiteLess(client, ctx.ProjectID, envSite)
		if err != nil {
			return "", "", "", err
		}
		return ctx.ProjectID, site.ID, ctx.ProjectName, nil
	}
	return ctx.ProjectID, ctx.Site.SiteID, ctx.ProjectName, nil
}
```

Update the doc comment above it: replace the sentence about the two sentinel errors' translation only if needed; keep the rest.

At each of the four callers, create the client BEFORE calling `envSiteContext` and pass it. For example `env set` currently reads:

```go
		projectID, siteID, _, err := envSiteContext("set env vars on")
		if err != nil {
			reportSiteError(err)
			return
		}

		client := api.NewClient(cfg)
```

becomes:

```go
		client := api.NewClient(cfg)
		projectID, siteID, _, err := envSiteContext(client, "set env vars on")
		if err != nil {
			reportSiteError(err)
			return
		}
```

Do the same for `env list` (`"list env vars for"`), `runEnvDelete` (`"delete env vars from"`) and `env import` (`"import env vars into"` — there the client is created after the parse step; move `client := api.NewClient(cfg)` up to just before the `envSiteContext` call and delete the later declaration).

- [ ] **Step 4: `domain create`**

In `cmd/domain.go`, the block after `resolveSiteContext` currently reads:

```go
	// A domain is served by a site; there is nothing to attach it to yet.
	if ctx.NoSite {
		failNoSite()
		return
	}

	client := api.NewClient(cfg)
	domain := args[0]

	if err := client.AddDomain(ctx.ProjectID, ctx.Site.SiteID, domain); err != nil {
```

Change it to:

```go
	client := api.NewClient(cfg)
	domain := args[0]

	// A domain is served by a site. A config naming none is either a site-less
	// project (nothing to attach to yet) or a config written before site keys
	// existed; the live list decides.
	siteID := ctx.Site.SiteID
	if ctx.NoSite {
		site, err := resolveSiteLess(client, ctx.ProjectID, domainSite)
		if err != nil {
			reportSiteError(err)
			return
		}
		siteID = site.ID
	}

	if err := client.AddDomain(ctx.ProjectID, siteID, domain); err != nil {
```

- [ ] **Step 5: Run the tests**

Run: `gofmt -l cmd/ && go vet ./cmd/ && go test ./cmd/ -run 'LegacyConfigResolvesTheLiveSite|ProjectHasNoSites|ResolveSiteLess'`
Expected: `ok`. (The D4 sweep `TestSiteScopedCommands_OnSiteLessConfig` will now FAIL for env and domain because their stub refuses every request — Task 3 updates it.)

- [ ] **Step 6: Commit**

```bash
git add cmd/env.go cmd/domain.go cmd/nosite_live_test.go
git commit -m "fix(env,domain): a config without site keys resolves the live site"
```

---

### Task 3: `logs`, `rollback`, `cron`, the `deploy` notice, and the sweep test

**Files:**
- Modify: `cmd/logs.go` lines 26-45, `cmd/rollback.go` lines 62-80, `cmd/cron.go` lines 58-64 and 147-160, `cmd/deploy.go` lines 238-246
- Modify: `cmd/nosite_test.go` — `TestSiteScopedCommands_OnSiteLessConfig` (lines ~467-508) and `TestDeploy_SiteLessConfigSaysItCreatesMain` (~556-578)
- Test: `cmd/nosite_live_test.go` (append)

- [ ] **Step 1: Write the failing tests**

Append to `cmd/nosite_live_test.go`:

```go
func TestLogs_LegacyConfigIsNotSiteLess(t *testing.T) {
	ts, _ := legacyStub(t, map[string]string{
		"GET /api/v1/projects/p1/logs": `{"logs":"hello"}`,
	})
	cliHome(t, ts.URL)

	out := runCLI(t, legacyDir(t), "logs")
	if strings.Contains(out, noSiteMessage) {
		t.Fatalf("legacy config must not be site-less:\n%s", out)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("output %q; want the logs", out)
	}
}

func TestCronList_LegacyConfigIsNotSiteLess(t *testing.T) {
	ts, _ := legacyStub(t, map[string]string{
		"GET /api/v1/projects/p1/cron": `{"crons":[]}`,
	})
	cliHome(t, ts.URL)

	out := runCLI(t, legacyDir(t), "cron", "list")
	if strings.Contains(out, noSiteMessage) {
		t.Fatalf("legacy config must not be site-less:\n%s", out)
	}
}

func TestDeploy_LegacyConfigGetsNoCreatesMainNotice(t *testing.T) {
	ts, _ := legacyStub(t, nil) // deploy itself 404s; only the notice matters
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, legacyDir(t), "deploy")
	if strings.Contains(out, "deploying creates the site 'main'") {
		t.Errorf("a project that already has a site must not be told deploying creates one:\n%s", out)
	}
}
```

Check the exact paths and response shapes `GetAppLogs` and `ListCrons` expect (`grep -n "func (c \*Client) GetAppLogs\|func (c \*Client) ListCrons" -A 12 internal/api/*.go`) and adjust the stub route keys/bodies so the happy path is served. If `ListCrons` takes a query string, the map key still matches on `r.URL.Path` only.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/ -run 'LegacyConfigIsNotSiteLess|LegacyConfigGetsNoCreatesMainNotice'`
Expected: the logs and cron tests FAIL on `noSiteMessage`; the deploy test FAILS on the notice.

- [ ] **Step 3: Confirm live in `logs`, `rollback`, `cron`; make the `deploy` notice live**

`cmd/logs.go`: delete the gate that sits before the config parse:

```go
		// Logs come from a running app, which lives on a site.
		if localConfigIsSiteLess() {
			failNoSite()
			return
		}
```

and after `client := api.NewClient(cfg)` insert:

```go
		// Logs come from a running app, which lives on a site. A config naming
		// none is only site-less when the project really has no site.
		if localConfigIsSiteLess() {
			if none, err := projectHasNoSites(client, projectCfg.ProjectID); err == nil && none {
				failNoSite()
				return
			}
		}
```

`cmd/rollback.go`: the same move — delete the pre-parse gate, insert after `client := api.NewClient(cfg)`:

```go
		// Rollback restores a previous deployment of a site. A config naming
		// none is only site-less when the project really has no site.
		if localConfigIsSiteLess() {
			if none, err := projectHasNoSites(client, projectCfg.ProjectID); err == nil && none {
				failNoSite()
				return
			}
		}
```

`cmd/cron.go`, `runCronList`: delete the gate before `localConfig()`; after `client := api.NewClient(cfg)` insert:

```go
	// Cron jobs are declared per site, so a site-less project can never have
	// one — but a config naming no site is only site-less when the project
	// really has no site.
	if localConfigIsSiteLess() {
		if none, err := projectHasNoSites(client, projectID); err == nil && none {
			failNoSite()
			return
		}
	}
```

`cmd/cron.go`, `resolveCronJob`: replace

```go
	if localConfigIsSiteLess() {
		return nil, "", nil, errNoSite
	}

	projectID, configSiteID, _, err := localConfig()
	if err != nil {
		return nil, "", nil, err
	}

	client := api.NewClient(cfg)
```

with

```go
	projectID, configSiteID, _, err := localConfig()
	if err != nil {
		return nil, "", nil, err
	}

	client := api.NewClient(cfg)
	if localConfigIsSiteLess() {
		if none, err := projectHasNoSites(client, projectID); err == nil && none {
			return nil, "", nil, errNoSite
		}
	}
```

`cmd/deploy.go`: the notice block

```go
		if ctx.NoSite {
			fmt.Println("ℹ️  This project has no site yet; deploying creates the site 'main'.")
		}

		fmt.Println(deployHeadline(ctx))

		client := api.NewClient(cfg)
```

becomes (the client moves up two statements; nothing else in the function changes):

```go
		client := api.NewClient(cfg)

		// A site-less project still deploys — the platform materializes `main`
		// on the first one — but say so, because nothing in the config or the
		// init flow ever mentioned a site. A config naming no site on a
		// project that already has one gets no such notice; when the list
		// cannot be read the notice still prints, as before.
		if ctx.NoSite {
			if none, err := projectHasNoSites(client, ctx.ProjectID); err != nil || none {
				fmt.Println("ℹ️  This project has no site yet; deploying creates the site 'main'.")
			}
		}

		fmt.Println(deployHeadline(ctx))
```

- [ ] **Step 4: Update the D4 sweep so its stub serves the one listing**

In `cmd/nosite_test.go`, `TestSiteScopedCommands_OnSiteLessConfig`: the comment says "none of them reaches for the API to find that out" — that is no longer true, and is the point of this change. Replace the comment's last sentence with: "and each of them reaches for the API exactly once, for the site list, to find that out." Replace `cliHome(t, noRequestsStub(t).URL)` with:

```go
			ts, paths := sitesStub(t, `[]`)
			cliHome(t, ts.URL)
```

and after the exit-code assertion add:

```go
			if len(*paths) != 1 {
				t.Errorf("served %v; want exactly the one site listing", *paths)
			}
```

Check `sitesStub` (`cmd/manifest_commands_test.go:99`) records paths the way this assertion expects and answers `GET /api/v1/projects/p1/sites`; if it keys on a different project id, use the id `siteLessDir` writes.

`TestDeploy_SiteLessConfigSaysItCreatesMain` keeps its 404-for-everything stub: the listing fails, so the notice still prints — the test passes unchanged. Verify rather than assume.

- [ ] **Step 5: Run everything**

Run: `gofmt -l . ; go vet ./... && GOOS=windows go vet ./... && go test ./... && go build -o /dev/null .`
Expected: gofmt prints nothing; vets clean; every package `ok`; build OK.

- [ ] **Step 6: Commit**

```bash
git add cmd/logs.go cmd/rollback.go cmd/cron.go cmd/deploy.go cmd/nosite_test.go cmd/nosite_live_test.go
git commit -m "fix(logs,rollback,cron,deploy): confirm site-less from the live site list"
```

- [ ] **Step 7: Report**

List `git log --oneline feat/connections-commands..HEAD`, paste the Step 5 output, and note any stub path or response shape you had to adjust and why. Do not push and do not open a PR.
