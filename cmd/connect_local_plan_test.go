package cmd

import (
	"strings"
	"testing"

	"paas-cli/internal/api"
)

func allPortsFree(int) bool { return true }

func TestPlanListeners_NumbersFromEachKindsBase(t *testing.T) {
	targets := []api.TunnelTarget{
		{ID: "t1", Kind: "postgres", Name: "pg-one", Host: "pg-one.databases.svc.cluster.local", Port: 5432},
		{ID: "t2", Kind: "mongodb", Name: "mg", Host: "mg.databases.svc.cluster.local", Port: 27017},
		{ID: "t3", Kind: "postgres", Name: "pg-two", Host: "pg-two.databases.svc.cluster.local", Port: 5432},
	}

	got := planListeners(targets, allPortsFree)
	want := []string{"127.0.0.1:15432", "127.0.0.1:15017", "127.0.0.1:15433"}
	if len(got) != len(want) {
		t.Fatalf("planListeners returned %d listeners; want %d", len(got), len(want))
	}
	for i, l := range got {
		if l.Addr != want[i] || l.Target.ID != targets[i].ID {
			t.Errorf("listener %d = %s for %s; want %s for %s", i, l.Addr, l.Target.ID, want[i], targets[i].ID)
		}
	}
}

func TestPlanListeners_SkipsBusyPorts(t *testing.T) {
	targets := []api.TunnelTarget{
		{ID: "t1", Kind: "postgres", Name: "pg", Host: "pg.databases.svc.cluster.local", Port: 5432},
		{ID: "t2", Kind: "postgres", Name: "pg2", Host: "pg2.databases.svc.cluster.local", Port: 5432},
	}
	// A local postgres already holds 15432, something else holds 15433.
	free := func(port int) bool { return port != 15432 && port != 15433 }

	got := planListeners(targets, free)
	if len(got) != 2 || got[0].Addr != "127.0.0.1:15434" || got[1].Addr != "127.0.0.1:15435" {
		t.Errorf("listeners = %+v; want the busy ports skipped", got)
	}
}

func TestRewriteEnvForLocal_PointsDatabaseURLsAtTheListeners(t *testing.T) {
	ls := []localListener{
		{Target: api.TunnelTarget{ID: "t1", Kind: "postgres", Name: "pg", Host: "pg-x.databases.svc.cluster.local", Port: 5432}, Addr: "127.0.0.1:15432"},
		{Target: api.TunnelTarget{ID: "t2", Kind: "mongodb", Name: "mg", Host: "mg-y.databases.svc.cluster.local", Port: 27017}, Addr: "127.0.0.1:15017"},
	}
	env := map[string]string{
		"DATABASE_URL":           "postgresql://u:p@pg-x.databases.svc.cluster.local:5432/db",
		"DATABASE_URL_ANALYTICS": "postgresql://u:p@pg-x.databases.svc.cluster.local:5432/analytics?sslmode=disable",
		"MONGODB_URI":            "mongodb://u:p@mg-y.databases.svc.cluster.local:27017/db?authSource=admin&replicaSet=rs0&directConnection=true",
		"GHAYMA_API_KEY":         "gsk_secret",
	}

	out, changed := rewriteEnvForLocal(env, ls)

	if out["DATABASE_URL"] != "postgresql://u:p@127.0.0.1:15432/db" {
		t.Errorf("DATABASE_URL = %q", out["DATABASE_URL"])
	}
	if out["DATABASE_URL_ANALYTICS"] != "postgresql://u:p@127.0.0.1:15432/analytics?sslmode=disable" {
		t.Errorf("per-database variant = %q; want it rewritten too", out["DATABASE_URL_ANALYTICS"])
	}
	if out["MONGODB_URI"] != "mongodb://u:p@127.0.0.1:15017/db?authSource=admin&replicaSet=rs0&directConnection=true" {
		t.Errorf("MONGODB_URI = %q; want only host:port replaced", out["MONGODB_URI"])
	}
	if out["GHAYMA_API_KEY"] != "gsk_secret" {
		t.Errorf("unrelated value changed: %q", out["GHAYMA_API_KEY"])
	}
	if strings.Join(changed, ",") != "DATABASE_URL,DATABASE_URL_ANALYTICS,MONGODB_URI" {
		t.Errorf("changed = %v; want the three rewritten keys in key order", changed)
	}
	if env["DATABASE_URL"] != "postgresql://u:p@pg-x.databases.svc.cluster.local:5432/db" {
		t.Errorf("the input map must not be modified, got %q", env["DATABASE_URL"])
	}
}

func TestRewriteEnvForLocal_LeavesEverythingElseAlone(t *testing.T) {
	ls := []localListener{
		{Target: api.TunnelTarget{ID: "t1", Kind: "postgres", Name: "pg", Host: "pg-x.databases.svc.cluster.local", Port: 5432}, Addr: "127.0.0.1:15432"},
	}
	// Same host, another port: not this target.
	env := map[string]string{"OTHER": "postgresql://u:p@pg-x.databases.svc.cluster.local:6432/db"}

	out, changed := rewriteEnvForLocal(env, ls)
	if out["OTHER"] != env["OTHER"] || len(changed) != 0 {
		t.Errorf("out, changed = %v, %v; want the value untouched", out, changed)
	}
}

func TestLocalHeader_IsAllCommentsAndNamesEveryListener(t *testing.T) {
	ls := []localListener{
		{Target: api.TunnelTarget{Kind: "postgres", Name: "my-postgres"}, Addr: "127.0.0.1:15432"},
		{Target: api.TunnelTarget{Kind: "mongodb", Name: "events"}, Addr: "127.0.0.1:15017"},
	}

	header := localHeader("shop", "main", ls)
	for _, line := range strings.Split(strings.TrimRight(header, "\n"), "\n") {
		if !strings.HasPrefix(line, "#") {
			t.Errorf("header line %q is not a comment", line)
		}
	}
	for _, want := range []string{"shop", "main", "my-postgres", "127.0.0.1:15432", "events", "127.0.0.1:15017", "connect --local"} {
		if !strings.Contains(header, want) {
			t.Errorf("header must mention %q, got:\n%s", want, header)
		}
	}
}
