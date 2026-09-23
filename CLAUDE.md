# CLAUDE.md

> **Source of truth — Ghayma-Architect.** Before changing platform behavior, read the relevant page in the Ghayma-Architect repo (sibling folder `Ghayma-Architect/`); update it in the same work-cycle after merging a behavior change. Every PR here carries the `Ghayma-Architect updated / not needed` checkbox.

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

Ghayma CLI (`ghayma`) — a Go CLI for deploying and managing apps on Ghayma. Built with Cobra for commands and promptui for interactive prompts. The Go module name is `paas-cli`.

## Build & Run

```bash
make build              # builds ./ghayma binary with version/commit/date ldflags
make release-all        # cross-compile for linux/darwin/windows into dist/
make clean              # remove dist/
go build -o ghayma .    # quick build without ldflags
```

Tests are Go tests: `cmd/*_test.go` and `internal/api/*_test.go`. Run with `go test ./...`.

## Architecture

**Entry point:** `main.go` calls `cmd.Execute()`.

**`cmd/`** — each file registers one top-level Cobra command (or subcommand group) onto `rootCmd`:
- `root.go` — root command, version command, ldflags vars (`version`, `commit`, `date`)
- `deploy.go` — tarball upload + deployment polling loop; has monorepo detection via `turbo.json`
- `init.go` — interactive project init, writes `.ghayma.json`
- `site.go` — `site create|list|use|scale` subcommands (`site add` kept as hidden deprecated alias)
- `site_environment.go` — `site environment|inherit-env` (Environments: a site's kind + the env var ladder)
- `promote.go` — `promote --from <site>`: ship the image a site already runs onto another site (default target: the project's default site)
- `access.go` / `access_render.go` — `access <kind> <name>` plus `add|rotate|allow|revoke`: the external principals (Connections 3c) that reach a database or bucket from outside Ghayma; the credential is printed once, at add and at rotate
- `db.go` — `db create|resize|list|info|credentials|stop|start|rotate|delete`
- `storage.go` — `storage create|list|info|credentials|expose|unexpose|rotate|delete`
- `auth.go` — `auth create|list|info|config|users|stats|rotate-keys|delete`
- `points.go` — `points` (project points meter + per-resource breakdown)
- `domain.go`, `env.go`, `logs.go`, `rollback.go`, `status.go`, `delete.go`, `login.go`, `logout.go`, `register.go`, `whoami.go`, `link.go`, `project.go` (ownership transfer)

**`internal/api/client.go`** — single API client struct wrapping `net/http`. All backend calls go through `authRequest()` which adds the Bearer token. The client handles projects, deployments, domains, env vars, databases, storage buckets, and auth apps.

**`internal/api/tar.go`** — creates gzip tarballs for deploy uploads, skipping `node_modules`, `.next`, `.git`, `.turbo`, `dist`.

**`internal/config/config.go`** — reads/writes `config.Path()` — `GHAYMA_CONFIG` when set, else `~/.paas-cli.json` (token, api_host, user_id, email). Default API host: `https://api.ghayma.tech`, overridden per-command by `GHAYMA_API_HOST`.

## Key Patterns

- Version info is injected at build time via `-ldflags` (see `Makefile` `LDFLAGS`)
- Project config lives in `.ghayma.json` in the project directory (not the CLI config). New projects write `.ghayma.json`; existing `.espacetech.json` projects are still read as a dual-read fallback (back-compat)
- Deploy detects monorepos by walking up to find `turbo.json`, then scans for `.ghayma.json` (or legacy `.espacetech.json`) files
- Env var operations auto-detect single-site projects and use site-scoped endpoints; multi-site projects require `site_id` in `.ghayma.json`
- Env vars can be INHERITED from the project's default site (Environments §3): the listing's `env_vars` are the site's own rows (what the replace-all PUT takes), `vars` is the resolved ladder. `env delete` uses the per-key DELETE and falls back to read-modify-write on a platform that does not serve it
- `--prod` on `deploy` is a warned no-op (Environments D6): a deployment is production when the target SITE is a production site
- Releases are triggered by pushing a `v*` tag (see `.github/workflows/release.yml`)
