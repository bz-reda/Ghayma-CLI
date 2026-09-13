package api

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// echoGateway is a stand-in for the platform's tunnel gateway: it upgrades the
// request, hands it to check, then echoes every frame back.
func echoGateway(t *testing.T, check func(*http.Request)) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if check != nil {
			check(r)
		}
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

func TestDialTunnel_CarriesTokenAndRoundTripsBytes(t *testing.T) {
	var gotPath, gotAuth, gotTarget string
	ts := echoGateway(t, func(r *http.Request) {
		gotPath, gotAuth, gotTarget = r.URL.Path, r.Header.Get("Authorization"), r.URL.Query().Get("target")
	})

	conn, err := DialTunnel(context.Background(), ts.URL, "tk-1", "t 1")
	if err != nil {
		t.Fatalf("DialTunnel: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("hello world")); err != nil {
		t.Fatalf("write: %v", err)
	}
	// A small buffer must read one message across several calls.
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("first read = %q, %v; want \"hello\"", buf, err)
	}
	rest := make([]byte, 6)
	if _, err := io.ReadFull(conn, rest); err != nil || string(rest) != " world" {
		t.Fatalf("second read = %q, %v; want \" world\"", rest, err)
	}

	if gotPath != "/v1/dial" || gotTarget != "t 1" {
		t.Errorf("dialed %s?target=%s; want /v1/dial with the escaped target id", gotPath, gotTarget)
	}
	if gotAuth != "Bearer tk-1" {
		t.Errorf("Authorization = %q; want the bearer token", gotAuth)
	}
}

func TestDialTunnel_ExpiredSessionSaysHowToFixIt(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"invalid or expired tunnel session"}`)
	}))
	defer ts.Close()

	_, err := DialTunnel(context.Background(), ts.URL, "tk-1", "t1")
	if err == nil || !strings.Contains(err.Error(), "ghayma connect --local again") {
		t.Errorf("err = %v; want the expired-session hint", err)
	}
}

func TestDialTunnel_SurfacesGatewayError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"error":"unknown target"}`)
	}))
	defer ts.Close()

	_, err := DialTunnel(context.Background(), ts.URL, "tk-1", "nope")
	if err == nil || !strings.Contains(err.Error(), "unknown target") {
		t.Errorf("err = %v; want the gateway's message", err)
	}
}

func TestDialTunnel_PingsToKeepTheStreamOpen(t *testing.T) {
	old := tunnelPingInterval
	tunnelPingInterval = 10 * time.Millisecond
	t.Cleanup(func() { tunnelPingInterval = old })

	pinged := make(chan struct{}, 1)
	up := websocket.Upgrader{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		ws.SetPingHandler(func(data string) error {
			select {
			case pinged <- struct{}{}:
			default:
			}
			return ws.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(time.Second))
		})
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer ts.Close()

	conn, err := DialTunnel(context.Background(), ts.URL, "tk-1", "t1")
	if err != nil {
		t.Fatalf("DialTunnel: %v", err)
	}
	defer conn.Close()
	// Reading is what processes the pong; nothing is ever sent, so it blocks.
	go conn.Read(make([]byte, 1))

	select {
	case <-pinged:
	case <-time.After(5 * time.Second):
		t.Fatal("the gateway never saw a ping")
	}
}

func TestPipeTunnel_MovesBytesBothWaysAndReturnsWhenLocalCloses(t *testing.T) {
	ts := echoGateway(t, nil)
	remote, err := DialTunnel(context.Background(), ts.URL, "tk-1", "t1")
	if err != nil {
		t.Fatalf("DialTunnel: %v", err)
	}

	local, app := net.Pipe()
	done := make(chan struct{})
	go func() {
		PipeTunnel(local, remote)
		close(done)
	}()

	if _, err := app.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 4)
	app.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(app, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo = %q, %v; want \"ping\" back through the tunnel", buf, err)
	}

	app.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("PipeTunnel did not return after the local side closed")
	}
}
