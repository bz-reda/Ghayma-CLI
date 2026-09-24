# `ghayma token` commands (CLI) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Manage account API tokens from the CLI: `ghayma token create|list|revoke|rotate`, with project restriction support.

**Architecture:** One command group file `cmd/token.go` following `cmd/access.go` (Cobra subcommands, `--json`, `--yes`, `failf`, `readAnswer`); three client additions in `internal/api/client.go` with httptest round-trips in `internal/api/tokens_test.go`. Secrets print once. Rotating the CLI's own token updates `~/.paas-cli.json` in place.

**Tech Stack:** Go 1.24, Cobra, the existing `api.Client` / `config` packages.

Spec: `docs/superpowers/specs/2026-09-24-tokens-point4-design.md` §2 (the file is copied into the branch in the last task). The backend wire contract is being built in parallel (Ghayma-backend branch `feat/token-project-restriction`):

- `POST /api/v1/tokens` body: `{name, scope, expires_in_days, project_ids: [uuid|slug]}` → 201 `{id, name, token, scope, scopes, expires_at, project_ids, projects: [{id, slug}], message}`; errors 400 `unknown_project`, 403 `scope_exceeds_token` / `projects_exceed_token`, 400 cap reached.
- `GET /api/v1/tokens?include_revoked=1` → `[{id, name, token_prefix, scope, expires_at, last_used_at, revoked_at, created_at, project_ids, projects}]`.
- `DELETE /api/v1/tokens/:id` → 200 (idempotent); 404 `token not found` when out of reach.
- `POST /api/v1/tokens/:id/rotate` body `{expires_in_days}` → 200 same shape as create; 404; 409 `token_revoked`.

## Global Constraints

- Branch `feat/token-commands` from `origin/main` in a worktree; PR only.
- Files: `cmd/token.go` (new), `cmd/token_test.go` (new), `internal/api/client.go` (token section only), `internal/api/tokens_test.go`, `cmd/login.go` (only the `CreateAPIToken` call site if its signature changes), `docs/` or `README.md` command reference if the repo keeps one (check `README.md` for the command list; add the four commands there), plus the plan copy. Nothing else.
- Exact UX (verbatim strings matter for the docs PR being written in parallel):
  - `ghayma token create <name> [--scope deploy,databases] [--expires 90] [--project slug ...] [--json]`; default scope `deploy`, default expiry 90 days (`--expires 0` = never, prints a warning line).
  - `ghayma token list [--all] [--json]`; columns: `PREFIX  NAME  SCOPE  PROJECTS  EXPIRES  LAST USED  CREATED`; revoked rows only with `--all`, shown with `REVOKED` in the EXPIRES column.
  - `ghayma token revoke <id|prefix|name> [--yes]`; `ghayma token rotate <id|prefix|name> [--expires N] [--json]`.
  - A target selector matches by exact id, exact prefix (`gh_xxxxxxx`), or exact name among **live** tokens; 0 matches → `❌ no token matches "<x>"`; >1 → `❌ "<x>" matches N tokens — use the id` listing them.
  - Revoking or rotating the CLI's own token (`cfg.APITokenID`): revoke asks the extra confirmation "This is the token this CLI is logged in with; you will have to run ghayma login again. Continue? [y/N]" (skipped with `--yes`, still prints the note); rotate replaces `cfg.APIToken`/`cfg.APITokenID` and saves, printing "This CLI's own token was rotated; the config was updated."
  - Secret output (create/rotate, non-JSON): a line `Token (shown once): gh_…` then `Save it now — it will not be shown again.`
- `gofmt -l` empty, `go vet ./...`, `go test -race -count=1 ./...` green; `make build` succeeds.
- Minimal clean comments. ZERO AI attribution.

---

### Task 1: Client additions

**Files:** `internal/api/client.go`, `internal/api/tokens_test.go`.

