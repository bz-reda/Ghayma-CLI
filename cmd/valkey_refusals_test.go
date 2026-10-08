package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"paas-cli/internal/api"
)

// Valkey answers several database commands differently: no shared credential,
// no public access yet, a disk that cannot shrink yet, and nothing connects
// before it runs. Each refusal must point at what does work.

type stubReply struct {
	status int
	body   string
}

const valkeyListRow = `{"id":"d1","name":"cache","type":"valkey","status":"running","valkey_mode":"cache","disk_gb":2,"project_id":"p1"}`

// dbListStub serves the database list and answers each extra route, keyed by
// "METHOD /path" or by "/path", with its reply; anything else is a 404. It
// returns every "METHOD /path" it was asked for.
func dbListStub(t *testing.T, list string, extra map[string]stubReply) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/databases" {
			io.WriteString(w, `{"databases":[`+list+`]}`)
			return
		}
		reply, ok := extra[r.Method+" "+r.URL.Path]
		if !ok {
			reply, ok = extra[r.URL.Path]
		}
		if !ok {
			reply = stubReply{http.StatusNotFound, `{"error":"unexpected ` + r.Method + " " + r.URL.Path + `"}`}
		}
		w.WriteHeader(reply.status)
		io.WriteString(w, reply.body)
	}))
	t.Cleanup(ts.Close)
	return ts, &calls
}

func valkeyListStub(t *testing.T, extra map[string]stubReply) (*httptest.Server, *[]string) {
	t.Helper()
	return dbListStub(t, valkeyListRow, extra)
}

const notRunningBody = `{"error":"the Valkey database is not running yet; connect the site once it is running (database cache is provisioning)","code":"database_not_running"}`

func TestDBCredentials_ValkeyNeedsNoRequest(t *testing.T) {
	ts, calls := valkeyListStub(t, nil)
	cliHome(t, ts.URL)
	out := runCLI(t, linkedDir(t), "db", "credentials", "cache")
	if strings.Contains(strings.Join(*calls, " "), "/credentials") || lastExitCode != 0 {
		t.Fatalf("exit=%d calls=%v out=%s", lastExitCode, *calls, out)
	}
	for _, want := range []string{
		"cache is a Valkey database: it has no shared credential. Each connected app has its own user in REDIS_URL / VALKEY_URL.",
		"ghayma connections", "ghayma env pull", "ghayma connect --local",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("out = %s; want %q", out, want)
		}
	}
}

func TestDBRotate_ValkeyRefusedBeforeThePrompt(t *testing.T) {
	ts, calls := valkeyListStub(t, nil)
	cliHome(t, ts.URL)
	forceStdin(t, true)
	out := runCLI(t, linkedDir(t), "db", "rotate", "cache")
	if lastExitCode != 1 || strings.Contains(strings.Join(*calls, " "), "/rotate") || strings.Contains(out, "Continue?") {
		t.Fatalf("exit=%d calls=%v out=%s", lastExitCode, *calls, out)
	}
	if !strings.Contains(out, "cache is a Valkey database: there is no shared password to rotate. Rotate one app's user with: ghayma connections rotate database cache --site <site>") {
		t.Fatalf("out = %s", out)
	}
}

func TestDBResize_ValkeyShrinkRefusedLocally(t *testing.T) {
	ts, calls := valkeyListStub(t, nil)
	cliHome(t, ts.URL)
	out := runCLI(t, linkedDir(t), "db", "resize", "cache", "--disk-gb", "1")
	if lastExitCode != 1 || strings.Contains(strings.Join(*calls, " "), "PATCH") {
		t.Fatalf("exit=%d calls=%v out=%s", lastExitCode, *calls, out)
	}
	if !strings.Contains(out, "A Valkey disk can grow, but it cannot shrink yet. cache keeps its 2 GB disk.") {
		t.Fatalf("out = %s", out)
	}
}

// The list read before the request is stale here (the disk already grew), so
// only the server can refuse the shrink.
func TestDBResize_ValkeyServerShrinkRefusalReadsTheSame(t *testing.T) {
	ts, _ := valkeyListStub(t, map[string]stubReply{
		"PATCH /api/v1/databases/d1/tier": {http.StatusConflict, `{"code":"shrink_unsupported","error":"this database's disk can grow, but shrinking it is not available yet"}`},
	})
	cliHome(t, ts.URL)
	out := runCLI(t, linkedDir(t), "db", "resize", "cache", "--disk-gb", "3")
	if lastExitCode != 1 || !strings.Contains(out, "A Valkey disk can grow, but it cannot shrink yet. cache keeps its 2 GB disk.") {
		t.Fatalf("exit=%d out=%s", lastExitCode, out)
	}
}

