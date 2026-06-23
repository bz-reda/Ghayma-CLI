# Ghayma Phase C — CLI Full Rename Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use `- [ ]` checkboxes.

**Goal:** Rename the `espacetech` CLI to `ghayma` — binary/command, service URLs, and display strings — WITHOUT breaking existing customers, by dual-reading the customer-committed config files.

**Architecture / decisions (locked):**
- Binary + command: `espacetech` → **`ghayma`**.
- Project config `.espacetech.json` → **dual-READ** (`.ghayma.json` first, else `.espacetech.json`); **WRITE `.ghayma.json`** on `init`/`link` for new projects. Same dual-read for `.espacetechignore` (try `.ghaymaignore`, else `.espacetechignore`, else `.dockerignore`).
- **KEEP** `~/.paas-cli.json` (auth-token file) and the Go module path `paas-cli` — brand-neutral `paas-*` internals; renaming the token file would force every user to re-auth.
- Service URLs → `api.ghayma.tech`, `auth.ghayma.tech`, `s3.ghayma.tech`, `*.web.ghayma.tech`, `cloud`→`dashboard.ghayma.dev`.

**Tech Stack:** Go (Cobra + promptui), module `paas-cli`. Has Go tests (`cmd/args_test.go`, `cmd/deprecation_test.go`, `internal/api/tar_smoke_test.go`). Verify with `go build ./...`, `go vet ./...`, `go test ./...`, and a real `make build` of the `ghayma` binary.

## Global Constraints
- Branch + PR. Branch `feat/ghayma-cli-rename` off `main`. No direct commits to `main`. No `Co-Authored-By`/AI attribution. `git add` only named files (never `-A`).
- **Back-compat is mandatory:** never remove the ability to read `.espacetech.json` / `.espacetechignore`. Existing customer projects MUST keep working.
- **Out of scope (coordinated elsewhere):** the backend/Dashboard-served install script (binary name + download URL) — a separate Dashboard PR; the GitHub repo rename — operator's side.
- Show `go build ./...` + `go test ./...` + `go vet ./...` output before "done".

## File Structure
- New helper: a shared `findProjectConfig()` + constants `projectConfigName = ".ghayma.json"`, `legacyProjectConfigName = ".espacetech.json"` (in `internal/config/` or a small `cmd/projectconfig.go`).
- Modify reads (~40 sites): `cmd/{domain,env,deploy,db,rollback,auth,storage,delete,site,project,init,logs}.go`, `internal/api/client.go`.
- Modify writes: `cmd/link.go`, `cmd/init.go`, `cmd/site.go`.
- Binary/build: `cmd/root.go`, `Makefile`, `scripts/build.sh`, `.gitignore`.
- URLs: `internal/config/config.go`, `cmd/auth.go`, `cmd/storage.go`, `cmd/init.go`.
- Ignore file: `internal/api/tar.go`.
- Docs: `README.md`, `CLAUDE.md`.

---

### Task 1: Project-config + ignore dual-read (CRITICAL back-compat)

**Files:** Create `cmd/projectconfig.go` (helper + constants); Modify the ~40 read sites + the write sites + `cmd/deploy.go` (`findInitializedApps`) + `internal/api/tar.go`; Test `cmd/projectconfig_test.go`.

**Interfaces — Produces:** `const projectConfigName = ".ghayma.json"`, `const legacyProjectConfigName = ".espacetech.json"`, `func findProjectConfig(dir string) (path string, err error)` (returns `.ghayma.json` if present in dir, else `.espacetech.json` if present, else an os.IsNotExist-compatible error), and `func projectConfigWritePath(dir string) string` (always returns the `.ghayma.json` path).

- [ ] **Step 1: Failing test** — `cmd/projectconfig_test.go`: in a temp dir, (a) only `.espacetech.json` present → `findProjectConfig` returns that path; (b) only `.ghayma.json` → returns that; (c) BOTH present → returns `.ghayma.json` (new wins); (d) neither → error. And `projectConfigWritePath` always ends in `.ghayma.json`.
- [ ] **Step 2:** `go test ./cmd/ -run TestFindProjectConfig` → FAIL (undefined).
- [ ] **Step 3: Implement** `cmd/projectconfig.go` with the constants + helpers (use `os.Stat`; prefer the new name).
- [ ] **Step 4:** Refactor every hardcoded `".espacetech.json"` READ site to resolve the path via `findProjectConfig(".")` (or the relevant dir for the monorepo scanner) before `os.ReadFile`. In `cmd/deploy.go:findInitializedApps`, match `info.Name() == projectConfigName || info.Name() == legacyProjectConfigName`. Change WRITE sites (`cmd/link.go`, `cmd/init.go`, `cmd/site.go`) to write `projectConfigWritePath(".")` (`.ghayma.json`). Update the `internal/api/client.go` error strings to mention `.ghayma.json`.
- [ ] **Step 5: `.espacetechignore` dual-read** — in `internal/api/tar.go`, the ignore-filename list: add `.ghaymaignore` BEFORE `.espacetechignore` (keep `.dockerignore` fallback). Update the `cmd/deploy.go:254` "no .espacetechignore or .dockerignore" message to mention `.ghaymaignore`.
- [ ] **Step 6:** `go test ./...`, `go vet ./...`, `go build ./...` — pass, show output. Add a pin/behavior test that a project with ONLY `.espacetech.json` still loads (back-compat).
- [ ] **Step 7: Commit** — `feat(config): dual-read .ghayma.json/.espacetech.json, write .ghayma.json for new projects`

