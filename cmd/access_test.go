package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"paas-cli/internal/api"
	"paas-cli/internal/config"
)

// accessRows is the listing the stub serves. The rogue "secret" field is
// deliberate: the server has no such field, and this pins that no future
// addition to the row type can make a listing print one. A credential exists
// for exactly one response — the create or the rotate that minted it.
const accessRows = `[
	{"id":"e1","kind":"database","resource_id":"d1","name":"metabase","level":"read-only","allow_cidrs":["10.0.0.0/8"],"expires_at":"2026-12-01T00:00:00Z","created_at":"2026-09-19T10:00:00Z","active":true,"secret":"NEVER-PRINT-ME"},
	{"id":"e2","kind":"database","resource_id":"d1","name":"legacy-etl","level":"connect","allow_cidrs":[],"created_at":"2026-08-01T00:00:00Z","revoked_at":"2026-09-01T00:00:00Z","active":false}]`

// accessStub serves the two resource listings, the connections listing and the
// access routes, recording every "METHOD /path" and body it was sent. Mutating
// routes answer with the given status and body.
func accessStub(t *testing.T, rows string, status int, body string) (*httptest.Server, *[]string, *[]string) {
	t.Helper()
	var seen, bodies []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.Path)
		bodies = append(bodies, string(raw))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/databases":
			io.WriteString(w, `{"databases":[
				{"id":"d1","name":"pg-main","project_id":"p1"},
				{"id":"d2","name":"pg-analytics","project_id":"p1"},
				{"id":"d9","name":"pg-main","project_id":"p2"}]}`)
		case r.URL.Path == "/api/v1/storage":
			io.WriteString(w, `{"buckets":[{"id":"b1","name":"uploads","project_id":"p1","garage_bucket":"gb-uploads"}]}`)
		case r.URL.Path == "/api/v1/projects/p1/connections":
			io.WriteString(w, `{"connections":[
				{"site_id":"s1","site_slug":"main","kind":"database","resource_id":"d1","resource_name":"pg-main","level":"connect"},
				{"site_id":"s2","site_slug":"admin","kind":"bucket","resource_id":"b1","resource_name":"uploads","level":"read"}]}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/access"):
			io.WriteString(w, `{"access":`+rows+`}`)
		default:
			w.WriteHeader(status)
			io.WriteString(w, body)
		}
	}))
	t.Cleanup(ts.Close)
	return ts, &seen, &bodies
}

// accessProject materializes a linked project directory: the access family is
// project-scoped, so the nearest config is all it needs.
func accessProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{projectConfigName: `{"project_id":"p1","name":"shop","site_id":"s1"}`})
	return dir
}

func accessTestTarget(apiHost, kind, name, id string) (*api.Client, *accessTarget) {
	client := api.NewClient(&config.Config{APIHost: apiHost, Token: "test-token"})
	return client, &accessTarget{ProjectID: "p1", ProjectName: "shop", Kind: kind, ResourceID: id, ResourceName: name}
}

// TestAccess_AuthAppIsRefusedBeforeAnyRequest: an auth app has no external
// principal to mint — its external access IS its restricted project keys — so
// every verb must refuse the kind locally and point at where keys are made.
func TestAccess_AuthAppIsRefusedBeforeAnyRequest(t *testing.T) {
	for _, args := range [][]string{
		{"access", "auth", "my-app"},
		{"access", "add", "auth", "my-app", "--name", "partner"},
		{"access", "revoke", "auth", "my-app", "partner", "--yes"},
		{"access", "rotate", "auth", "my-app", "partner", "--yes"},
		{"access", "allow", "auth", "my-app", "partner", "--set", ""},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			ts, seen, _ := accessStub(t, accessRows, http.StatusOK, "{}")
			cliHome(t, ts.URL)

			out := runCLI(t, accessProject(t), args...)

			if !strings.Contains(out, "restricted project keys") {
				t.Errorf("output = %q; want the auth-app refusal", out)
			}
			if !strings.Contains(out, "Project → Settings → API keys") {
				t.Errorf("output = %q; want the console pointer (the CLI has no keys command)", out)
			}
			if len(*seen) != 0 {
				t.Errorf("requests = %v; want none before the refusal", *seen)
			}
			if lastExitCode != 1 {
				t.Errorf("exit code = %d; want 1", lastExitCode)
			}
		})
	}
}

// TestAccessList_MergesAppsAndPrincipals: the listing is the two halves side by
// side, and it must never show a secret — there is no field for one, and a row
// that carried one anyway must not reach the output.
func TestAccessList_MergesAppsAndPrincipals(t *testing.T) {
	ts, seen, _ := accessStub(t, accessRows, http.StatusOK, "{}")
	cliHome(t, ts.URL)

	out := runCLI(t, accessProject(t), "access", "database", "pg-main")

	for _, want := range []string{
		"Access to database 'pg-main'",
		"Apps in this project", "main", "connect",
		"External principals", "metabase", "read-only", "10.0.0.0/8", "2026-12-01",
		"legacy-etl", "revoked", "any source",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("listing = %q; want %q", out, want)
		}
	}
	if strings.Contains(out, "NEVER-PRINT-ME") || strings.Contains(out, "Password") || strings.Contains(out, "Secret key") {
		t.Errorf("listing = %q; a credential must NEVER appear in a listing", out)
	}
	if !strings.Contains(strings.Join(*seen, " "), "GET /api/v1/projects/p1/databases/d1/access") {
		t.Errorf("requests = %v; want the resolved database's access route", *seen)
	}
}

// TestAccessList_UnknownResourceNamesTheOnesThereAre: the name is resolved
// inside THIS project — a same-named database of another project must not be
// picked, and a miss says what is available.
func TestAccessList_ResolvesWithinTheProject(t *testing.T) {
	ts, seen, _ := accessStub(t, `[]`, http.StatusOK, "{}")
	cliHome(t, ts.URL)
	dir := accessProject(t)

	out := runCLI(t, dir, "access", "database", "pg-main")
	if !strings.Contains(strings.Join(*seen, " "), "/projects/p1/databases/d1/access") {
		t.Errorf("requests = %v; want this project's database d1, never d9", *seen)
	}
	if !strings.Contains(out, "none — add one with: ghayma access add database pg-main --name") {
		t.Errorf("empty listing = %q; want the add hint", out)
	}

	out = runCLI(t, dir, "access", "database", "nope")
	if !strings.Contains(out, `no database named "nope" in this project`) || !strings.Contains(out, "pg-analytics, pg-main") {
		t.Errorf("miss = %q; want the available names", out)
	}
}

// TestAccessAdd_PrintsTheCredentialOnce is the whole point of the command: the
// secret exists in exactly one response, so it is printed with the server's
// warning and the way to mint another.
func TestAccessAdd_PrintsTheCredentialOnce(t *testing.T) {
	created := `{"access":{"id":"e3","name":"metabase","level":"read-only","allow_cidrs":["203.0.113.0/24"],"expires_at":"2026-10-19T00:00:00Z"},
		"credential":{"credential_ref":"c_1a2b3c4d","secret":"p4ssw0rd","uri":"postgresql://c_1a2b3c4d:p4ssw0rd@pg-d1.db.ghayma.tech:5432/db_x?sslmode=require"},
		"warning":"this credential is shown once and cannot be retrieved again"}`
	ts, seen, bodies := accessStub(t, accessRows, http.StatusCreated, created)
	cliHome(t, ts.URL)

	out := runCLI(t, accessProject(t), "access", "add", "database", "pg-main",
		"--name", "metabase", "--level", "read-only", "--allow", "203.0.113.0/24, 198.51.100.7", "--expires", "30")

	if !strings.Contains(strings.Join(*seen, " "), "POST /api/v1/projects/p1/databases/d1/access") {
		t.Fatalf("requests = %v; want the create route", *seen)
	}
	body := (*bodies)[len(*bodies)-1]
	for _, want := range []string{`"name":"metabase"`, `"level":"read-only"`, `"203.0.113.0/24"`, `"198.51.100.7"`, `"expires_at"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body = %s; want %s", body, want)
		}
	}
	for _, want := range []string{
		"✅ Added 'metabase' to database 'pg-main' (read-only)",
		"Allowed sources: 203.0.113.0/24",
		"🔑 Credential for 'metabase' on database 'pg-main'",
		"postgresql://c_1a2b3c4d:p4ssw0rd@pg-d1.db.ghayma.tech:5432/db_x?sslmode=require",
		"Username:   c_1a2b3c4d",
		"Password:   p4ssw0rd",
		"this credential is shown once and cannot be retrieved again — store it now.",
		"ghayma access rotate database pg-main metabase",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q; want %q", out, want)
		}
	}
}