func TestDBResize_HelpSaysValkeyCannotShrink(t *testing.T) {
	help := dbResizeCmd.Long + dbResizeCmd.Flags().Lookup("disk-gb").Usage
	for _, want := range []string{"A Valkey disk cannot shrink yet.", "Postgres and MongoDB"} {
		if !strings.Contains(help, want) {
			t.Errorf("help = %q; want %q", help, want)
		}
	}
}

func TestRotateFailure_NotRunningIsNotASharedCredential(t *testing.T) {
	msg := rotateFailure(&api.APIError{Status: 409, Code: api.CodeDatabaseNotRunning, Message: "the Valkey database is not running yet; connect the site once it is running"}, "database", "cache")
	if strings.Contains(msg, "shared credential") || strings.Contains(msg, "ghayma db rotate") || !strings.Contains(msg, "not running") {
		t.Fatalf("msg = %q", msg)
	}
}

func TestRotateConnection_ValkeyNotRunning(t *testing.T) {
	code := stubExit(t)
	ts, _ := rotateStub(t, http.StatusConflict, notRunningBody)
	client, target := rotateTarget(ts.URL)
	out := captureStdout(t, func() {
		rotateConnection(client, target, "database", "pg-main", true, readAnswer)
	})
	if *code != 1 || strings.Contains(out, "shared credential") || !strings.Contains(out, "pg-main is not running yet, so no app can connect to it. Wait until ghayma db info pg-main shows running, then run this again.") {
		t.Fatalf("exit=%d out=%s", *code, out)
	}
}

func TestAccessAdd_ValkeyPointsToTheTunnel(t *testing.T) {
	ts, _ := valkeyListStub(t, map[string]stubReply{
		"POST /api/v1/projects/p1/databases/d1/access": {http.StatusBadRequest, `{"error":"public access for this database engine is not available yet; use the ghayma tunnel","code":"public_access_unavailable"}`},
	})
	cliHome(t, ts.URL)
	out := runCLI(t, linkedDir(t), "access", "add", "database", "cache", "--name", "laptop")
	if lastExitCode != 1 || !strings.Contains(out, "public access for this database engine is not available yet; use the ghayma tunnel") || !strings.Contains(out, "Reach it from your machine with: ghayma connect --local") {
		t.Fatalf("exit=%d out=%s", lastExitCode, out)
	}
}

func TestAccessList_ValkeySaysThereIsNoPublicAccess(t *testing.T) {
	ts, _ := valkeyListStub(t, map[string]stubReply{
		"GET /api/v1/projects/p1/databases/d1/access": {http.StatusOK, `{"access":[]}`},
		"/api/v1/projects/p1/connections":             {http.StatusOK, `{"connections":[]}`},
	})
	cliHome(t, ts.URL)
	out := runCLI(t, linkedDir(t), "access", "database", "cache")
	if !strings.Contains(out, "Public access is not available for Valkey yet — use ghayma connect --local.") || strings.Contains(out, "ghayma access add") {
		t.Fatalf("out = %s", out)
	}
}

func TestConnectDatabase_ValkeyNotRunning(t *testing.T) {
	ts, _ := valkeyListStub(t, map[string]stubReply{
		"/api/v1/projects/p1/sites":                     {http.StatusOK, `[{"id":"s1","name":"main","slug":"main","status":"live","is_default":true,"environment":"production"}]`},
		"GET /api/v1/projects/p1/sites/s1/connections":  {http.StatusOK, `{"connections":[],"available":[{"kind":"database","resource_id":"d1","resource_name":"cache","levels":["read-only","connect"]}]}`},
		"/api/v1/projects/p1/connections":               {http.StatusOK, `{"connections":[]}`},
		"POST /api/v1/projects/p1/sites/s1/connections": {http.StatusConflict, notRunningBody},
	})
	cliHome(t, ts.URL)
	noPrompt(t)
	out := runCLI(t, linkedDir(t), "connect", "database", "cache")
	if lastExitCode != 1 || !strings.Contains(out, "cache is not running yet, so no app can connect to it. Wait until ghayma db info cache shows running, then run this again.") {
		t.Fatalf("exit=%d out=%s", lastExitCode, out)
	}
}

func TestDBSitesAdd_ValkeyNotRunning(t *testing.T) {
	ts, _ := valkeyListStub(t, map[string]stubReply{
		"GET /api/v1/databases/d1/sites": {http.StatusOK, `{"sites":[{"site_id":"s1","slug":"main","name":"main","has_access":false}]}`},
		"PUT /api/v1/databases/d1/sites": {http.StatusConflict, notRunningBody},
	})
	cliHome(t, ts.URL)
	out := runCLI(t, linkedDir(t), "db", "sites", "cache", "--add", "main")
	if lastExitCode != 1 || !strings.Contains(out, "cache is not running yet, so no app can connect to it. Wait until ghayma db info cache shows running, then run this again.") {
		t.Fatalf("exit=%d out=%s", lastExitCode, out)
	}
}

