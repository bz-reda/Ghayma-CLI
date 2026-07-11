# Points Marketplace CLI Implementation Plan (ghayma CLI)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Align the `ghayma` CLI with the points-allowance marketplace backend: tier/disk/backup/bracket selection on create commands, a points status command, resize support, and correct rendering of the marketplace rejection classes.

**Architecture:** All pricing data comes from `GET /api/v1/marketplace/catalog` (live since 2026-07-09); per-project usage from `GET /api/v1/projects/:id/points`. The CLI renders choices with their points costs (promptui interactive; flags for non-interactive), previews cost before submitting, and lets the backend be the authority — a 409 is rendered, never pre-blocked. ZERO pricing numbers hardcoded in the CLI.

**Tech Stack:** Go + Cobra + promptui (existing). Tests: standard Go tests in `cmd/*_test.go` + `internal/api/*_test.go` (httptest-backed client tests are the existing idiom — follow it).

**Context (read first):** `CLI/CLAUDE.md`; backend contract in `paas-api/internal/points/catalog_handler.go` (response shapes) + `summary.go`; spec `paas-api/docs/superpowers/specs/2026-07-07-points-allowance-design.md` §4–§5.

## Global Constraints

- Branch `feat/points-marketplace` off main. TDD per task; `go test ./...` green before done-claims. **No push / release tag without operator approval.**
- **NEVER** add `Co-Authored-By` / AI attribution to commits.
- Every points cost shown comes from the catalog response at runtime. No fallback price tables.
- Backward compatible: all new flags optional; without them, interactive prompts (or backend defaults for scripted use). Existing `.ghayma.json` files keep working untouched.
- Two rejection classes render differently (match the Dashboard): `insufficient plan points` → show shortfall + three paths (upgrade / free points / PAYG); `platform points capacity` → "Ghayma is at capacity — contact support" (never an upsell). Max-tier 409 → "tier X exceeds your plan's max (Y) — upgrade to unlock".
- The catalog fetch must fail soft for old servers: on 404 (backend without the endpoint), skip pricing display and fall back to plain prompts — never blocks the command.

---

### Task 1: API client — catalog, points summary, typed marketplace errors

**Files:**
- Modify: `internal/api/client.go` (+ new `internal/api/marketplace.go` if client.go is large)
- Test: `internal/api/marketplace_test.go` (httptest server idiom)

**Interfaces (later tasks consume):**

```go
type MarketplaceCatalog struct { /* mirror catalog_handler.go JSON 1:1 */ }
type ProjectPointsSummary struct { /* mirror summary.go JSON 1:1 */ }
func (c *Client) GetMarketplaceCatalog() (*MarketplaceCatalog, error)   // nil, ErrCatalogUnavailable on 404
func (c *Client) GetProjectPoints(projectID string) (*ProjectPointsSummary, error)

// Typed 409 classification, parsed from the error JSON body:
type MarketplaceError struct { Kind string /* insufficient|capacity|maxtier|other */; Message string }
func classifyAPIError(status int, body []byte) error // wraps into MarketplaceError when it matches
```

- [ ] Tests first (httptest: 200 catalog roundtrip, 404 → ErrCatalogUnavailable, 409 bodies classify into the three kinds), then implement, `go test ./...` green, commit `feat(api): marketplace catalog + points summary + typed 409s`.

### Task 2: `ghayma points` command

**Files:** create `cmd/points.go`, `cmd/points_test.go`.

- Resolves the project from `.ghayma.json` (same pattern as `status.go`), calls `GetProjectPoints`, renders: `Points: 12/30 used · 18 remaining` + breakdown table (KIND / NAME / DETAIL / PTS), `over budget by N` warning line when over, "(enforcement not active yet)" note when `enforced=false`, hidden meter + note for `payg`. Untiered rows marked `untiered (pending next deploy)`.
- [ ] Test: golden-ish output assertions on a fixture summary (existing cmd test idiom). Commit `feat(points): ghayma points command`.

### Task 3: `db create` + new `db resize` — tier / disk / backup

**Files:** modify `cmd/db.go`; test `cmd/db_points_test.go`; client additions for the resize/retier endpoint if missing.

