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
- `db.go` — `db create|resize|list|info|credentials|link|unlink|expose|unexpose|stop|start|rotate|delete`
- `storage.go` — `storage create|list|info|credentials|link|unlink|expose|unexpose|rotate|delete`
- `auth.go` — `auth create|list|info|config|users|stats|rotate-keys|delete`
- `points.go` — `points` (project points meter + per-resource breakdown)
- `domain.go`, `env.go`, `logs.go`, `rollback.go`, `status.go`, `delete.go`, `login.go`, `logout.go`, `register.go`, `whoami.go`, `link.go`, `project.go` (ownership transfer)

**`internal/api/client.go`** — single API client struct wrapping `net/http`. All backend calls go through `authRequest()` which adds the Bearer token. The client handles projects, deployments, domains, env vars, databases, storage buckets, and auth apps.

**`internal/api/tar.go`** — creates gzip tarballs for deploy uploads, skipping `node_modules`, `.next`, `.git`, `.turbo`, `dist`.

**`internal/config/config.go`** — reads/writes `~/.paas-cli.json` (token, api_host, user_id, email). Default API host: `https://api.ghayma.tech`.

## Key Patterns

- Version info is injected at build time via `-ldflags` (see `Makefile` `LDFLAGS`)
- Project config lives in `.ghayma.json` in the project directory (not the CLI config). New projects write `.ghayma.json`; existing `.espacetech.json` projects are still read as a dual-read fallback (back-compat)
- Deploy detects monorepos by walking up to find `turbo.json`, then scans for `.ghayma.json` (or legacy `.espacetech.json`) files
- Env var operations auto-detect single-site projects and use site-scoped endpoints; multi-site projects require `site_id` in `.ghayma.json`
- Releases are triggered by pushing a `v*` tag (see `.github/workflows/release.yml`)
