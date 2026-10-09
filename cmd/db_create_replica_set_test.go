package cmd

import (
	"strings"
	"testing"
)

// No shared credentials, part 4: a standalone MongoDB cannot give each app its
// own database user, so `db create --replica-set=false` is refused before any
// request, and the create body never carries replica_set.

const mongoCreated = `{"database":{"id":"d9","name":"docs","type":"mongodb","status":"provisioning","replica_set":true,"port":27017}}`

func TestDBCreate_ReplicaSetFalseIsRefused(t *testing.T) {
	stub := createStub(t, shopSites, mongoCreated)
	forceStdin(t, true)
	noSiteQuestions(t)
	cliHome(t, stub.URL)

	out := runCLI(t, linkedDir(t), "db", "create", "docs", "--type", "mongodb", "--replica-set=false", "--no-connect")

	want := "❌ MongoDB always runs as a single-node replica set: a standalone instance cannot give each app its own database user. Leave --replica-set out.\n"
	if out != want || lastExitCode != 1 {
		t.Fatalf("exit=%d output = %q; want %q", lastExitCode, out, want)
	}
	if calls := stub.seen(); len(calls) != 0 {
		t.Errorf("requests = %v; the refusal must come before any call", calls)
	}
}

func TestDBCreate_ReplicaSetTrueSendsNothing(t *testing.T) {
	stub := createStub(t, shopSites, mongoCreated)
	forceStdin(t, true)
	noSiteQuestions(t)
	cliHome(t, stub.URL)

	out := runCLI(t, linkedDir(t), "db", "create", "docs", "--type", "mongodb", "--replica-set=true", "--no-connect")

	if !stub.created() {
		t.Fatalf("no create went out:\n%s", out)
	}
	if _, sent := stub.body["replica_set"]; sent {
		t.Errorf("body = %v; replica_set must never be sent", stub.body)
	}
	if !strings.Contains(out, "   Mode:    replica set (rs0) — transactions supported\n") {
		t.Errorf("output = %s; want the replica-set mode line", out)
	}
}

func TestDBCreate_WithoutReplicaSetSendsNothing(t *testing.T) {
	stub := createStub(t, shopSites, mongoCreated)
	forceStdin(t, true)
	noSiteQuestions(t)
	cliHome(t, stub.URL)

	runCLI(t, linkedDir(t), "db", "create", "docs", "--type", "mongodb", "--no-connect")

	if _, sent := stub.body["replica_set"]; sent || !stub.created() {
		t.Errorf("created=%v body = %v; want a create without replica_set", stub.created(), stub.body)
	}
}

func TestDBCreateHelp_HidesReplicaSet(t *testing.T) {
	out := helpText(t, dbCreateCmd)
	if !strings.Contains(out, "--type") {
		t.Fatalf("help = %q; want the flag listing", out)
	}
	if strings.Contains(out, "replica-set") || strings.Contains(out, "standalone") {
		t.Errorf("db create --help still offers --replica-set:\n%s", out)
	}
}