- `db create` flags: `--tier xs|s|m|l`, `--disk-gb N`, `--backup weekly|daily|sixhourly`. Interactive fallback: promptui selects rendered from the catalog — `xs — 0.25 vCPU / 256 MB · 2 pts`, disk prompt stepped by `rates.db_block_gb`, backup select showing `+N pts` computed from the catalog formula (reuse ONE helper `dbCostPreview(catalog, tier, diskGB, backup)` — mirror of pricing.go, single place).
- Before submit: print `This database will reserve N pts` (+ `M remaining after` when the summary is fetchable).
- `db resize`: `--tier`, `--disk-gb` (grow-only — print "disk cannot shrink" client-side when target < current), `--backup`; hits the retier endpoint.
- 409s render per the Global Constraints classes.
- [ ] Tests: cost-preview helper vectors (same numbers as backend pricing_test.go), flag wiring, shrink message. Commit `feat(db): tier/disk/backup selection priced in points`.

### Task 4: `storage create` quota + `auth create` bracket/features

**Files:** modify `cmd/storage.go`, `cmd/auth.go`; tests alongside.

- Storage: `--quota-gb N` (stepped by `rates.obj_block_gb`; preview `= N pts`).
- Auth: `--users 1k|10k|100k|1m`, `--2fa`, `--sms`. SMS price line MUST come from the selected bracket (`+N pts, includes M msgs/mo`). Preview total via `authCostPreview` mirroring AuthCost.
- [ ] Tests as Task 3. Commit `feat(storage,auth): marketplace pricing on create`.

### Task 5: app tier + replicas — `site scale`

**Files:** modify `cmd/site.go` (+ client method for the SetAppTier endpoint); test `cmd/site_scale_test.go`.

- New subcommand `ghayma site scale [--site <name>] --tier a|b|c|d --replicas N` (either flag optional; current values shown first from the site record). Replicas validated ≥ 1 client-side with the backend's reason. Preview `tier × replicas = N pts`. Max-tier 409 rendered per class.
- Deploy flow itself stays untouched (tier self-heal happens server-side); `deploy` output gains one line when the deployment response carries points info — only if the field already exists; do NOT add backend changes in this plan.
- [ ] Tests, commit `feat(site): scale command — app tier + replicas`.

### Task 6: `ghayma init` must ASK which plan to use (operator requirement 2026-07-09)

**Files:** modify `cmd/init.go` (+ client method for the public plans list if missing); test `cmd/init_plan_test.go`.

**Why:** today `init` silently creates the project on the backend's default plan (`CreateProjectRequest.Plan` is sent empty → server defaults to hobby — this is how the 2026-07-09 ghayma-admin project landed on hobby without the operator choosing it). The backend already accepts `plan` in `POST /api/v1/projects` (`internal/projects/handler.go:31`), so this is pure CLI work.

- Fetch plans from the public `GET /api/v1/billing/plans` (carries `points`, `max_app_tier`, `max_db_tier`, price since PR #193). Render a promptui select AFTER the billing-account step: `hobby — 2,500 DZD/mo · 10 pts` (one line per active plan, price + points from the response — nothing hardcoded).
- `--plan <slug>` flag for non-interactive use; validate the slug against the fetched list and fail with the available slugs on a miss.
- Send the chosen slug as `plan` in the create request. If the plans fetch fails (old/self-hosted server), fall back to today's behavior (empty plan → server default) with a printed note — never block init.
- [ ] Tests: prompt options built from a fixture plans response; `--plan` validation (valid, invalid, fetch-failed fallback). Commit `feat(init): interactive plan selection at project create`.

### Task 7: docs touchpoint + release readiness

- Update `README` usage section for the new flags/commands (this repo documents commands there; keep to the existing style — this is explicitly asked, not an unsolicited doc).
- Full `go test ./...` + `make build` + manual smoke against prod (read-only: `ghayma points`, `db create` prompt rendering with `Ctrl-C` before submit).
- [ ] **STOP — operator approval before push; release = `v*` tag per repo flow (operator decides version).**

## Out of scope
- Backup schedule change command for existing DBs beyond `db resize --backup` (covered there).
- PAYG surfacing, pause-app, Docs-site content (separate Docs plan).

## Self-review notes
- Catalog 404 fail-soft keeps the CLI working against older self-hosted backends.
- One cost-preview helper per service, tested against the same vectors as backend pricing_test.go — no drift surface.
- No hardcoded pricing anywhere; prompts render from catalog only.
