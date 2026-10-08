package cmd

import (
	"strings"
	"testing"

	"paas-cli/internal/api"
)

// Valkey (plan 1b): created with the existing --type flag, its mode chosen with
// --mode, and the engines and modes the API would refuse are refused here
// before any request.

func TestValidateCreateEngine(t *testing.T) {
	cases := []struct {
		typ, mode, wantErr string
	}{
		{"postgres", "", ""},
		{"mongodb", "", ""},
		{"valkey", "", ""},
		{"valkey", "cache", ""},
		{"valkey", "store", ""},
		{"redis", "", "Create a Valkey database instead"},
		{"mysql", "", `unknown database type "mysql"`},
		{"postgres", "store", "--mode applies to Valkey only"},
		{"valkey", "persist", `--mode must be cache or store`},
	}
	for _, c := range cases {
		err := validateCreateEngine(c.typ, c.mode)
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s/%s: unexpected %v", c.typ, c.mode, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%s/%s: err = %v, want %q", c.typ, c.mode, err, c.wantErr)
		}
	}
}

func TestDBStatusText_ShowsWhyAValkeyFailed(t *testing.T) {
	got := dbStatusText(api.DatabaseInfo{Status: "error", StatusMessage: "CrashLoopBackOff:\n exit 1"})
	if got != "error · CrashLoopBackOff: exit 1" {
		t.Fatalf("got %q", got)
	}
	if got := dbStatusText(api.DatabaseInfo{Status: "running", StatusMessage: "stale"}); got != "running" {
		t.Fatalf("a running database shows no message, got %q", got)
	}
}

func TestDBCreate_ValkeySendsModeAndPrintsIt(t *testing.T) {
	stub := createStub(t, shopSites, `{"database":{"id":"d9","name":"cache","type":"valkey","version":"9.1","status":"provisioning","valkey_mode":"store","host":"vk-cache-d9.pdb-p.svc.cluster.local","port":6379}}`)
	forceStdin(t, true)
	noSiteQuestions(t)
	cliHome(t, stub.URL)
	out := runCLI(t, linkedDir(t), "db", "create", "cache", "--type", "valkey", "--mode", "store", "--no-connect")
	if string(stub.body["mode"]) != `"store"` || string(stub.body["type"]) != `"valkey"` {
		t.Fatalf("body = %v", stub.body)
	}
	if !strings.Contains(out, "Mode:") || !strings.Contains(out, "store") {
		t.Fatalf("output = %s", out)
	}
	for _, want := range []string{
		"   Mode:    store — never evicts; writes fail when memory is full. Snapshot plus append-only file every second.\n",
		"   Connected apps receive REDIS_URL and VALKEY_URL at runtime (not during builds).\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// A create without --mode sends none, so the server applies its cache default.
func TestDBCreate_ValkeyWithoutModeSendsNone(t *testing.T) {
	stub := createStub(t, shopSites, `{"database":{"id":"d9","name":"cache","type":"valkey","status":"provisioning","valkey_mode":"cache","port":6379}}`)
	forceStdin(t, true)
	noSiteQuestions(t)
	cliHome(t, stub.URL)
	out := runCLI(t, linkedDir(t), "db", "create", "cache", "--type", "valkey", "--no-connect")
	if _, sent := stub.body["mode"]; sent {
		t.Errorf("mode sent without --mode: %s", stub.body["mode"])
	}
	if !strings.Contains(out, "   Mode:    cache — evicts the least-recently-used keys when memory is full. Snapshots only.\n") {
		t.Errorf("missing the cache mode line:\n%s", out)
	}
}

func TestDBCreate_RefusesRedisAndStrayModeLocally(t *testing.T) {
	stub := createStub(t, shopSites, `{}`)
	forceStdin(t, true)
	noSiteQuestions(t)
	cliHome(t, stub.URL)
	out := runCLI(t, linkedDir(t), "db", "create", "r", "--type", "redis", "--no-connect")
	if !strings.Contains(out, "Create a Valkey database instead") || lastExitCode != 1 || stub.body != nil {
		t.Fatalf("exit=%d body=%v out=%s", lastExitCode, stub.body, out)
	}
	out = runCLI(t, linkedDir(t), "db", "create", "p", "--mode", "store", "--no-connect")
	if !strings.Contains(out, "--mode applies to Valkey only") || lastExitCode != 1 {
		t.Fatalf("exit=%d out=%s", lastExitCode, out)
	}
	if calls := stub.seen(); len(calls) != 0 {
		t.Errorf("the refusals must come before any call, got %v", calls)
	}
}

// A Valkey's info shows its mode and never the platform user nor a shrink
// target (its disk cannot shrink); postgres keeps its database and username lines.
func TestDBInfo_ValkeyShowsModeNotThePlatformUser(t *testing.T) {
	valkey := `{"id":"d9","name":"cache","type":"valkey","version":"9.1","status":"running","valkey_mode":"store","host":"vk-cache-d9.pdb-p.svc.cluster.local","port":6379,"db_name":"0","username":"ghayma","storage_mb":1024,"disk_used_bytes":1288490188,"min_disk_gb":2,"cpu_limit":"500m","memory_limit":"512Mi","project_id":"p1"}`
	ts, _ := newDBStub(t, valkey+","+pgRow("running", 10, ""), 200, "")
	cliHome(t, ts.URL)

	out := runCLI(t, t.TempDir(), "db", "info", "cache")
	for _, want := range []string{
		"   Type:       valkey 9.1\n",
		"   Mode:       store — never evicts; writes fail when memory is full. Snapshot plus append-only file every second.\n",
		"   Users:      each connected app has its own user (REDIS_URL / VALKEY_URL)\n",
		"   Disk used:  1.2 GB\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, never := range []string{"   Username:", "ghayma", "   Database:", "smallest disk"} {
		if strings.Contains(out, never) {
			t.Errorf("a Valkey's info must not show %q:\n%s", never, out)
		}
	}

	out = runCLI(t, t.TempDir(), "db", "info", "pg")
	if strings.Contains(out, "Mode:") || strings.Contains(out, "Users:") {
		t.Errorf("postgres info gained Valkey lines:\n%s", out)
	}
}
