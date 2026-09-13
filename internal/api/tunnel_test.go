package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenTunnelSession_PostsAndDecodes201(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/projects/p1/sites/s1/tunnel-sessions" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"token":"tk-1","expires_at":"2026-09-13T20:00:00Z","gateway_url":"wss://tunnel.ghayma.tech","targets":[{"id":"t1","kind":"postgres","name":"my-postgres","host":"pg-x.databases.svc.cluster.local","port":5432}]}`)
	}))
	defer ts.Close()

	session, err := newTestClient(ts.URL).OpenTunnelSession("p1", "s1")
	if err != nil {
		t.Fatalf("OpenTunnelSession: %v", err)
	}
	if session.Token != "tk-1" || session.ExpiresAt != "2026-09-13T20:00:00Z" || session.GatewayURL != "wss://tunnel.ghayma.tech" {
		t.Errorf("session = %+v; want the decoded envelope", session)
	}
	if len(session.Targets) != 1 {
		t.Fatalf("targets = %+v; want one", session.Targets)
	}
	want := TunnelTarget{ID: "t1", Kind: "postgres", Name: "my-postgres", Host: "pg-x.databases.svc.cluster.local", Port: 5432}
	if session.Targets[0] != want {
		t.Errorf("target = %+v; want %+v", session.Targets[0], want)
	}
}

func TestOpenTunnelSession_SurfacesServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, `{"error":"this site has no database connection to tunnel — connect one with ghayma connect"}`)
	}))
	defer ts.Close()

	_, err := newTestClient(ts.URL).OpenTunnelSession("p1", "s1")
	if err == nil || err.Error() != "this site has no database connection to tunnel — connect one with ghayma connect" {
		t.Errorf("err = %v; want the server's message", err)
	}
}

func TestCloseTunnelSession_SendsTheToken(t *testing.T) {
	var got map[string]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/projects/p1/sites/s1/tunnel-sessions/close" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, `{"closed":true}`)
	}))
	defer ts.Close()

	if err := newTestClient(ts.URL).CloseTunnelSession("p1", "s1", "tk-1"); err != nil {
		t.Fatalf("CloseTunnelSession: %v", err)
	}
	if got["token"] != "tk-1" {
		t.Errorf("body = %v; want the session token", got)
	}
}

func TestCloseTunnelSession_SurfacesServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"error":"insufficient role"}`)
	}))
	defer ts.Close()

	if err := newTestClient(ts.URL).CloseTunnelSession("p1", "s1", "tk-1"); err == nil || err.Error() != "insufficient role" {
		t.Errorf("err = %v; want the server's message", err)
	}
}
