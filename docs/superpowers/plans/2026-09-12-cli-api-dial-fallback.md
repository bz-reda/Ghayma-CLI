# CLI: reach the API when the resolver's Cloudflare edge is unreachable Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** `ghayma db list` / `env list` (every command) must work from a network whose DNS resolver returns Cloudflare edge addresses that network cannot reach, and must fail fast with an actionable message when nothing is reachable.

**Root cause (confirmed with the user's diagnostics):** the colleague's resolver returns `188.114.96.5` / `188.114.97.5` for `api.ghayma.tech` (a Cloudflare EU prefix); TCP to `188.114.96.5:443` fails from their ISP, while the same zone's `172.67.201.71` edge answers in 30 ms. Both returned addresses sit on the bad prefix, so Go's built-in fall-over between resolved addresses cannot help. The CLI amplifies it: `api.NewClient` uses a bare `&http.Client{}` — no dial timeout beyond the transport's 30 s, no retry, no re-resolution — so every command hangs ~30 s and dies with `dial tcp 188.114.96.5:443: i/o timeout`.

**Fix:** a custom dialer for the API client that (1) dials the system-resolved addresses with a short per-address timeout, (2) on total failure re-resolves the host through DNS-over-HTTPS (Cloudflare `cloudflare-dns.com`, then Google `dns.google`) and dials those addresses, (3) remembers the address that worked for the rest of the process, (4) fails with a message that names the addresses tried and tells the user to try DNS `1.1.1.1` / `8.8.8.8`; plus (5) two retries with backoff for idempotent requests on transport errors. No overall client timeout (uploads and log streaming stay unbounded).

**Tech Stack:** Go 1.25, stdlib only (`net`, `net/http`, `encoding/json`, `context`, `sync`, `time`). No new dependencies.

## Global Constraints

- Worktree only: `/Users/bouzi/Projects/THROCT/worktrees/CLI/fix-api-dial-fallback` (branch `fix/api-dial-fallback`).
- Files you change: new `internal/api/transport.go` + `internal/api/transport_test.go`, `internal/api/client.go` (`NewClient`, `authRequest`, and the two unauthenticated `Post` calls in `Login`/`Register` only), `CHANGELOG.md` if the repo keeps one (check), nothing else. Do not change command files under `cmd/`.
- `gofmt -l` clean, `go vet ./...`, `go build ./...`, `go test ./...` green before each commit (the whole CLI suite — including the existing `internal/api` tests, which stub the client; keep them passing). Tests must not need network: inject resolver, DoH URLs and dial timeouts.
- NO AI attribution. Minimal clean comments. Commit per task. Do NOT push, do NOT tag. Write `.superpowers/pr-body.md` (untracked) at the end.

### Task 1: The fallback dialer

**Files:** `internal/api/transport.go`, `internal/api/transport_test.go`.

**Interfaces:**
```go
type fallbackDialer struct {
    dialTimeout time.Duration                                  // default 6 s per address
    lookup      func(ctx context.Context, host string) ([]net.IP, error)   // default net.DefaultResolver.LookupIP(ctx, "ip", host)
    dohURLs     []string                                       // default {"https://cloudflare-dns.com/dns-query", "https://dns.google/resolve"}
    dohClient   *http.Client                                   // default: plain client, 5 s timeout (must NOT use this dialer — no recursion)
    mu          sync.Mutex
    known       map[string]string                              // host -> "ip" that last worked
}
func newFallbackDialer() *fallbackDialer
func (d *fallbackDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error)
type ReachabilityError struct { Host string; Tried []string; Last error }
func (e *ReachabilityError) Error() string   // "cannot reach api.ghayma.tech: connection to 188.114.96.5:443 timed out (also tried 188.114.97.5, 172.67.201.71). Your DNS answers point at addresses this network cannot reach — try setting your DNS to 1.1.1.1 or 8.8.8.8, or check a VPN or firewall."
func (e *ReachabilityError) Unwrap() error
```
Behaviour of `DialContext`:
1. Split `addr` into host/port. If `host` is an IP literal → plain `net.Dialer{Timeout: dialTimeout}` dial, return.
2. If `known[host]` is set → dial it first; on success return; on failure fall through (and forget it).
3. System resolution via `lookup`; dial each address in order with `dialTimeout` (a `net.Dialer` with that timeout, honouring `ctx`). First success → remember `known[host]`, return.
4. If every system address failed (or lookup failed) → DoH: for each URL, GET `<url>?name=<host>&type=A` with header `Accept: application/dns-json`, parse `{"Answer":[{"type":1,"data":"1.2.3.4"}]}`; collect A records not already tried; dial each with `dialTimeout`; first success → remember, return.
5. Nothing worked → return `&ReachabilityError{Host, Tried (every address attempted, in order), Last (the last dial error)}`.
Keep it dependency-free and small (~120 lines). Log nothing.

- [ ] **Failing tests** (all offline, `httptest` + local listeners):
  - `TestFallbackDialer_SystemAddressWorks`: lookup returns the loopback IP of a running `net.Listen("tcp", "127.0.0.1:0")`; dial succeeds; `known` set.
  - `TestFallbackDialer_FallsBackToDoH`: lookup returns `10.255.255.1` (RFC1918 blackhole — with `dialTimeout = 150ms` it times out fast); a `httptest.Server` playing DoH answers the loopback listener's IP; dial succeeds via DoH; `Tried` order verified through the returned conn's remote address; second dial goes straight to `known` without calling `lookup` (count calls).
  - `TestFallbackDialer_SecondDoHProvider`: first DoH URL returns 500, second answers → success.
  - `TestFallbackDialer_AllFail`: lookup → blackhole, DoH → blackhole → `*ReachabilityError` with both IPs in `Tried`, message contains `1.1.1.1` hint; `errors.As` works through `url.Error` wrapping (assert with a real `http.Client{Transport: &http.Transport{DialContext: d.DialContext}}` GET).
  - `TestFallbackDialer_IPLiteralBypassesLookup`: lookup must not be called.
- [ ] Implement; tests green. Commit `feat(api): dialer that re-resolves through DNS-over-HTTPS when the resolver's addresses are unreachable`.

### Task 2: Wire the client + retries

**Files:** `internal/api/client.go` (`NewClient`, `authRequest`, `Login`, `Register`), `internal/api/transport.go` (`newHTTPClient`), `internal/api/transport_test.go` (extend).

- [ ] `newHTTPClient() *http.Client`: `Transport = http.DefaultTransport.(*http.Transport).Clone()` with `DialContext = newFallbackDialer().DialContext`, `TLSHandshakeTimeout = 10 * time.Second`, `Proxy = http.ProxyFromEnvironment` (kept). **No** `Client.Timeout`, **no** `ResponseHeaderTimeout` (uploads and log streaming must stay unbounded). `NewClient` uses it.
- [ ] Retry helper `func (c *Client) do(req *http.Request) (*http.Response, error)`: attempts = 3 for `GET`/`HEAD` (idempotent, `req.Body == nil`), 1 otherwise; retry only on transport errors (`*url.Error` whose `Err` is a `net.Error` timeout, a `*ReachabilityError`, or `syscall.ECONNRESET`/`ECONNREFUSED` wrapped) — never on an HTTP status; backoff 500 ms then 1500 ms; honour `req.Context()`. `authRequest` and the two `Post` calls in `Login`/`Register` go through `do` (POSTs therefore get one attempt, but still benefit from the dialer's fallback).
- [ ] **Tests:** a fake `http.RoundTripper` injected into `Client.http.Transport`: GET fails twice with a timeout `url.Error` then succeeds → 1 response, 3 calls; POST fails once → error after 1 call; a 500 status → no retry. Keep `internal/api`'s existing tests green: they call `NewClient(cfg)` with `cfg.APIHost = httptest.Server.URL` (an IP-literal host such as `127.0.0.1:PORT`), which the dialer's IP-literal bypass must handle without any lookup or DoH — this is why Task 1 has that test.
- [ ] `go test ./...` green. Commit `feat(api): retry idempotent requests on transport errors; wire the fallback dialer`.

### Task 3: Final

- [ ] `gofmt -l .`; `go vet ./...`; `go build ./...`; `go test ./...` — all green, all three OSes are CI-only; make sure nothing you wrote is platform-specific.
- [ ] `CHANGELOG.md` entry if the repo has one (check; if not, skip).
- [ ] `.superpowers/pr-body.md`: the diagnosis (resolver → 188.114.96.x/97.x, ISP path down, 172.67 fine; bare client, 30 s hang), the fix (fallback dialer + DoH re-resolution + memory + actionable error; retries for idempotent calls; no global timeout so uploads/logs stay unbounded), what the colleague will see after the release (first command may take ~6 s to fail over, then instant), the manual test (`ghayma db list` on the affected machine), and "tag v0.9.1 after merge". No attribution. Do NOT push.
