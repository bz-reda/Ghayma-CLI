# `ghayma connect --local` — CLI half of the tunnel (3a, part 2) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `ghayma connect --local [--site <slug>] [--out .env.local]` opens a tunnel session for the site, listens on `127.0.0.1` for each of the site's connected databases, writes the site's effective variables with the database hosts rewritten to those listeners, and pipes every accepted connection to the platform's tunnel gateway over a WebSocket until Ctrl-C.

**Architecture:** One WebSocket per TCP connection (no multiplexing): for every accepted local connection the CLI dials `wss://<gateway>/v1/dial?target=<id>` with `Authorization: Bearer <token>` and copies bytes both ways; pings every 30 s keep Cloudflare from cutting idle sockets. The session comes from `POST /projects/:id/sites/:siteId/tunnel-sessions` (admin role). The env file is produced by the same code path as `env pull` (`GetEffectiveSiteEnv` → rewrite → `renderDotenv`, with the same git-safety refusal), so the developer's app runs unchanged. The dial reuses the CLI's fallback dialer, so the Cloudflare-edge reachability fix of v0.9.1 applies to the tunnel too.

**Tech Stack:** Go 1.25, Cobra, `github.com/gorilla/websocket` (new direct dependency), `net`, `os/signal`; tests with `httptest` WebSocket servers and local TCP echo listeners — Linux, Windows, macOS.

## Global Constraints

- Never put AI attribution in commits, PR text or files.
- Never print a variable value; the file is written with `envPullRefusal` + 0600 exactly like `env pull`.
- The token is sent only in the `Authorization` header; it never appears in output, logs or the env file.
- `connect --local` takes NO positional arguments; `connect <kind> <name>` keeps requiring exactly two. Implement with a custom `Args` that checks the `--local` flag.
- Tests must not need the network or a TTY; CI runs on three OSes.
- Commit after every task; do not push.

---

### Task 1: API client — open/close a session

**Files:** create `internal/api/tunnel.go`, test `internal/api/tunnel_test.go`.