// TestAccessAdd_BucketPrintsTheKeyPair: a bucket credential is an endpoint plus
// a key pair, not a URL, and the labels have to say so.
func TestAccessAdd_BucketPrintsTheKeyPair(t *testing.T) {
	created := `{"access":{"id":"e4","name":"backups","level":"read"},
		"credential":{"credential_ref":"GK31c2f218","secret":"s3cr3tkey","endpoint":"https://s3.ghayma.tech","bucket":"gb-uploads"}}`
	ts, _, _ := accessStub(t, `[]`, http.StatusCreated, created)
	cliHome(t, ts.URL)

	out := runCLI(t, accessProject(t), "access", "add", "bucket", "uploads", "--name", "backups", "--level", "read")

	for _, want := range []string{
		"Endpoint:   https://s3.ghayma.tech", "Bucket:     gb-uploads",
		"Access key: GK31c2f218", "Secret key: s3cr3tkey",
		// No server warning in this body: the CLI still says it.
		"this credential is shown once and cannot be retrieved again — store it now.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q; want %q", out, want)
		}
	}
}

// TestAccessAdd_RefusesBadInputBeforeAnyRequest: a missing principal name and a
// level the kind does not accept are answered locally — a round-trip buys
// nothing when the answer is already known.
func TestAccessAdd_RefusesBadInputBeforeAnyRequest(t *testing.T) {
	cases := []struct {
		name, want string
		args       []string
	}{
		{"no --name", "A principal needs a name", []string{"access", "add", "database", "pg-main"}},
		{"wrong level", `level "admin" is not valid for a database`, []string{"access", "add", "database", "pg-main", "--name", "x", "--level", "admin"}},
		{"negative expiry", "--expires takes a number of days in the future", []string{"access", "add", "database", "pg-main", "--name", "x", "--expires", "-3"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, seen, _ := accessStub(t, `[]`, http.StatusCreated, "{}")
			cliHome(t, ts.URL)

			out := runCLI(t, accessProject(t), tc.args...)

			if !strings.Contains(out, tc.want) {
				t.Errorf("output = %q; want %q", out, tc.want)
			}
			for _, got := range *seen {
				if strings.HasPrefix(got, "POST") {
					t.Errorf("requests = %v; nothing may be created on bad input", *seen)
				}
			}
			if lastExitCode != 1 {
				t.Errorf("exit code = %d; want 1", lastExitCode)
			}
		})
	}
}

