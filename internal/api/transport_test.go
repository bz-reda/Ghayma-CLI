package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"paas-cli/internal/config"
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
	// No cache path: a test must never read or write the user's home. Tests
	// that exercise the cache point this at a temp file.
	d.cachePath = ""
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

// TestFallbackDialer_DialsAGroupInParallel pins the reason a command on the
// affected network no longer waits out one timeout per dead address: the whole
// group goes out at once, so the group costs one dial timeout, not their sum.
func TestFallbackDialer_DialsAGroupInParallel(t *testing.T) {
	port := localListener(t)
	d := newTestDialer()
	d.lookup = func(ctx context.Context, host string) ([]net.IP, error) {
		return []net.IP{
			net.ParseIP(blackholeIP),
			net.ParseIP("10.255.255.2"),
			net.ParseIP("127.0.0.1"),
		}, nil
	}

	start := time.Now()
	conn, err := d.DialContext(context.Background(), "tcp", "api.test:"+port)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	remote, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
	conn.Close()

	if remote != "127.0.0.1" {
		t.Errorf("connected to %s; want the reachable 127.0.0.1", remote)
	}
	if elapsed >= 2*d.dialTimeout {
		t.Errorf("dial took %v; want under %v — the two dead addresses were dialed one after another", elapsed, 2*d.dialTimeout)
	}
	if got := d.known["api.test"]; got != "127.0.0.1" {
		t.Errorf("known[api.test] = %q; want 127.0.0.1", got)
	}
}

// TestFallbackDialer_TriedKeepsTheOfferedOrder guards the error message against
// the parallel dial: addresses finish in whatever order the network decides,
// but they must be reported in the order the resolver offered them.
func TestFallbackDialer_TriedKeepsTheOfferedOrder(t *testing.T) {
	want := []string{blackholeIP, "10.255.255.2", "10.255.255.3"}
	d := newTestDialer()
	d.lookup = func(ctx context.Context, host string) ([]net.IP, error) {
		ips := make([]net.IP, 0, len(want))
		for _, ip := range want {
			ips = append(ips, net.ParseIP(ip))
		}
		return ips, nil
	}

	_, err := d.DialContext(context.Background(), "tcp", "api.test:443")
	var re *ReachabilityError
	if !errors.As(err, &re) {
		t.Fatalf("DialContext error = %#v; want *ReachabilityError", err)
	}
	if !slices.Equal(re.Tried, want) {
		t.Errorf("Tried = %v; want %v", re.Tried, want)
	}
}

func TestNewFallbackDialer_Defaults(t *testing.T) {
	d := newFallbackDialer()
	if d.dialTimeout != 4*time.Second {
		t.Errorf("dialTimeout = %v; want 4s", d.dialTimeout)
	}
	if d.cachePath == "" {
		t.Error("cachePath is empty; the real dialer must remember addresses across commands")
	}
	if got := newTestDialer().cachePath; got != "" {
		t.Errorf("newTestDialer cachePath = %q; want empty so tests never touch the user's home", got)
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

// diskCache mirrors the documented shape of ~/.paas-cli.net-cache.json
// independently of the production type, so renaming either key fails here.
type diskCache map[string]struct {
	IP    string `json:"ip"`
	Until string `json:"until"`
}

// tempCachePath returns a throwaway path for the remembered-address file.
func tempCachePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "net-cache.json")
}

func readDiskCache(t *testing.T, path string) diskCache {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	var entries diskCache
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("cache %s is not JSON: %v", data, err)
	}
	return entries
}

func seedCache(t *testing.T, path string, entries map[string]netCacheEntry) {
	t.Helper()
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal cache: %v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
}

// TestFallbackDialer_RemembersAcrossProcesses covers why the cache exists at
// all: every ghayma command is a new process, so the address that worked has
// to survive on disk or the next command repeats the whole fallback.
func TestFallbackDialer_RemembersAcrossProcesses(t *testing.T) {
	port := localListener(t)
	path := tempCachePath(t)

	first := newTestDialer()
	first.cachePath = path
	first.lookup = func(ctx context.Context, host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}
	conn, err := first.DialContext(context.Background(), "tcp", "api.test:"+port)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	conn.Close()

	entry, ok := readDiskCache(t, path)["api.test"]
	if !ok {
		t.Fatal("nothing cached for api.test")
	}
	if entry.IP != "127.0.0.1" {
		t.Errorf("cached ip = %q; want 127.0.0.1", entry.IP)
	}
	until, err := time.Parse(time.RFC3339, entry.Until)
	if err != nil {
		t.Fatalf("until %q is not RFC3339: %v", entry.Until, err)
	}
	if drift := time.Until(until) - 24*time.Hour; drift > 0 || drift < -time.Minute {
		t.Errorf("until = %v, %v away from a 24h TTL", until, drift)
	}

	// A second process: same cache file, a resolver that must not be consulted.
	var lookups atomic.Int32
	second := newTestDialer()
	second.cachePath = path
	second.lookup = func(ctx context.Context, host string) ([]net.IP, error) {
		lookups.Add(1)
		return nil, errors.New("the cached address should have answered")
	}
	conn2, err := second.DialContext(context.Background(), "tcp", "api.test:"+port)
	if err != nil {
		t.Fatalf("second process DialContext: %v", err)
	}
	conn2.Close()
	if got := lookups.Load(); got != 0 {
		t.Errorf("lookups = %d; want 0 (the cached address is dialed first)", got)
	}
}