```go
type TunnelTarget struct { ID, Kind, Name, Host string; Port int }   // json: id, kind, name, host, port
type TunnelSession struct { Token, ExpiresAt, GatewayURL string; Targets []TunnelTarget }
func (c *Client) OpenTunnelSession(projectID, siteID string) (*TunnelSession, error)   // POST …/tunnel-sessions → 201
func (c *Client) CloseTunnelSession(projectID, siteID, token string) error             // POST …/tunnel-sessions/close {token}
```
Tests (httptest, as `connections_test.go` does): path/method, 201 decode, error message pass-through (`409` → the server's `error`), close body carries the token.

Commit: `feat(api): tunnel session calls`

---

### Task 2: The WebSocket dial and the pipe

**Files:** create `internal/api/tunnel_dial.go`, test `internal/api/tunnel_dial_test.go`; `go get github.com/gorilla/websocket@v1.5.3 && go mod tidy`.

```go
// DialTunnel opens one gateway stream for a target and returns it as a net.Conn.
func DialTunnel(ctx context.Context, gatewayURL, token, targetID string) (net.Conn, error)
// PipeTunnel copies bytes both ways until either side closes; returns when done.
func PipeTunnel(local net.Conn, remote net.Conn)
```
`DialTunnel` uses `websocket.Dialer{NetDialContext: apiDialer.DialContext, HandshakeTimeout: 15 * time.Second}` with the header; a non-101 answer surfaces the body's `error` (401 → "tunnel session expired — run ghayma connect --local again"). The returned `net.Conn` is an adapter over `*websocket.Conn` (binary messages ↔ byte stream, a read buffer for partial reads) that also runs a 30 s ping loop and sets the pong handler.

Tests: an `httptest` server upgrading with `websocket.Upgrader{}` that echoes binary frames; `DialTunnel` + write/read round-trip; a 401 answer produces the friendly error; `PipeTunnel` between a local `net.Pipe()` and the adapter moves bytes both ways and returns when the local side closes.

Commit: `feat(api): websocket dial and pipe for the tunnel`

---

### Task 3: Env rewriting and listener planning (pure)

**Files:** create `cmd/connect_local_plan.go`, test `cmd/connect_local_plan_test.go`.

```go
type localListener struct { Target api.TunnelTarget; Addr string }   // Addr = 127.0.0.1:<port>
// planListeners picks one local address per target: postgres from 15432 upward, mongodb from 15017 upward, skipping ports the probe reports busy.
func planListeners(targets []api.TunnelTarget, free func(port int) bool) []localListener
// rewriteEnvForLocal returns a copy of env where every value containing "<target.Host>:<target.Port>" has it replaced by the listener address; returns the keys it changed.
func rewriteEnvForLocal(env map[string]string, ls []localListener) (map[string]string, []string)
// localHeader is the dotenv header naming the listeners.
func localHeader(projectName, siteSlug string, ls []localListener) string
```
Tests: port planning (two postgres → 15432, 15433; a mongo → 15017; a busy port skipped), URL rewriting for `postgresql://u:p@pg-x.databases.svc.cluster.local:5432/db` and `mongodb://u:p@mg-y.databases.svc.cluster.local:27017/db?authSource=admin&replicaSet=rs0&directConnection=true` (host:port replaced, everything else intact), untouched values untouched, per-database `_<SEGMENT>` variants rewritten too, the header is all comment lines and names each listener.

Commit: `feat(connect): local listener planning and env rewriting`

---

### Task 4: `ghayma connect --local`

**Files:** modify `cmd/connect.go` (flags `--local`, `--out`; custom `Args`), create `cmd/connect_local.go`, test `cmd/connect_local_test.go`.

Flow (`runConnectLocal`): login check → `resolveConnectionTarget(client, connectSite, "tunnel into")` → `OpenTunnelSession` (a `409` prints the server's message: no database connection to tunnel; `403` → the admin-role hint used by `env pull`) → `planListeners` with a real probe (`net.Listen` then close) → `GetEffectiveSiteEnv` → `rewriteEnvForLocal` → the same refusal/write path as `env pull` (`gitFileState`, `envPullRefusal`, `renderDotenv(localHeader(...), env)`, 0600) → print one line per listener `   my-postgres (postgres)  → 127.0.0.1:15432` and `   Wrote N variables to .env.local (hosts point at the listeners; restore with: ghayma env pull)` → start one accept loop per listener (each accepted conn: `DialTunnel` → `PipeTunnel` in a goroutine; a dial error prints one line and closes the local conn) → block on SIGINT/SIGTERM → close listeners, best-effort `CloseTunnelSession`, print `Tunnel closed.` If the session expires (8 h) a dial answers 401 → print the friendly error once and exit 1.

`Args` on `connectCmd`: `func(cmd, args) error { if connectLocal { if len(args) != 0 { return errors.New("ghayma connect --local takes no arguments; use --site <slug> to choose the app") }; return nil }; return argChecker("argument", "connections", 2, 2)(cmd, args) }`. `runConnect` dispatches to `runConnectLocal` when `--local`.

Tests: the `Args` rule both ways; a loopback integration test that starts an `httptest` gateway (upgrade + echo), stubs `openSessionFn`/`pullEnvFn` seams (package-level function vars, like `promptManifestSiteFn`), runs the listener loop against one target, connects with `net.Dial`, and sees bytes echoed; the env file written in a temp dir contains the rewritten host and never the token.

Commit: `feat: ghayma connect --local tunnels the site's databases to localhost`

---

### Task 5: README, verification

Add to the README Connections table: `ghayma connect --local [--site <slug>] [--out <file>]` — "Tunnel the app's databases to localhost and write `.env.local` pointing at them". Run: `gofmt -l . ; go vet ./... && GOOS=windows go vet ./... && go test ./... && go build -o /dev/null .` → all `ok`. Commit `docs: connect --local in the README`. Report the log, the outputs, and deviations. Do not push. Note for the reviewer: end-to-end against the platform needs the backend and Infra halves deployed; until then `connect --local` fails at `OpenTunnelSession` with a plain 404 — map "404 page not found" to "This platform does not serve tunnel sessions yet" the way `env pull` does.
