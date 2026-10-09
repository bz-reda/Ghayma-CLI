package cmd

import (
	"strings"
	"testing"
)

// `storage info` shows the endpoint and public URL the server reports for the
// bucket; the composed hosts are only a fallback for a backend that sends none.

func TestStorageInfo_HostsFromTheBackend(t *testing.T) {
	row := strings.Replace(uploadsBucket, `"external_access":false`, `"external_access":true,"is_public":true,"public_url":"https://uploads-b1c2d3e4.web.staging.example"`, 1)
	ts, _ := detailsStub(t, "/api/v1/storage", `{"buckets":[`+row+`]}`, `{"connections":[]}`)
	cliHome(t, ts.URL)

	out := runCLI(t, t.TempDir(), "storage", "info", "uploads")

	for _, want := range []string{
		"   Endpoint:   https://storage.staging.example\n",
		"   Public URL: https://uploads-b1c2d3e4.web.staging.example\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "ghayma.tech") {
		t.Errorf("output composes a host the server did not send:\n%s", out)
	}
}

func TestStorageInfo_FallsBackWithoutBackendHosts(t *testing.T) {
	row := strings.Replace(uploadsBucket, `"endpoint":"https://storage.staging.example",`, "", 1)
	row = strings.Replace(row, `"external_access":false`, `"external_access":true`, 1)
	ts, _ := detailsStub(t, "/api/v1/storage", `{"buckets":[`+row+`]}`, `{"connections":[]}`)
	cliHome(t, ts.URL)

	out := runCLI(t, t.TempDir(), "storage", "info", "uploads")

	for _, want := range []string{
		"   Endpoint:   https://s3.ghayma.tech\n",
		"   Public URL: https://uploads-b1c2d3e4.web.ghayma.tech\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
