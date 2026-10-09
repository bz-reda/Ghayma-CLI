package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCreateDatabase_SendsModeOnlyForValkey(t *testing.T) {
	var bodies []map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"database":{"id":"d1","name":"c","type":"valkey","valkey_mode":"store","status":"provisioning"}}`)
	}))
	defer ts.Close()
	c := newTestClient(ts.URL)

	db, _, err := c.CreateDatabase("c", "valkey", "p1", "", 0, "", nil, "store")
	if err != nil {
		t.Fatal(err)
	}
	if bodies[0]["mode"] != "store" {
		t.Fatalf("mode not sent: %v", bodies[0])
	}
	if db.ValkeyMode != "store" {
		t.Fatalf("ValkeyMode = %q", db.ValkeyMode)
	}
	if _, _, err := c.CreateDatabase("p", "postgres", "p1", "", 0, "", nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := bodies[1]["mode"]; ok {
		t.Fatalf("mode sent for postgres: %v", bodies[1])
	}
}

func TestCreateDatabase_RefusalKeepsCode(t *testing.T) {
	ts := jsonStatusServer(t, http.StatusBadRequest, `{"error":"Redis is not offered. Create a Valkey database instead: it speaks the Redis protocol and every Redis client works with it.","code":"use_valkey"}`)
	c := newTestClient(ts.URL)
	_, _, err := c.CreateDatabase("r", "redis", "p1", "", 0, "", nil, "")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeUseValkey || apiErr.Status != 400 {
		t.Fatalf("err = %#v", err)
	}
	if !strings.HasPrefix(err.Error(), "Redis is not offered.") {
		t.Fatalf("message lost: %q", err.Error())
	}
}

func TestSetValkeyMode(t *testing.T) {
	var got struct{ method, path, mode string }
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		got.method, got.path, got.mode = r.Method, r.URL.Path, b["mode"]
		io.WriteString(w, `{"database":{"id":"d1","type":"valkey","valkey_mode":"store","status":"running"}}`)
	}))
	defer ts.Close()
	db, err := newTestClient(ts.URL).SetValkeyMode("d1", "store")
	if err != nil {
		t.Fatal(err)
	}
	if got.method != "PATCH" || got.path != "/api/v1/databases/d1/valkey-mode" || got.mode != "store" {
		t.Fatalf("request = %+v", got)
	}
	if db.ValkeyMode != "store" {
		t.Fatalf("ValkeyMode = %q", db.ValkeyMode)
	}
}

func TestSetValkeyMode_RefusalKeepsCode(t *testing.T) {
	ts := jsonStatusServer(t, http.StatusConflict, `{"error":"the database must be running to change its disk","code":"database_not_running"}`)
	_, err := newTestClient(ts.URL).SetValkeyMode("d1", "store")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeDatabaseNotRunning {
		t.Fatalf("err = %#v", err)
	}
}

func TestStreamDatabaseLogs(t *testing.T) {
	var query string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Path + "?" + r.URL.RawQuery
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "2026-10-08T09:12:22.108Z Valkey is starting\n2026-10-08T09:12:22.139Z Ready\n")
	}))
	defer ts.Close()
	rc, err := newTestClient(ts.URL).StreamDatabaseLogs(context.Background(), "d1", 50, true)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	body, _ := io.ReadAll(rc)
	if query != "/api/v1/databases/d1/logs?follow=1&tail=50" {
		t.Fatalf("query = %q", query)
	}
	if !strings.Contains(string(body), "Valkey is starting") {
		t.Fatalf("body = %q", body)
	}
}

func TestStreamDatabaseLogs_NoFollowLeavesItOut(t *testing.T) {
	var query string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
	}))
	defer ts.Close()
	rc, err := newTestClient(ts.URL).StreamDatabaseLogs(context.Background(), "d1", 200, false)
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
	if query != "tail=200" {
		t.Fatalf("query = %q", query)
	}
}

// Cancelling ctx ends a follow the server holds open: Ctrl-C must not leave
// the CLI blocked on a stream that runs up to 10 minutes.
func TestStreamDatabaseLogs_CancelEndsFollow(t *testing.T) {
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "first line\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer ts.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rc, err := newTestClient(ts.URL).StreamDatabaseLogs(ctx, "d1", 200, true)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	br := bufio.NewReader(rc)
	if line, err := br.ReadString('\n'); err != nil || line != "first line\n" {
		t.Fatalf("line = %q, err = %v", line, err)
	}

	cancel()
	done := make(chan error, 1)
	go func() {
		_, err := br.ReadByte()
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("read after cancel returned no error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling ctx did not end the stream")
	}
}

// Any 2xx is the stream: a nil stream with a nil error would panic the caller.
func TestStreamDatabaseLogs_Any2xxIsTheStream(t *testing.T) {
	ts := jsonStatusServer(t, http.StatusPartialContent, "line\n")
	rc, err := newTestClient(ts.URL).StreamDatabaseLogs(context.Background(), "d1", 200, false)
	if err != nil || rc == nil {
		t.Fatalf("rc = %v, err = %v", rc, err)
	}
	rc.Close()
}

func TestStreamDatabaseLogs_NoPodKeepsCode(t *testing.T) {
	ts := jsonStatusServer(t, http.StatusConflict, `{"error":"the database has no running pod","code":"no_pod"}`)
	_, err := newTestClient(ts.URL).StreamDatabaseLogs(context.Background(), "d1", 200, false)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeNoPod || apiErr.Status != 409 {
		t.Fatalf("err = %#v", err)
	}
}

func TestDatabaseRefusalsKeepCode(t *testing.T) {
	ts := jsonStatusServer(t, http.StatusConflict, `{"error":"Valkey has no shared credential: every connected site has its own user (see the site's connections)","code":"no_shared_credential"}`)
	c := newTestClient(ts.URL)
	for name, call := range map[string]func() error{
		"stop":  func() error { return c.StopDatabase("d1") },
		"start": func() error { return c.StartDatabase("d1") },
	} {
		var apiErr *APIError
		err := call()
		if !errors.As(err, &apiErr) || apiErr.Code != CodeNoSharedCredential || apiErr.Status != 409 {
			t.Errorf("%s: err = %#v", name, err)
		}
		if err != nil && !strings.HasPrefix(err.Error(), "Valkey has no shared credential") {
			t.Errorf("%s: message lost: %q", name, err.Error())
		}
	}
}

func TestCatalogDBTier_ValkeyAllowed(t *testing.T) {
	off, on := false, true
	if !(CatalogDBTier{}).ValkeyAllowed() {
		t.Error("an older catalog without valkey_enabled must read as allowed")
	}
	if (CatalogDBTier{ValkeyEnabled: &off}).ValkeyAllowed() {
		t.Error("valkey_enabled=false must refuse")
	}
	if !(CatalogDBTier{ValkeyEnabled: &on}).ValkeyAllowed() {
		t.Error("valkey_enabled=true must allow")
	}
}

func TestDiskChangeError_ShrinkUnsupported(t *testing.T) {
	e := diskChangeError(409, []byte(`{"error":"this database's disk can grow, but shrinking it is not available yet","code":"shrink_unsupported"}`))
	if e == nil || e.Code != CodeShrinkUnsupported {
		t.Fatalf("got %#v", e)
	}
}

func TestDatabaseInfo_DecodesValkeyFields(t *testing.T) {
	var db DatabaseInfo
	json.Unmarshal([]byte(`{"type":"valkey","valkey_mode":"cache","status":"error","status_message":"CrashLoopBackOff: exit 1","pending_connections":["s1"]}`), &db)
	if db.ValkeyMode != "cache" || db.StatusMessage != "CrashLoopBackOff: exit 1" || len(db.PendingConnections) != 1 {
		t.Fatalf("db = %+v", db)
	}
}
