package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// No shared credentials, part 4: `db credentials` shows where a database is and
// what each connected site receives, never a password. It reads the database
// list and the project's connections; the retired /credentials route is never
// called.

// detailsStub serves one listing (the databases or the buckets) and project
// p1's connections. A call to a retired credentials or rotate route fails the
// test. It returns every "METHOD /path" it was asked for.
func detailsStub(t *testing.T, listPath, listBody, connections string) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/credentials") || strings.HasSuffix(r.URL.Path, "/rotate"):
			t.Errorf("%s %s must never be called", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusGone)
			io.WriteString(w, `{"error":"retired","code":"shared_credentials_retired"}`)
		case r.Method == http.MethodGet && r.URL.Path == listPath:
			io.WriteString(w, listBody)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/p1/connections":
			io.WriteString(w, connections)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"unexpected `+r.Method+" "+r.URL.Path+`"}`)
		}
	}))
	t.Cleanup(ts.Close)
	return ts, &calls
}

const shopDB = `{"id":"d1","name":"shop","type":"postgres","version":"16","status":"running","host":"pg-shop.pdb-p1.svc.cluster.local","port":5432,"db_name":"shop","username":"u_3f1a2b3c","project_id":"p1"}`

const shopDBConnections = `{"connections":[
	{"site_id":"s1","site_slug":"main","kind":"database","resource_id":"d1","resource_name":"shop","level":"connect","env_names":["DATABASE_URL","DATABASE_URL_SHOP"]},
	{"site_id":"s2","site_slug":"admin-panel","kind":"database","resource_id":"d1","resource_name":"shop","level":"read-only","env_names":["DATABASE_URL_SHOP"]},
	{"site_id":"s1","site_slug":"main","kind":"database","resource_id":"d2","resource_name":"analytics","level":"connect","env_names":["DATABASE_URL_ANALYTICS"]}]}`

func TestDBCredentials_ShowsConnectionDetailsWithoutASecret(t *testing.T) {
	ts, calls := detailsStub(t, "/api/v1/databases", `{"databases":[`+shopDB+`]}`, shopDBConnections)
	cliHome(t, ts.URL)

	out := runCLI(t, t.TempDir(), "db", "credentials", "shop")

	want := "🔌 Connection details for 'shop' (postgres)\n" +
		"\n" +
		"   Host:      pg-shop.pdb-p1.svc.cluster.local\n" +
		"   Port:      5432\n" +
		"   Database:  shop\n" +
		"\n" +
		"   Connected sites and the variables they receive:\n" +
		"     main         connect    DATABASE_URL, DATABASE_URL_SHOP\n" +
		"     admin-panel  read-only  DATABASE_URL_SHOP\n" +
		"\n" +
		"   No password is shown: each site has its own credential, delivered in these variables.\n" +
		"   From your laptop:     ghayma connect --local\n" +
		"   From outside Ghayma:  ghayma access add database shop --name <principal>\n"
	if out != want {
		t.Fatalf("output:\n%s\nwant:\n%s", out, want)
	}
	for _, never := range []string{"password:", "Password", "u_", "DATABASE_URL_ANALYTICS"} {
		if strings.Contains(out, never) {
			t.Errorf("output must not contain %q:\n%s", never, out)
		}
	}
	if lastExitCode != 0 {
		t.Errorf("exit code = %d; want 0", lastExitCode)
	}
	if got := strings.Join(*calls, ", "); got != "GET /api/v1/databases, GET /api/v1/projects/p1/connections" {
		t.Errorf("requests = %s; want the database list then the project's connections", got)
	}
}

func TestDBCredentials_NotConnectedToAnySite(t *testing.T) {
	ts, _ := detailsStub(t, "/api/v1/databases", `{"databases":[`+shopDB+`]}`,
		`{"connections":[{"site_id":"s1","site_slug":"main","kind":"bucket","resource_id":"d1","resource_name":"shop","level":"read-write"}]}`)
	cliHome(t, ts.URL)

	out := runCLI(t, t.TempDir(), "db", "credentials", "shop")

	if !strings.Contains(out, "\n   Not connected to any site. Connect one with: ghayma connect database shop --site <slug>\n") {
		t.Errorf("output = %s; want the not-connected line", out)
	}
	if strings.Contains(out, "Connected sites and the variables they receive") {
		t.Errorf("output = %s; an unconnected database has no sites block", out)
	}
}

func TestDBCredentials_DatabaseWithNoProject(t *testing.T) {
	row := strings.Replace(shopDB, `,"project_id":"p1"`, "", 1)
	ts, calls := detailsStub(t, "/api/v1/databases", `{"databases":[`+row+`]}`, `{"connections":[]}`)
	cliHome(t, ts.URL)

	out := runCLI(t, t.TempDir(), "db", "credentials", "shop")

	if out != "❌ Database 'shop' belongs to no project.\n" || lastExitCode != 1 {
		t.Fatalf("exit=%d output = %q", lastExitCode, out)
	}
	if len(*calls) != 1 {
		t.Errorf("requests = %v; want only the database list", *calls)
	}
}

// A Valkey's apps get REDIS_URL / VALKEY_URL; it has no database name to show
// and no access from outside Ghayma (it refuses one).
func TestDBCredentials_Valkey(t *testing.T) {
	valkey := `{"id":"d1","name":"cache","type":"valkey","status":"running","valkey_mode":"cache","host":"vk-cache-d1.pdb-p1.svc.cluster.local","port":6379,"db_name":"0","username":"ghayma","project_id":"p1"}`
	conns := `{"connections":[{"site_id":"s1","site_slug":"main","kind":"database","resource_id":"d1","resource_name":"cache","level":"connect","env_names":["REDIS_URL","VALKEY_URL"]}]}`
	ts, calls := detailsStub(t, "/api/v1/databases", `{"databases":[`+valkey+`]}`, conns)
	cliHome(t, ts.URL)

	out := runCLI(t, linkedDir(t), "db", "credentials", "cache")

	want := "🔌 Connection details for 'cache' (valkey)\n" +
		"\n" +
		"   Host:      vk-cache-d1.pdb-p1.svc.cluster.local\n" +
		"   Port:      6379\n" +
		"\n" +
		"   Connected sites and the variables they receive:\n" +
		"     main  connect  REDIS_URL, VALKEY_URL\n" +
		"\n" +
		"   No password is shown: each site has its own credential, delivered in these variables.\n" +
		"   From your laptop:     ghayma connect --local\n"
	if out != want || lastExitCode != 0 {
		t.Fatalf("exit=%d output:\n%s\nwant:\n%s", lastExitCode, out, want)
	}
	if strings.Contains(strings.Join(*calls, " "), "/credentials") {
		t.Errorf("requests = %v; the credentials route must not be called", *calls)
	}
}
