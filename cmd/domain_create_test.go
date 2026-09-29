package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The address a domain's A record points at is the platform's, and it moves:
// the CLI printed the retired Hetzner address for three weeks after the
// 2026-09-06 cutover. It comes from the server's dns_target, never a literal.

// domainStub accepts the domain create and answers the status check with
// check (the JSON body) and checkStatus.
func domainStub(t *testing.T, checkStatus int, check string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/domains":
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"id":"d1","domain":"my-app.com","pending_dns":true}`)
		case r.URL.Path == "/api/v1/domains/check":
			w.WriteHeader(checkStatus)
			io.WriteString(w, check)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"unexpected `+r.URL.Path+`"}`)
		}
	}))
	t.Cleanup(ts.Close)
	return ts, &seen
}

func TestDomainCreate_PointsAtTheServersAddress(t *testing.T) {
	ts, seen := domainStub(t, http.StatusOK,
		`{"domain":"my-app.com","dns_target":"203.0.113.10","dns":{"ok":false,"resolved_ips":[],"reason":"the hostname has no address record yet"},"ssl":{"ok":false}}`)
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, linkedDir(t), "domain", "create", "my-app.com")

	if !containsPath(*seen, "GET /api/v1/domains/check?domain=my-app.com") {
		t.Errorf("requests = %v; want the status check for the new domain", *seen)
	}
	if !strings.Contains(out, "Add an A record in your DNS: my-app.com → 203.0.113.10") {
		t.Errorf("output = %q; want the server's dns_target as the A record", out)
	}
	if !strings.Contains(out, "Right now: the hostname has no address record yet.") {
		t.Errorf("output = %q; want the server's reason", out)
	}
	if strings.Contains(out, "Redeploy") {
		t.Errorf("output = %q; attaching a domain needs no redeploy", out)
	}
}

func TestDomainCreate_DNSAlreadyHereSaysSo(t *testing.T) {
	ts, _ := domainStub(t, http.StatusOK,
		`{"domain":"my-app.com","dns_target":"203.0.113.10","dns":{"ok":true,"resolved_ips":["203.0.113.10"]},"ssl":{"ok":false}}`)
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, linkedDir(t), "domain", "create", "my-app.com")

	if !strings.Contains(out, "DNS already points here") {
		t.Errorf("output = %q; want the domain reported as pointing here", out)
	}
	if strings.Contains(out, "Add an A record") {
		t.Errorf("output = %q; no A record to add when DNS already points here", out)
	}
}

// A failed check must not fall back to an address the CLI knows: any literal
// goes stale the next time the platform moves.
func TestDomainCreate_FailedCheckPrintsNoAddress(t *testing.T) {
	ts, _ := domainStub(t, http.StatusInternalServerError, `{"error":"server PUBLIC_IP not configured"}`)
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, linkedDir(t), "domain", "create", "my-app.com")

	if !strings.Contains(out, "✅ Domain 'my-app.com' added") {
		t.Errorf("output = %q; the domain was added", out)
	}
	if ip := regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}\b`).FindString(out); ip != "" {
		t.Errorf("output = %q; printed %s although the server named no address", out, ip)
	}
	if !strings.Contains(out, "Dashboard") {
		t.Errorf("output = %q; want the user sent to where the address is shown", out)
	}
	if lastExitCode != 0 {
		t.Errorf("exit code = %d; want 0, the domain was added", lastExitCode)
	}
}

// TestNoPlatformAddressLiteral keeps the platform's addresses, past and
// present, out of the CLI's code.
func TestNoPlatformAddressLiteral(t *testing.T) {
	for _, dir := range []string{".", "../internal/api", "../internal/config"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, addr := range []string{"65.109.68.181", "212.83.160.10"} {
				if strings.Contains(string(src), addr) {
					t.Errorf("%s hard-codes the platform address %s; read dns_target from the API", f, addr)
				}
			}
		}
	}
}
