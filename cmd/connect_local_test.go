package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/gorilla/websocket"
)

// echoGateway stands in for the platform's tunnel gateway: it upgrades the
// request and echoes every frame, so a byte written into a listener comes back.
func echoGateway(t *testing.T) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		for {
			kind, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if err := ws.WriteMessage(kind, data); err != nil {
				return
			}
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestConnectArgs_LocalTakesNoneAndTheKindFormTakesTwo(t *testing.T) {
	old := connectLocal
	t.Cleanup(func() { connectLocal = old })

	connectLocal = true
	if err := connectCmd.Args(connectCmd, nil); err != nil {
		t.Errorf("connect --local with no arguments = %v; want it accepted", err)
	}
	if err := connectCmd.Args(connectCmd, []string{"database", "pg"}); err == nil || !strings.Contains(err.Error(), "--site") {
		t.Errorf("connect --local database pg = %v; want a refusal pointing at --site", err)
	}

	connectLocal = false
	if err := connectCmd.Args(connectCmd, []string{"database", "pg"}); err != nil {
		t.Errorf("connect database pg = %v; want it accepted", err)
	}
	if err := connectCmd.Args(connectCmd, []string{"database"}); err == nil {
		t.Error("connect database: want the two-argument refusal")
	}
	if err := connectCmd.Args(connectCmd, nil); err == nil {
		t.Error("connect with no arguments: want the two-argument refusal")
	}
}

func TestServeListener_PipesAcceptedConnectionsThroughTheGateway(t *testing.T) {
	gateway := echoGateway(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	target := api.TunnelTarget{ID: "t1", Kind: "postgres", Name: "pg", Host: "pg-x.databases.svc.cluster.local", Port: 5432}
	session := &api.TunnelSession{Token: "tk-1", GatewayURL: gateway.URL, Targets: []api.TunnelTarget{target}}
	fatal := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		serveListener(ln, localListener{Target: target, Addr: ln.Addr().String()}, session, fatal)
		close(done)
	}()

	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial the listener: %v", err)
	}
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("select 1")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 8)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "select 1" {
		t.Fatalf("echo = %q, %v; want the bytes back through the gateway", buf, err)
	}
	conn.Close()

	ln.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("serveListener did not return after its listener closed")
	}
	select {
	case err := <-fatal:
		t.Errorf("unexpected fatal error: %v", err)
	default:
	}
}