// TestRevokeAccess_ConfirmGuardsTheDelete: revoking destroys a credential, so a
// declined prompt must leave the server alone and --yes must skip it.
func TestRevokeAccess_ConfirmGuardsTheDelete(t *testing.T) {
	row := &api.ExternalAccess{ID: "e1", Name: "metabase", Level: "read-only", Active: true}

	t.Run("declined", func(t *testing.T) {
		ts, seen, _ := accessStub(t, accessRows, http.StatusNoContent, "")
		client, target := accessTestTarget(ts.URL, "database", "pg-main", "d1")

		out := captureStdout(t, func() { revokeAccess(client, target, row, false, func() string { return "n" }) })

		if !strings.Contains(out, "cannot be restored") {
			t.Errorf("prompt = %q; want what cannot be undone", out)
		}
		if !strings.Contains(out, "Cancelled") {
			t.Errorf("output = %q; want the cancellation", out)
		}
		if len(*seen) != 0 {
			t.Errorf("requests = %v; a declined confirm must delete nothing", *seen)
		}
	})

	t.Run("--yes", func(t *testing.T) {
		ts, seen, _ := accessStub(t, accessRows, http.StatusNoContent, "")
		client, target := accessTestTarget(ts.URL, "database", "pg-main", "d1")

		out := captureStdout(t, func() {
			revokeAccess(client, target, row, true, func() string {
				t.Fatal("--yes must not prompt")
				return ""
			})
		})

		if len(*seen) != 1 || (*seen)[0] != "DELETE /api/v1/projects/p1/databases/d1/access/e1" {
			t.Fatalf("requests = %v; want the delete route", *seen)
		}
		if !strings.Contains(out, "✅ Revoked 'metabase' on database 'pg-main'") {
			t.Errorf("output = %q; want the success line", out)
		}
	})

	t.Run("already revoked is no change", func(t *testing.T) {
		ts, seen, _ := accessStub(t, accessRows, http.StatusNoContent, "")
		client, target := accessTestTarget(ts.URL, "database", "pg-main", "d1")
		gone := &api.ExternalAccess{ID: "e2", Name: "legacy-etl", RevokedAt: "2026-09-01T00:00:00Z"}

		out := captureStdout(t, func() { revokeAccess(client, target, gone, true, readAnswer) })

		if !strings.Contains(out, "already revoked") || !strings.Contains(out, "no change") {
			t.Errorf("output = %q; want the no-change line", out)
		}
		if len(*seen) != 0 {
			t.Errorf("requests = %v; want none", *seen)
		}
	})
}