func TestConnectionsHelp_NamesTheValkeyVariables(t *testing.T) {
	if !strings.Contains(connectionsCmd.Long, "REDIS_URL / VALKEY_URL") || !strings.Contains(connectionsCmd.Long, "the GHAYMA_AUTH_* set") {
		t.Fatalf("Long = %q", connectionsCmd.Long)
	}
}

func valkeyRowWithStatus(status string) string {
	return strings.Replace(valkeyListRow, `"status":"running"`, `"status":"`+status+`"`, 1)
}

func TestDBMode_RefusedLocallyUnlessRunning(t *testing.T) {
	cases := map[string]string{
		"stopped":      "cache is stopped, so its mode cannot change. Start it first: ghayma db start cache",
		"provisioning": "cache is provisioning, so its mode cannot change now. Try again once ghayma db info cache shows running.",
		"error":        "cache is in error, so its mode cannot change now. Try again once ghayma db info cache shows running.",
	}
	for status, want := range cases {
		t.Run(status, func(t *testing.T) {
			ts, calls := dbListStub(t, valkeyRowWithStatus(status), nil)
			cliHome(t, ts.URL)
			out := runCLI(t, linkedDir(t), "db", "mode", "cache", "store")
			if lastExitCode != 1 || strings.Contains(strings.Join(*calls, " "), "PATCH") || strings.Contains(out, "Switching") || !strings.Contains(out, want) {
				t.Fatalf("exit=%d calls=%v out=%s", lastExitCode, *calls, out)
			}
		})
	}
}

// A database stopped between the read and the switch is worded by its status
// now.
func TestDBMode_RaceIsWordedByTheCurrentStatus(t *testing.T) {
	ts, _ := valkeyListStub(t, map[string]stubReply{
		"PATCH /api/v1/databases/d1/valkey-mode": {http.StatusConflict, `{"error":"the database must be running to change its disk","code":"database_not_running"}`},
		"GET /api/v1/databases/d1":               {http.StatusOK, `{"database":` + valkeyRowWithStatus("stopped") + `}`},
	})
	cliHome(t, ts.URL)
	out := runCLI(t, linkedDir(t), "db", "mode", "cache", "store")
	if lastExitCode != 1 || !strings.Contains(out, "cache is stopped, so its mode cannot change. Start it first: ghayma db start cache") {
		t.Fatalf("exit=%d out=%s", lastExitCode, out)
	}
}

func TestDBLogs_NoPodIsWordedByStatus(t *testing.T) {
	cases := map[string][]string{
		"stopped":      {"main is stopped, so it has no running log. Start it with: ghayma db start main"},
		"provisioning": {"main is not running yet", "ghayma db info main"},
	}
	for status, wants := range cases {
		t.Run(status, func(t *testing.T) {
			quietLogsSignals(t)
			row := `{"id":"d1","name":"main","type":"postgres","status":"` + status + `"}`
			ts, _ := dbListStub(t, row, map[string]stubReply{
				"/api/v1/databases/d1/logs": {http.StatusConflict, `{"error":"the database has no running pod","code":"no_pod"}`},
			})
			cliHome(t, ts.URL)
			out := runCLI(t, linkedDir(t), "db", "logs", "main")
			if lastExitCode != 1 {
				t.Fatalf("exit=%d out=%s", lastExitCode, out)
			}
			for _, want := range wants {
				if !strings.Contains(out, want) {
					t.Errorf("out = %s; want %q", out, want)
				}
			}
		})
	}
}

func TestDBLogs_FollowEndedSaysWhy(t *testing.T) {
	quietLogsSignals(t)
	ts, _ := dbListStub(t, `{"id":"d1","name":"main","type":"postgres","status":"running"}`, map[string]stubReply{
		"/api/v1/databases/d1/logs": {http.StatusOK, ""},
	})
	cliHome(t, ts.URL)
	out := runCLI(t, linkedDir(t), "db", "logs", "main", "-f")
	if !strings.Contains(out, "ℹ️  The stream ended (the platform ends a follow after 10 minutes, or when the database restarts). Run the command again to keep following.") {
		t.Fatalf("out = %s", out)
	}
}

func TestDBLogsAndMode_MissingArgumentsReadNaturally(t *testing.T) {
	cases := map[string][]string{
		"ghayma db logs requires a name.":                             {"db", "logs"},
		"ghayma db mode requires a name and a mode (cache or store).": {"db", "mode", "cache"},
	}
	for want, args := range cases {
		out := runCLI(t, t.TempDir(), args...)
		if !strings.Contains(out, want) || strings.Contains(out, "requires a argument") {
			t.Errorf("%v: out = %s; want %q", args, out, want)
		}
	}
}