func TestServeListener_ReportsAnExpiredSessionAsFatal(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"invalid or expired tunnel session"}`)
	}))
	defer gateway.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	target := api.TunnelTarget{ID: "t1", Kind: "postgres", Name: "pg"}
	session := &api.TunnelSession{Token: "tk-1", GatewayURL: gateway.URL}
	fatal := make(chan error, 1)
	go serveListener(ln, localListener{Target: target, Addr: ln.Addr().String()}, session, fatal)

	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial the listener: %v", err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Error("a failed dial must close the local connection")
	}

	select {
	case err := <-fatal:
		if !strings.Contains(err.Error(), "ghayma connect --local again") {
			t.Errorf("fatal = %v; want the expired-session message", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("an expired session must be reported as fatal")
	}
}

func TestWriteLocalEnv_WritesTheRewrittenFilePrivately(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env.local")
	ls := []localListener{{Target: api.TunnelTarget{ID: "t1", Kind: "postgres", Name: "my-postgres", Host: "pg-x.databases.svc.cluster.local", Port: 5432}, Addr: "127.0.0.1:15432"}}
	env := map[string]string{"DATABASE_URL": "postgresql://u:p@127.0.0.1:15432/db", "GHAYMA_API_KEY": "gsk_x"}

	if err := writeLocalEnv(path, "shop", "main", env, ls, false); err != nil {
		t.Fatalf("writeLocalEnv: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, "DATABASE_URL='postgresql://u:p@127.0.0.1:15432/db'") {
		t.Errorf("file must carry the rewritten value, got:\n%s", body)
	}
	if !strings.Contains(body, "my-postgres") || !strings.HasPrefix(body, "#") {
		t.Errorf("file must open with the header naming the listeners, got:\n%s", body)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, %v; want 0600", info.Mode().Perm(), err)
		}
	}
}

func TestWriteLocalEnv_RefusesAFileGitDoesNotIgnore(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init").CombinedOutput(); err != nil {
		t.Skipf("git init: %v (%s)", err, out)
	}
	path := filepath.Join(dir, ".env.local")

	err := writeLocalEnv(path, "shop", "main", map[string]string{"A": "1"}, nil, false)
	if err == nil || !strings.Contains(err.Error(), ".gitignore") {
		t.Fatalf("err = %v; want the env pull refusal", err)
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Error("a refused file must not be written")
	}
}

// stubTunnelSeams points the session, environment and wait seams at test
// doubles and restores them afterwards.
func stubTunnelSeams(t *testing.T, open func() (*api.TunnelSession, error), env map[string]string, wait func(<-chan error) error) {
	t.Helper()
	oldOpen, oldPull, oldWait, oldOut := openSessionFn, pullEnvFn, waitForStopFn, connectLocalOut
	t.Cleanup(func() {
		openSessionFn, pullEnvFn, waitForStopFn, connectLocalOut = oldOpen, oldPull, oldWait, oldOut
	})
	openSessionFn = func(*api.Client, string, string) (*api.TunnelSession, error) { return open() }
	pullEnvFn = func(*api.Client, string, string) (map[string]string, error) { return env, nil }
	waitForStopFn = wait
}

func TestTunnelSite_ServesTheDatabasesLocallyAndClosesTheSession(t *testing.T) {
	code := stubExit(t)
	gateway := echoGateway(t)

	closed := make(chan string, 1)
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		closed <- body["token"]
		io.WriteString(w, `{"closed":true}`)
	}))
	defer apiServer.Close()

	out := filepath.Join(t.TempDir(), ".env.local")
	var tunnelErr error
	stubTunnelSeams(t,
		func() (*api.TunnelSession, error) {
			return &api.TunnelSession{
				Token:      "tk-secret",
				ExpiresAt:  "2026-09-14T00:00:00Z",
				GatewayURL: gateway.URL,
				Targets:    []api.TunnelTarget{{ID: "t1", Kind: "postgres", Name: "my-postgres", Host: "pg-x.databases.svc.cluster.local", Port: 5432}},
			}, nil
		},
		map[string]string{
			"DATABASE_URL":   "postgresql://u:p@pg-x.databases.svc.cluster.local:5432/db",
			"GHAYMA_API_KEY": "gsk_x",
		},
		func(<-chan error) error {
			// The file names the listener, so it is also how the test finds it.
			data, err := os.ReadFile(out)
			if err != nil {
				tunnelErr = err
				return nil
			}
			addr := regexp.MustCompile(`127\.0\.0\.1:\d+`).FindString(string(data))
			if addr == "" {
				tunnelErr = fmt.Errorf("no listener address in the env file")
				return nil
			}
			conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
			if err != nil {
				tunnelErr = err
				return nil
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(5 * time.Second))
			if _, err := conn.Write([]byte("select 1")); err != nil {
				tunnelErr = err
				return nil
			}
			buf := make([]byte, 8)
			if _, err := io.ReadFull(conn, buf); err != nil {
				tunnelErr = err
			} else if string(buf) != "select 1" {
				tunnelErr = fmt.Errorf("echo = %q", buf)
			}
			return nil
		})
	connectLocalOut = out

	client := api.NewClient(&config.Config{APIHost: apiServer.URL, Token: "test-token"})
	target := &connectionTarget{ProjectID: "p1", ProjectName: "shop", Site: api.Site{ID: "s1", Slug: "main"}, AppDir: filepath.Dir(out)}

	output := captureStdout(t, func() { tunnelSite(client, target) })

	if tunnelErr != nil {
		t.Fatalf("through the tunnel: %v", tunnelErr)
	}
	if *code != -1 {
		t.Errorf("exit code = %d; want no exit", *code)
	}
	for _, want := range []string{"my-postgres (postgres)", "127.0.0.1:", "Wrote 2 variables", "ghayma env pull", "Tunnel closed."} {
		if !strings.Contains(output, want) {
			t.Errorf("output must mention %q, got:\n%s", want, output)
		}
	}
	for _, secret := range []string{"tk-secret", "gsk_x", "u:p@"} {
		if strings.Contains(output, secret) {
			t.Errorf("output must never print %q, got:\n%s", secret, output)
		}
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read the env file: %v", err)
	}
	body := string(data)
	if strings.Contains(body, "pg-x.databases.svc.cluster.local") || !strings.Contains(body, "127.0.0.1:") {
		t.Errorf("the env file must point at the listener, got:\n%s", body)
	}
	if strings.Contains(body, "tk-secret") {
		t.Error("the session token must never reach the env file")
	}

	select {
	case token := <-closed:
		if token != "tk-secret" {
			t.Errorf("closed token = %q; want the session's", token)
		}
	case <-time.After(5 * time.Second):
		t.Error("the session must be closed on the way out")
	}
}

func TestTunnelSite_GivesTheSessionBackWhenTheRunFails(t *testing.T) {
	code := stubExit(t)

	closed := make(chan string, 1)
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		closed <- body["token"]
		io.WriteString(w, `{"closed":true}`)
	}))
	defer apiServer.Close()

	// A session the platform opened with nothing to reach: the run stops, and
	// the session must not be left dangling until it expires.
	stubTunnelSeams(t,
		func() (*api.TunnelSession, error) {
			return &api.TunnelSession{Token: "tk-secret", GatewayURL: "wss://tunnel.example"}, nil
		},
		nil,
		func(<-chan error) error { return nil })

	client := api.NewClient(&config.Config{APIHost: apiServer.URL, Token: "test-token"})
	target := &connectionTarget{ProjectID: "p1", ProjectName: "shop", Site: api.Site{ID: "s1", Slug: "main"}, AppDir: t.TempDir()}

	output := captureStdout(t, func() { tunnelSite(client, target) })
	if !strings.Contains(output, "no database connection to tunnel") || *code != 1 {
		t.Errorf("output = %q, exit = %d; want the refusal and exit 1", output, *code)
	}
	select {
	case token := <-closed:
		if token != "tk-secret" {
			t.Errorf("closed token = %q; want the session's", token)
		}
	case <-time.After(5 * time.Second):
		t.Error("a failed run must still close the session")
	}
}

func TestTunnelSite_ReportsAPlatformWithoutTunnels(t *testing.T) {
	code := stubExit(t)
	stubTunnelSeams(t,
		func() (*api.TunnelSession, error) { return nil, fmt.Errorf("HTTP 404: 404 page not found") },
		nil,
		func(<-chan error) error { return nil })

	client := api.NewClient(&config.Config{APIHost: "http://127.0.0.1:1", Token: "test-token"})
	target := &connectionTarget{ProjectID: "p1", ProjectName: "shop", Site: api.Site{ID: "s1", Slug: "main"}, AppDir: t.TempDir()}

	output := captureStdout(t, func() { tunnelSite(client, target) })
	if !strings.Contains(output, "does not serve tunnel sessions yet") || *code != 1 {
		t.Errorf("output = %q, exit = %d; want the not-deployed message and exit 1", output, *code)
	}
}

func TestTunnelSite_PassesTheServersRefusalThrough(t *testing.T) {
	code := stubExit(t)
	stubTunnelSeams(t,
		func() (*api.TunnelSession, error) {
			return nil, fmt.Errorf("this site has no database connection to tunnel — connect one with ghayma connect")
		},
		nil,
		func(<-chan error) error { return nil })

	client := api.NewClient(&config.Config{APIHost: "http://127.0.0.1:1", Token: "test-token"})
	target := &connectionTarget{ProjectID: "p1", ProjectName: "shop", Site: api.Site{ID: "s1", Slug: "main"}, AppDir: t.TempDir()}

	output := captureStdout(t, func() { tunnelSite(client, target) })
	if !strings.Contains(output, "no database connection to tunnel") || *code != 1 {
		t.Errorf("output = %q, exit = %d; want the server's message and exit 1", output, *code)
	}
}