// TestRotateAccess_PrintsTheNewSecretOnce: a rotation is the second and last
// moment a secret exists outside the engine.
func TestRotateAccess_PrintsTheNewSecretOnce(t *testing.T) {
	rotated := `{"credential":{"credential_ref":"c_1a2b3c4d","secret":"n3wp4ss","uri":"postgresql://c_1a2b3c4d:n3wp4ss@pg-d1.db.ghayma.tech:5432/db_x?sslmode=require"},"warning":"this credential is shown once and cannot be retrieved again"}`
	ts, seen, _ := accessStub(t, accessRows, http.StatusOK, rotated)
	client, target := accessTestTarget(ts.URL, "database", "pg-main", "d1")
	row := &api.ExternalAccess{ID: "e1", Name: "metabase", Active: true}

	out := captureStdout(t, func() { rotateAccess(client, target, row, true, readAnswer) })

	if len(*seen) != 1 || (*seen)[0] != "POST /api/v1/projects/p1/databases/d1/access/e1/rotate" {
		t.Fatalf("requests = %v; want the rotate route", *seen)
	}
	for _, want := range []string{"✅ Rotated 'metabase'", "n3wp4ss", "shown once", "store it now"} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q; want %q", out, want)
		}
	}

	declined := captureStdout(t, func() {
		rotateAccess(client, target, row, false, func() string { return "" })
	})
	if !strings.Contains(declined, "Cancelled") || strings.Contains(declined, "n3wp4ss") {
		t.Errorf("declined rotate = %q; want no new secret", declined)
	}
}