---

### Task 2: Binary + command rename → ghayma

**Files:** `cmd/root.go:18,27`, `Makefile:4,22`, `scripts/build.sh:25,36,43`, `.gitignore:1`; update `cmd/args_test.go` assertions.

- [ ] **Step 1: Failing test** — update `cmd/args_test.go` expectations from `espacetech` → `ghayma` for the root command name/usage; run, watch fail.
- [ ] **Step 2: Implement** — `cmd/root.go:18` `Use: "ghayma"`; `:27` version banner `"ghayma %s\n"`. `Makefile:4` `BINARY := ghayma`. `scripts/build.sh` artifact names `ghayma-${GOOS}-${GOARCH}` (and the tar/zip globs). `.gitignore:1` `ghayma`. Leave the `-X paas-cli/cmd.version` ldflags (module path unchanged).
- [ ] **Step 3:** `go test ./cmd/`, `go build ./...`, and `make build` → produces a `ghayma` binary; `./ghayma version` prints `ghayma <ver>`. Show output.
- [ ] **Step 4: Commit** — `feat(cli): rename binary and root command espacetech -> ghayma`

---

### Task 3: Service URLs → ghayma

**Files:** `internal/config/config.go:37,43`, `cmd/auth.go` (the `auth.espace-tech.com` lines), `cmd/storage.go:128,130`, `cmd/init.go:226`.

- [ ] **Step 1: Pin test** — `internal/config` default `APIHost` is `https://api.ghayma.tech`; no `api.espace-tech.com` literal remains in `config.go`.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** `config.go:37,43` default host → `https://api.ghayma.tech`. `cmd/auth.go` display URLs → `https://auth.ghayma.tech/v1/...`. `cmd/storage.go:128` → `https://s3.ghayma.tech`; `:130` → `https://%s.web.ghayma.tech`. `cmd/init.go:226` billing link → `https://dashboard.ghayma.dev/settings/billing`.
- [ ] **Step 4:** `go test ./...`, `go build ./...`. Commit — `feat(cli): point service URLs at ghayma hosts`

---

### Task 4: Display strings → Ghayma

**Files:** all `cmd/*.go` user-facing strings; `cmd/root.go:19,20`, `login.go:20`, `register.go:15`, `link.go:18`, `project.go:29`.

- [ ] **Step 1: Pin test** — assert the help/hint strings use `ghayma` (e.g. `"ghayma login"`), and the `cmd/` tree no longer contains the literal command hint `espacetech ` or `"Espace-Tech"`.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Replace the ~80 `espacetech <subcommand>` hint/error strings → `ghayma <subcommand>`; the `Short`/`Long` "Espace-Tech Cloud" → "Ghayma". Do NOT touch the `paas-cli` module path, `~/.paas-cli.json`, or the kept-internal identifiers.
- [ ] **Step 4:** `go test ./...`, `go build ./...`, `go vet ./...`. Commit — `feat(cli): rebrand user-facing strings to Ghayma`

---

### Task 5: README + CLAUDE.md

**Files:** `README.md`, `CLAUDE.md`.

- [ ] **Step 1:** Update the binary/command (`espacetech`→`ghayma`), the install URL (`api.espace-tech.com/install.sh` → the ghayma install URL — note the script is backend-served), the repo clone refs (reconcile to the actual remote / new name), `docs.espace-tech.com` → `docs.ghayma.dev`, and document the **dual-read** behavior (`.ghayma.json`, falls back to `.espacetech.json`). Brand "Espace-Tech Cloud" → "Ghayma".
- [ ] **Step 2:** Commit — `docs(cli): update README + CLAUDE.md for ghayma`

---

## Final verification
- [ ] `go build ./...`, `go vet ./...`, `go test ./...` — green, output shown.
- [ ] `make build && ./ghayma version` → prints `ghayma <ver>`.
- [ ] A project dir with only `.espacetech.json` still resolves config (back-compat); a fresh `init` writes `.ghayma.json`.
- [ ] `rg -n "espace-tech|espacetech|Espace-Tech" cmd internal` — remaining hits are intentional: `.espacetech.json`/`.espacetechignore` in the **dual-read** code paths and tests, and the kept `paas-cli` module/`~/.paas-cli.json` references.

## Self-Review notes
- **Coverage:** dual-read config+ignore (T1), binary/command (T2), URLs (T3), strings (T4), docs (T5). Install script + repo rename are out of scope (coordinated).
- **Risk:** T1 is the customer-breaking one — the back-compat test (only-`.espacetech.json` project still loads) is the gate.