func TestFallbackDialer_IgnoresAnExpiredCacheEntry(t *testing.T) {
	port := localListener(t)
	path := tempCachePath(t)
	seedCache(t, path, map[string]netCacheEntry{
		"api.test": {IP: blackholeIP, Until: time.Now().Add(-time.Minute)},
	})

	var lookups atomic.Int32
	d := newTestDialer()
	d.cachePath = path
	d.lookup = func(ctx context.Context, host string) ([]net.IP, error) {
		lookups.Add(1)
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}

	start := time.Now()
	conn, err := d.DialContext(context.Background(), "tcp", "api.test:"+port)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	conn.Close()

	if got := lookups.Load(); got != 1 {
		t.Errorf("lookups = %d; want 1 (an expired entry is not a remembered address)", got)
	}
	if elapsed >= d.dialTimeout {
		t.Errorf("dial took %v; the expired address was dialed anyway", elapsed)
	}
}

func TestFallbackDialer_ForgetRemovesTheCacheEntry(t *testing.T) {
	path := tempCachePath(t)
	seedCache(t, path, map[string]netCacheEntry{
		"api.test":   {IP: blackholeIP, Until: time.Now().Add(time.Hour)},
		"other.test": {IP: "127.0.0.1", Until: time.Now().Add(time.Hour)},
	})

	d := newTestDialer()
	d.cachePath = path
	d.lookup = func(ctx context.Context, host string) ([]net.IP, error) {
		return nil, errors.New("no such host")
	}
	if _, err := d.DialContext(context.Background(), "tcp", "api.test:443"); err == nil {
		t.Fatal("DialContext succeeded; want the cached blackhole address to fail")
	}

	entries := readDiskCache(t, path)
	if _, ok := entries["api.test"]; ok {
		t.Error("the dead address is still cached; every later command would dial it first")
	}
	if got := entries["other.test"].IP; got != "127.0.0.1" {
		t.Errorf("other.test ip = %q; want 127.0.0.1 left alone", got)
	}
}

func TestFallbackDialer_UnusableCachePathIsHarmless(t *testing.T) {
	port := localListener(t)
	d := newTestDialer()
	d.cachePath = t.TempDir() // a directory: every read and write of it fails
	d.lookup = func(ctx context.Context, host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}

	conn, err := d.DialContext(context.Background(), "tcp", "api.test:"+port)
	if err != nil {
		t.Fatalf("DialContext: %v — an unusable cache must never break a command", err)
	}
	conn.Close()
	if got := d.known["api.test"]; got != "127.0.0.1" {
		t.Errorf("known[api.test] = %q; want the in-process memory to work regardless", got)
	}
}

// stubTransport answers requests from a script keyed on the attempt number.
type stubTransport struct {
	calls atomic.Int32
	fn    func(attempt int, req *http.Request) (*http.Response, error)
}

func (s *stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return s.fn(int(s.calls.Add(1)), req)
}