// TestAccessAllow_SetsAndClears pins both halves of the allowlist command and
// the collapse note that goes with a mixed resource.
func TestAccessAllow_SetsAndClears(t *testing.T) {
	t.Run("set", func(t *testing.T) {
		updated := `{"access":{"id":"e1","name":"metabase","allow_cidrs":["198.51.100.7/32","203.0.113.0/24"],"active":true}}`
		ts, seen, bodies := accessStub(t, accessRows, http.StatusOK, updated)
		cliHome(t, ts.URL)

		out := runCLI(t, accessProject(t), "access", "allow", "database", "pg-main", "metabase",
			"--set", "203.0.113.0/24, 198.51.100.7")

		if !strings.Contains(strings.Join(*seen, " "), "PUT /api/v1/projects/p1/databases/d1/access/e1/allowlist") {
			t.Fatalf("requests = %v; want the allowlist route", *seen)
		}
		body := (*bodies)[len(*bodies)-1]
		if !strings.Contains(body, `"allow_cidrs":["203.0.113.0/24","198.51.100.7"]`) {
			t.Errorf("body = %s; want the list as typed, trimmed", body)
		}
		if !strings.Contains(out, "✅ Allowlist for 'metabase' on database 'pg-main': 198.51.100.7/32, 203.0.113.0/24") {
			t.Errorf("output = %q; want the canonical list the server stored", out)
		}
	})

	t.Run("clear", func(t *testing.T) {
		ts, _, bodies := accessStub(t, accessRows, http.StatusOK, `{"access":{"id":"e1","name":"metabase","allow_cidrs":[],"active":true}}`)
		cliHome(t, ts.URL)

		out := runCLI(t, accessProject(t), "access", "allow", "database", "pg-main", "metabase", "--set", "")

		if got := (*bodies)[len(*bodies)-1]; !strings.Contains(got, `"allow_cidrs":[]`) {
			t.Errorf("body = %s; want an explicit empty list", got)
		}
		if !strings.Contains(out, "Allowlist cleared for 'metabase'") || !strings.Contains(out, "any source") {
			t.Errorf("output = %q; want the cleared line", out)
		}
	})

	t.Run("--set is required", func(t *testing.T) {
		ts, seen, _ := accessStub(t, accessRows, http.StatusOK, "{}")
		cliHome(t, ts.URL)

		out := runCLI(t, accessProject(t), "access", "allow", "database", "pg-main", "metabase")

		if !strings.Contains(out, "Pass the whole list with --set") {
			t.Errorf("output = %q; want the missing-flag refusal", out)
		}
		if len(*seen) != 0 {
			t.Errorf("requests = %v; want none", *seen)
		}
	})
}

// TestAccessErrors_Render covers the three answers this API has of its own: a
// 400 printed verbatim (the server says which entry was wrong), a 409 that
// names the way out, and a 503 that says nothing changed.
func TestAccessErrors_Render(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		args    []string
		wants   []string
		unwants []string
	}{
		{
			name: "400 CIDR verbatim", status: http.StatusBadRequest,
			body: `{"error":"invalid CIDR: \"10.0.0.0/8x\""}`,
			args: []string{"access", "allow", "database", "pg-main", "metabase", "--set", "10.0.0.0/8x"},
			// The server's own sentence, unparaphrased: it names the entry.
			wants: []string{`invalid CIDR: "10.0.0.0/8x"`},
		},
		{
			name: "409 revoked", status: http.StatusConflict,
			body: `{"error":"this external access has been revoked: e1"}`,
			args: []string{"access", "rotate", "database", "pg-main", "metabase", "--yes"},
			wants: []string{"this external access has been revoked: e1",
				"A revoked principal stays revoked", "ghayma access add"},
		},
		{
			name: "503 engine", status: http.StatusServiceUnavailable,
			body: `{"error":"could not mint the credential; no external access was created (database d1): dial tcp: connect: connection refused"}`,
			args: []string{"access", "add", "database", "pg-main", "--name", "metabase"},
			wants: []string{"no external access was created",
				"Nothing was changed — run the same command again once it answers."},
			// Quiet: the status code and Go's error plumbing stay out of it.
			unwants: []string{"HTTP 503", "Failed to add"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, _, _ := accessStub(t, accessRows, tc.status, tc.body)
			cliHome(t, ts.URL)

			out := runCLI(t, accessProject(t), tc.args...)

			for _, want := range tc.wants {
				if !strings.Contains(out, want) {
					t.Errorf("output = %q; want %q", out, want)
				}
			}
			for _, unwanted := range tc.unwants {
				if strings.Contains(out, unwanted) {
					t.Errorf("output = %q; must not contain %q", out, unwanted)
				}
			}
			if lastExitCode != 1 {
				t.Errorf("exit code = %d; want 1", lastExitCode)
			}
		})
	}
}

func TestParseAccessKind(t *testing.T) {
	for arg, want := range map[string]string{
		"database": "database", "db": "database", "DB": "database",
		"bucket": "bucket", "storage": "bucket",
	} {
		if got, err := parseAccessKind(arg); err != nil || got != want {
			t.Errorf("parseAccessKind(%q) = %q, %v; want %q", arg, got, err, want)
		}
	}
	for _, arg := range []string{"auth", "auth-app", "auth_app"} {
		_, err := parseAccessKind(arg)
		if err == nil || !strings.Contains(err.Error(), "restricted project keys") {
			t.Errorf("parseAccessKind(%q) err = %v; want the auth-app refusal", arg, err)
		}
	}
	if _, err := parseAccessKind("cron"); err == nil || !strings.Contains(err.Error(), "use database or bucket") {
		t.Errorf("parseAccessKind(\"cron\") err = %v; want the kind list", err)
	}
}