**Interfaces (produces):**
```go
type TokenProject struct { ID string `json:"id"`; Slug string `json:"slug"` }
type CreatedAPIToken struct { ID, Name, Token, Scope string; ExpiresAt *time.Time; ProjectIDs []string `json:"project_ids"`; Projects []TokenProject `json:"projects"` }
type APITokenInfo struct { …existing…; LastUsedAt *time.Time `json:"last_used_at"`; RevokedAt *time.Time `json:"revoked_at"`; ProjectIDs []string `json:"project_ids"`; Projects []TokenProject `json:"projects"` }
type CreateAPITokenInput struct { Name, Scope string; ExpiresInDays int; ProjectIDs []string }
func (c *Client) CreateAPIToken(in CreateAPITokenInput) (*CreatedAPIToken, error)   // signature change: update cmd/login.go
func (c *Client) ListAPITokens(includeRevoked bool) ([]APITokenInfo, error)
func (c *Client) RotateAPIToken(id string, expiresInDays int) (*CreatedAPIToken, error)
// DeleteAPIToken unchanged
```
- [ ] Failing httptest tests: create sends `project_ids` only when non-empty and decodes `projects`; list adds `?include_revoked=1` only when asked; rotate posts `{expires_in_days}` and decodes the secret; a 403 with `code` surfaces as `*APIError{Status:403, Code:"scope_exceeds_token"}`.
- [ ] Implement; fix `cmd/login.go`'s call; run `go test ./internal/api/...`; commit `api: token create with projects, list with revoked, rotate`.

---

### Task 2: The command group

**Files:** `cmd/token.go`, `cmd/token_test.go`.

Skeleton (adapt names to `cmd/access.go`'s conventions; keep `Long` texts short and factual):

```go
var tokenCmd = &cobra.Command{Use: "token", Short: "Manage account API tokens", Long: `Account API tokens act as you across the API (scoped, optionally restricted to projects).
A token can only create, rotate or revoke tokens no wider than itself.`}
var tokenCreateCmd = &cobra.Command{Use: "create <name>", Args: argChecker("argument", "", 1, 1), Run: runTokenCreate}
var tokenListCmd   = &cobra.Command{Use: "list", Run: runTokenList}
var tokenRevokeCmd = &cobra.Command{Use: "revoke <id|prefix|name>", Args: argChecker("argument", "", 1, 1), Run: runTokenRevoke}
var tokenRotateCmd = &cobra.Command{Use: "rotate <id|prefix|name>", Args: argChecker("argument", "", 1, 1), Run: runTokenRotate}
```
Helpers: `tokenClient()` (login check like `accessClient`), `selectToken(tokens []api.APITokenInfo, ref string) (*api.APITokenInfo, error)` (pure, tested), `renderTokenSecret(t *api.CreatedAPIToken)`, `renderTokenTable(rows []api.APITokenInfo)`, `tokenProjectsColumn(t)` (slugs joined by `,`, `*` when unrestricted). Errors by status: 403 with `code` → print the backend's `error` message verbatim; 400 cap → verbatim; 404 → `no token matches`.

- [ ] Failing tests: `selectToken` (id / prefix / name / none / ambiguous, revoked rows ignored); `tokenProjectsColumn`; rotate-own updates a temp config (`GHAYMA_CONFIG` env to a temp file) — see how `cmd/logout_test.go` or similar stubs `exitFn` and config paths.
- [ ] Implement; `make build`; run `./ghayma token --help` and paste the help into the PR body; commit `cli: ghayma token create/list/revoke/rotate`.

---

### Task 3: README/docs, plan copy, PR

- [ ] Add the four commands to the CLI's command reference if one exists in the repo; copy the plan to `docs/superpowers/plans/2026-09-24-cli-token-commands-plan.md` (create the directory if the repo has none; if the repo keeps no `docs/superpowers`, skip the copy and say so).
- [ ] `go test -race -count=1 ./... 2>&1 | tail -5`; `make build`.
- [ ] Push; `gh pr create` titled `token: create, list, revoke and rotate account API tokens`; body: **Why**, **Commands** (help output), **Contract** (the four endpoints), **Own-token handling**, **Not changed**. No attribution. Note in the body that the backend PR (`feat/token-project-restriction`) must be live before `--project` and the ⊆ errors have effect; everything else works against today's API.

Report back: PR URL, test tail, deviations.