func stubResponse(status int, req *http.Request) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{}`)),
		Request:    req,
	}
}

func stubClient(t *testing.T, st *stubTransport) *Client {
	t.Helper()
	c := NewClient(&config.Config{APIHost: "https://api.test", Token: "jwt-x"})
	c.http.Transport = st
	return c
}

// shrinkBackoff keeps the retry tests fast without weakening them.
func shrinkBackoff(t *testing.T) {
	t.Helper()
	old := retryBackoff
	retryBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { retryBackoff = old })
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestClientDo_RetriesIdempotentTransportErrors(t *testing.T) {
	shrinkBackoff(t)
	cases := []struct {
		name string
		err  error
	}{
		{"timeout", timeoutError{}},
		{"unreachable", &ReachabilityError{Host: "api.test", Tried: []string{blackholeIP}, Last: timeoutError{}}},
		{"connection reset", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}},
		{"connection refused", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &stubTransport{fn: func(attempt int, req *http.Request) (*http.Response, error) {
				if attempt < 3 {
					return nil, tc.err
				}
				return stubResponse(http.StatusOK, req), nil
			}}
			req, err := http.NewRequest(http.MethodGet, "https://api.test/api/v1/projects", nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}

			resp, err := stubClient(t, st).do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			resp.Body.Close()
			if got := st.calls.Load(); got != 3 {
				t.Errorf("transport calls = %d; want 3 (two retries)", got)
			}
		})
	}
}

func TestClientDo_DoesNotRetry(t *testing.T) {
	shrinkBackoff(t)
	cases := []struct {
		name    string
		newReq  func() *http.Request
		respErr error
	}{
		{
			"post with a body",
			func() *http.Request {
				req, _ := http.NewRequest(http.MethodPost, "https://api.test/api/v1/auth/login", strings.NewReader(`{}`))
				return req
			},
			timeoutError{},
		},
		{
			"get with a body",
			func() *http.Request {
				req, _ := http.NewRequest(http.MethodGet, "https://api.test/api/v1/projects", strings.NewReader(`{}`))
				return req
			},
			timeoutError{},
		},
		{
			"error that is not a transport failure",
			func() *http.Request {
				req, _ := http.NewRequest(http.MethodGet, "https://api.test/api/v1/projects", nil)
				return req
			},
			errors.New("x509: certificate signed by unknown authority"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &stubTransport{fn: func(attempt int, req *http.Request) (*http.Response, error) {
				return nil, tc.respErr
			}}
			if _, err := stubClient(t, st).do(tc.newReq()); err == nil {
				t.Fatal("do succeeded; want the transport error")
			}
			if got := st.calls.Load(); got != 1 {
				t.Errorf("transport calls = %d; want 1", got)
			}
		})
	}
}

func TestClientDo_NeverRetriesAnHTTPStatus(t *testing.T) {
	shrinkBackoff(t)
	st := &stubTransport{fn: func(attempt int, req *http.Request) (*http.Response, error) {
		return stubResponse(http.StatusInternalServerError, req), nil
	}}
	req, _ := http.NewRequest(http.MethodGet, "https://api.test/api/v1/projects", nil)

	resp, err := stubClient(t, st).do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d; want 500 handed back untouched", resp.StatusCode)
	}
	if got := st.calls.Load(); got != 1 {
		t.Errorf("transport calls = %d; want 1 (a status is the caller's business)", got)
	}
}

func TestClientDo_StopsWhenTheContextIsDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := &stubTransport{fn: func(attempt int, req *http.Request) (*http.Response, error) {
		cancel()
		return nil, timeoutError{}
	}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.test/api/v1/projects", nil)

	start := time.Now()
	if _, err := stubClient(t, st).do(req); err == nil {
		t.Fatal("do succeeded; want the cancellation error")
	}
	if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
		t.Errorf("do took %v; want an immediate return instead of waiting out the backoff", elapsed)
	}
	if got := st.calls.Load(); got != 1 {
		t.Errorf("transport calls = %d; want 1 on a cancelled request", got)
	}
}

// TestNewHTTPClient_NoDeadlineOnTheWholeRequest pins the shape uploads and log
// streaming depend on: the dialer is ours, the handshake is bounded, and
// nothing puts a clock on the response itself.
func TestNewHTTPClient_NoDeadlineOnTheWholeRequest(t *testing.T) {
	client := newHTTPClient()
	if client.Timeout != 0 {
		t.Errorf("Client.Timeout = %v; want 0 (uploads and log streaming are unbounded)", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T; want *http.Transport", client.Transport)
	}
	if transport.ResponseHeaderTimeout != 0 {
		t.Errorf("ResponseHeaderTimeout = %v; want 0", transport.ResponseHeaderTimeout)
	}
	if transport.TLSHandshakeTimeout != 10*time.Second {
		t.Errorf("TLSHandshakeTimeout = %v; want 10s", transport.TLSHandshakeTimeout)
	}
	if transport.DialContext == nil || transport.Proxy == nil {
		t.Error("want both a fallback DialContext and the environment proxy")
	}
	if transport == http.DefaultTransport {
		t.Error("newHTTPClient mutated http.DefaultTransport instead of cloning it")
	}
}

func TestNewClient_UsesTheFallbackTransport(t *testing.T) {
	c := NewClient(&config.Config{APIHost: "https://api.test"})
	transport, ok := c.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T; want *http.Transport", c.http.Transport)
	}
	if transport.DialContext == nil {
		t.Error("NewClient built a client without the fallback dialer")
	}
}