func TestFindPrincipal(t *testing.T) {
	rows := []api.ExternalAccess{
		{ID: "e1", Name: "metabase", Active: true},
		{ID: "e2", Name: "metabase", RevokedAt: "2026-09-01T00:00:00Z"},
		{ID: "e3", Name: "partner", Active: true},
	}
	if row, err := findPrincipal(rows, "METABASE"); err != nil || row.ID != "e1" {
		t.Errorf("a revoked namesake must not shadow the live one: %+v, %v", row, err)
	}
	if row, err := findPrincipal(rows, "e2"); err != nil || row.ID != "e2" {
		t.Errorf("an id must resolve exactly: %+v, %v", row, err)
	}
	_, err := findPrincipal(rows, "nobody")
	if err == nil || !strings.Contains(err.Error(), "in force: metabase, partner") {
		t.Errorf("miss = %v; want the principals in force", err)
	}
	_, err = findPrincipal([]api.ExternalAccess{}, "nobody")
	if err == nil || !strings.Contains(err.Error(), "no principal in force") {
		t.Errorf("empty resource = %v", err)
	}
	twins := []api.ExternalAccess{{ID: "e1", Name: "dup", Active: true}, {ID: "e2", Name: "dup", Active: true}}
	if _, err := findPrincipal(twins, "dup"); err == nil || !strings.Contains(err.Error(), "by its id (e1, e2)") {
		t.Errorf("two live namesakes = %v; want the ids", err)
	}
}

// TestCollapseNote states the rule the allowlist help explains: the filter runs
// before authentication, so one unrestricted principal disables it for all.
func TestCollapseNote(t *testing.T) {
	restricted := api.ExternalAccess{ID: "e1", Name: "metabase", AllowCIDRs: []string{"10.0.0.0/8"}, Active: true}
	open := api.ExternalAccess{ID: "e2", Name: "legacy-etl", Active: true}
	revokedOpen := api.ExternalAccess{ID: "e3", Name: "old", RevokedAt: "2026-09-01T00:00:00Z"}

	if note := collapseNote([]api.ExternalAccess{restricted, open}); !strings.Contains(note, "'legacy-etl' has no allowlist") ||
		!strings.Contains(note, "no IP filter is enforced") {
		t.Errorf("mixed = %q; want the collapse note", note)
	}
	if note := collapseNote([]api.ExternalAccess{restricted, revokedOpen}); note != "" {
		t.Errorf("a revoked unrestricted principal collapses nothing, got %q", note)
	}
	if note := collapseNote([]api.ExternalAccess{restricted}); note != "" {
		t.Errorf("all restricted = %q; want no note", note)
	}
	if note := collapseNote([]api.ExternalAccess{open}); note != "" {
		t.Errorf("all unrestricted = %q; nothing collapses when nothing is filtered", note)
	}
}

func TestExpiryFromDaysAndCIDRParsing(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	if got, err := expiryFromDays(0, now); err != nil || got != "" {
		t.Errorf("0 days = %q, %v; want no expiry", got, err)
	}
	if got, err := expiryFromDays(30, now); err != nil || got != "2026-10-19T12:00:00Z" {
		t.Errorf("30 days = %q, %v", got, err)
	}
	if _, err := expiryFromDays(-1, now); err == nil {
		t.Error("a past expiry must be refused before the request")
	}
	if got := parseCIDRList(" 10.0.0.0/8 ,, 1.2.3.4 "); len(got) != 2 || got[0] != "10.0.0.0/8" || got[1] != "1.2.3.4" {
		t.Errorf("parseCIDRList = %v", got)
	}
	if got := parseCIDRList(""); got == nil || len(got) != 0 {
		t.Errorf("an empty --set = %v; want an empty non-nil list", got)
	}
}
