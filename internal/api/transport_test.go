package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// blackholeIP has no listener and normally no route, so a dial to it fails or
// times out but never connects — the offline stand-in for the unreachable
// Cloudflare edge the affected resolver hands out.
const blackholeIP = "10.255.255.1"

func newTestDialer() *fallbackDialer {
	d := newFallbackDialer()
	d.dialTimeout = 150 * time.Millisecond
	d.dohURLs = nil
	d.dohClient = &http.Client{Timeout: 2 * time.Second}
	return d
}

// localListener returns a listening loopback socket and its port.
func localListener(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("listener addr %q: %v", ln.Addr(), err)
	}
	return port
}

// dohServer answers dns-json with a fixed set of A records.
func dohServer(t *testing.T, ips ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name == "" {
			t.Errorf("DoH request without a name: %s", r.URL)
		}
		if got := r.Header.Get("Accept"); got != "application/dns-json" {
			t.Errorf("DoH Accept = %q; want application/dns-json", got)
		}
		answers := make([]string, 0, len(ips))
		for _, ip := range ips {
			answers = append(answers, fmt.Sprintf(`{"name":%q,"type":1,"TTL":60,"data":%q}`, name, ip))
		}
		fmt.Fprintf(w, `{"Status":0,"Answer":[%s]}`, strings.Join(answers, ","))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFallbackDialer_SystemAddressWorks(t *testing.T) {
	port := localListener(t)
	d := newTestDialer()
	d.lookup = func(ctx context.Context, host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}

	conn, err := d.DialContext(context.Background(), "tcp", "api.test:"+port)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	if got := d.known["api.test"]; got != "127.0.0.1" {
		t.Errorf("known[api.test] = %q; want 127.0.0.1", got)
	}
}

func TestFallbackDialer_FallsBackToDoH(t *testing.T) {
	port := localListener(t)
	doh := dohServer(t, "127.0.0.1")

	var lookups atomic.Int32
	d := newTestDialer()
	d.dohURLs = []string{doh.URL + "/dns-query"}
	d.lookup = func(ctx context.Context, host string) ([]net.IP, error) {
		lookups.Add(1)
		return []net.IP{net.ParseIP(blackholeIP)}, nil
	}

	conn, err := d.DialContext(context.Background(), "tcp", "api.test:"+port)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	remote, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
	conn.Close()
	if remote != "127.0.0.1" {
		t.Fatalf("connected to %s; want the DoH answer 127.0.0.1 after the system address failed", remote)
	}
	if got := lookups.Load(); got != 1 {
		t.Fatalf("system lookups = %d; want 1", got)
	}

	conn2, err := d.DialContext(context.Background(), "tcp", "api.test:"+port)
	if err != nil {
		t.Fatalf("second DialContext: %v", err)
	}
	conn2.Close()
	if got := lookups.Load(); got != 1 {
		t.Errorf("system lookups after second dial = %d; want 1 (the working address is remembered)", got)
	}
}

func TestFallbackDialer_SecondDoHProvider(t *testing.T) {
	port := localListener(t)
	var firstHits atomic.Int32
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()
	working := dohServer(t, "127.0.0.1")

	d := newTestDialer()
	d.dohURLs = []string{broken.URL, working.URL}
	d.lookup = func(ctx context.Context, host string) ([]net.IP, error) {
		return nil, errors.New("no such host")
	}

	conn, err := d.DialContext(context.Background(), "tcp", "api.test:"+port)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	conn.Close()
	if got := firstHits.Load(); got != 1 {
		t.Errorf("first DoH provider hit %d times; want 1", got)
	}
}

func TestFallbackDialer_AllFail(t *testing.T) {
	doh := dohServer(t, "10.255.255.2")
	d := newTestDialer()
	d.dohURLs = []string{doh.URL}
	d.lookup = func(ctx context.Context, host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP(blackholeIP)}, nil
	}

	client := &http.Client{Transport: &http.Transport{DialContext: d.DialContext}}
	resp, err := client.Get("http://api.test/api/v1/projects")
	if err == nil {
		resp.Body.Close()
		t.Fatal("Get succeeded; want a reachability failure")
	}

	var re *ReachabilityError
	if !errors.As(err, &re) {
		t.Fatalf("errors.As(*ReachabilityError) = false for %#v", err)
	}
	if re.Host != "api.test" {
		t.Errorf("Host = %q; want api.test", re.Host)
	}
	want := []string{blackholeIP, "10.255.255.2"}
	if len(re.Tried) != len(want) || re.Tried[0] != want[0] || re.Tried[1] != want[1] {
		t.Errorf("Tried = %v; want %v (system address first, DoH answer second)", re.Tried, want)
	}
	if re.Unwrap() == nil {
		t.Error("Unwrap() = nil; want the last dial error")
	}
	msg := re.Error()
	for _, frag := range []string{"cannot reach api.test", blackholeIP, "10.255.255.2", "1.1.1.1", "8.8.8.8"} {
		if !strings.Contains(msg, frag) {
			t.Errorf("error message %q does not mention %q", msg, frag)
		}
	}
}

func TestFallbackDialer_IPLiteralBypassesLookup(t *testing.T) {
	port := localListener(t)
	var lookups atomic.Int32
	d := newTestDialer()
	d.dohURLs = []string{"http://127.0.0.1:1/dns-query"}
	d.lookup = func(ctx context.Context, host string) ([]net.IP, error) {
		lookups.Add(1)
		return nil, errors.New("lookup must not run for an IP literal")
	}

	conn, err := d.DialContext(context.Background(), "tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	conn.Close()

	if got := lookups.Load(); got != 0 {
		t.Errorf("lookups = %d; want 0 for an IP literal (httptest servers depend on this)", got)
	}
	if len(d.known) != 0 {
		t.Errorf("known = %v; want empty for an IP literal", d.known)
	}
}
