package cmd

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// cronsFormField forwards the `crons` key verbatim when present and omits it
// (empty) when absent — the presence gate the backend relies on (spec §7).
func TestCronsFormField(t *testing.T) {
	// Absent key → empty → deploy never touches jobs.
	if got := cronsFormField(nil); got != "" {
		t.Errorf("nil crons = %q; want empty (omit field)", got)
	}
	// Present-but-empty array is authoritative → must be forwarded, not omitted.
	if got := cronsFormField(json.RawMessage("[]")); got != "[]" {
		t.Errorf("empty-array crons = %q; want []", got)
	}
	// A populated array is forwarded verbatim.
	raw := `[{"name":"nightly","schedule":"0 3 * * *","path":"/api/cron"}]`
	if got := cronsFormField(json.RawMessage(raw)); got != raw {
		t.Errorf("crons = %q; want verbatim %q", got, raw)
	}
}

// The projectConfig struct captures `crons` as raw JSON, so both the absent and
// present cases are distinguishable end-to-end from a .ghayma.json body.
func TestProjectConfigCronsPresence(t *testing.T) {
	var absent projectConfig
	if err := json.Unmarshal([]byte(`{"project_id":"p1","name":"app"}`), &absent); err != nil {
		t.Fatal(err)
	}
	if got := cronsFormField(absent.Crons); got != "" {
		t.Errorf("no crons key → %q; want empty", got)
	}

	var present projectConfig
	body := `{"project_id":"p1","name":"app","crons":[{"name":"n","schedule":"0 3 * * *"}]}`
	if err := json.Unmarshal([]byte(body), &present); err != nil {
		t.Fatal(err)
	}
	got := cronsFormField(present.Crons)
	if !strings.Contains(got, `"name":"n"`) || !strings.HasPrefix(got, "[") {
		t.Errorf("crons key → %q; want the raw array", got)
	}
}

// Source-pin: the deploy path must both read the crons key and send it as a form
// field. Guards against a refactor silently dropping the config-as-code wiring.
func TestDeploySendsCronsField(t *testing.T) {
	deploy, err := os.ReadFile("deploy.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(deploy), "Crons:           cronsFormField(projCfg.Crons)") {
		t.Error("deploy.go must set bc.Crons from the project config's crons key")
	}

	client, err := os.ReadFile("../internal/api/client.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(client), `WriteField("crons"`) {
		t.Error("client.go Deploy must send the crons form field")
	}
}
