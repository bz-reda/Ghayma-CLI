package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
)

// fallbackDialer works around resolvers that hand out addresses the local
// network cannot reach (2026-09-12: an ISP whose DNS answered 188.114.96.5 /
// 188.114.97.5 for api.ghayma.tech, a Cloudflare prefix it could not route,
// while another edge of the same zone answered in 30 ms). Every system address
// is dialed with a short timeout; if all of them fail the host is re-resolved
// over DNS-over-HTTPS and those addresses are dialed too. The address that
// worked is remembered for the rest of the process.
type fallbackDialer struct {
	dialTimeout time.Duration
	lookup      func(ctx context.Context, host string) ([]net.IP, error)
	dohURLs     []string
	dohClient   *http.Client

	mu    sync.Mutex
	known map[string]string
}

func newFallbackDialer() *fallbackDialer {
	return &fallbackDialer{
		dialTimeout: 6 * time.Second,
		lookup: func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		},
		dohURLs: []string{
			"https://cloudflare-dns.com/dns-query",
			"https://dns.google/resolve",
		},
		// Plain client on purpose: resolving DoH through this dialer would recurse.
		dohClient: &http.Client{Timeout: 5 * time.Second},
		known:     map[string]string{},
	}
}

func (d *fallbackDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if net.ParseIP(host) != nil {
		return d.dial(ctx, network, host, port)
	}

	var tried []string
	var last error

	if ip := d.recall(host); ip != "" {
		conn, err := d.dial(ctx, network, ip, port)
		if err == nil {
			return conn, nil
		}
		d.forget(host)
		tried, last = append(tried, ip), err
	}

	ips, err := d.lookup(ctx, host)
	if err != nil {
		last = err
	}
	for _, ip := range ips {
		conn, err := d.tryAddress(ctx, network, host, ip.String(), port, &tried)
		if conn != nil {
			return conn, nil
		}
		if err != nil {
			last = err
		}
	}

	for _, dohURL := range d.dohURLs {
		for _, ip := range d.resolveDoH(ctx, dohURL, host) {
			conn, err := d.tryAddress(ctx, network, host, ip, port, &tried)
			if conn != nil {
				return conn, nil
			}
			if err != nil {
				last = err
			}
		}
	}

	return nil, &ReachabilityError{Host: host, Tried: tried, Last: last}
}

// tryAddress dials ip unless it was already tried, recording the attempt. A
// successful connection is remembered as the host's working address.
func (d *fallbackDialer) tryAddress(ctx context.Context, network, host, ip, port string, tried *[]string) (net.Conn, error) {
	for _, seen := range *tried {
		if seen == ip {
			return nil, nil
		}
	}
	*tried = append(*tried, ip)

	conn, err := d.dial(ctx, network, ip, port)
	if err != nil {
		return nil, err
	}
	d.remember(host, ip)
	return conn, nil
}

func (d *fallbackDialer) dial(ctx context.Context, network, ip, port string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: d.dialTimeout}
	return dialer.DialContext(ctx, network, net.JoinHostPort(ip, port))
}

// resolveDoH asks one DNS-over-HTTPS provider for the host's A records. Any
// failure yields no addresses: the caller moves on to the next provider.
func (d *fallbackDialer) resolveDoH(ctx context.Context, dohURL, host string) []string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dohURL+"?name="+url.QueryEscape(host)+"&type=A", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "application/dns-json")

	resp, err := d.dohClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var answer struct {
		Answer []struct {
			Type int    `json:"type"`
			Data string `json:"data"`
		} `json:"Answer"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&answer); err != nil {
		return nil
	}

	var ips []string
	for _, a := range answer.Answer {
		if a.Type == 1 && net.ParseIP(a.Data) != nil {
			ips = append(ips, a.Data)
		}
	}
	return ips
}

func (d *fallbackDialer) recall(host string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.known[host]
}

func (d *fallbackDialer) remember(host, ip string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.known[host] = ip
}

func (d *fallbackDialer) forget(host string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.known, host)
}

// ReachabilityError reports that every address known for a host refused or
// swallowed the connection, and names them so the user can see what their
// resolver handed out.
type ReachabilityError struct {
	Host  string
	Tried []string
	Last  error
}

const dnsHint = "Your DNS answers point at addresses this network cannot reach — try setting your DNS to 1.1.1.1 or 8.8.8.8, or check a VPN or firewall."

func (e *ReachabilityError) Error() string {
	if len(e.Tried) == 0 {
		if e.Last != nil {
			return fmt.Sprintf("cannot reach %s: no address resolved (%v). %s", e.Host, e.Last, dnsHint)
		}
		return fmt.Sprintf("cannot reach %s: no address resolved. %s", e.Host, dnsHint)
	}

	reason := "failed"
	var netErr net.Error
	if errors.As(e.Last, &netErr) && netErr.Timeout() {
		reason = "timed out"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "cannot reach %s: connection to %s %s", e.Host, e.Tried[0], reason)
	if len(e.Tried) > 1 {
		fmt.Fprintf(&b, " (also tried %s)", strings.Join(e.Tried[1:], ", "))
	}
	fmt.Fprintf(&b, ". %s", dnsHint)
	return b.String()
}

func (e *ReachabilityError) Unwrap() error { return e.Last }

// apiDialer is process-wide: the address that worked for the first request is
// reused by every client the command builds afterwards.
var apiDialer = newFallbackDialer()

// retryBackoff is the pause before each retry, and its length sets how many
// retries an idempotent request gets.
var retryBackoff = []time.Duration{500 * time.Millisecond, 1500 * time.Millisecond}

// newHTTPClient builds the client every API call goes through. It deliberately
// carries no Client.Timeout and no ResponseHeaderTimeout: source uploads and
// log streaming have to stay unbounded.
func newHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment
	transport.DialContext = apiDialer.DialContext
	transport.TLSHandshakeTimeout = 10 * time.Second
	return &http.Client{Transport: transport}
}

// do sends a request and retries it when the transport itself failed — a
// timeout, an unreachable address, a reset or refused connection. Only
// body-less GET and HEAD are retried, since replaying anything else could
// duplicate a write. An HTTP status is never retried: what a 5xx means is the
// caller's business.
func (c *Client) do(req *http.Request) (*http.Response, error) {
	attempts := 1
	if req.Body == nil && (req.Method == http.MethodGet || req.Method == http.MethodHead) {
		attempts = len(retryBackoff) + 1
	}

	var err error
	for attempt := range attempts {
		if attempt > 0 {
			select {
			case <-time.After(retryBackoff[attempt-1]):
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
		}

		var resp *http.Response
		resp, err = c.http.Do(req)
		if err == nil {
			return resp, nil
		}
		if !isRetryableTransportError(err) {
			return nil, err
		}
	}
	return nil, err
}

func isRetryableTransportError(err error) bool {
	var unreachable *ReachabilityError
	if errors.As(err, &unreachable) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED)
}
