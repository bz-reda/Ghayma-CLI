package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Cross-environment database connections (Environments §3, Reda 2026-09-17):
// warn, confirm, connect — never block, and never fail a connect because the
// pre-flight could not read something.

// crossEnvStub serves everything `connect database` reads: the site list with
// environments, the target site's connection view, the project-wide connection
// list the pre-flight inspects, and the POST. connections is the body of
// GET /projects/p1/connections; "" makes that read fail, which is the
// fail-open case.
func crossEnvStub(t *testing.T, sites, connections string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/connections"):
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"site_id":"s2","site_slug":"dev","kind":"database","resource_id":"d1","resource_name":"shop-pg","level":"connect","env_names":["DATABASE_URL"]}`)
		case strings.HasSuffix(r.URL.Path, "/sites/s2/connections"):
			io.WriteString(w, `{"connections":[],"available":[{"kind":"database","resource_id":"d1","resource_name":"shop-pg","levels":["read-only","connect"],"env_names":["DATABASE_URL"]}]}`)
		case r.URL.Path == "/api/v1/projects/p1/connections":
			if connections == "" {
				w.WriteHeader(http.StatusInternalServerError)
				io.WriteString(w, `{"error":"connections are unavailable"}`)
				return
			}
			io.WriteString(w, connections)
		case strings.HasSuffix(r.URL.Path, "/sites"):
			io.WriteString(w, sites)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"unexpected `+r.URL.Path+`"}`)
		}
	}))
	t.Cleanup(ts.Close)
	return ts, &seen
}

// dbOnMain says the database is already connected to the production site.
const dbOnMain = `{"connections":[{"site_id":"s1","site_slug":"main","kind":"database","resource_id":"d1","resource_name":"shop-pg","level":"connect"}]}`

// devLinkedDir is a directory linked to the DEVELOPMENT site of project p1.
func devLinkedDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{projectConfigName: `{"project_id":"p1","name":"shop","slug":"shop","site_id":"s2","site_name":"dev","site_slug":"dev"}`})
	return dir
}

// TestCrossEnvWarning is the matrix. The danger is the SHARING, not the order,
// so both directions warn; two sites on the same side of the production line
// are silent.
func TestCrossEnvWarning(t *testing.T) {
	prod := siteKind{Slug: "main", Environment: "production"}
	dev := siteKind{Slug: "dev", Environment: "development"}
	staging := siteKind{Slug: "staging", Environment: "staging"}
	otherProd := siteKind{Slug: "admin", Environment: "production"}

	// development → a database production already uses.
	got := crossEnvWarning("shop-pg", dev, []siteKind{prod})
	if !strings.Contains(got, "shop-pg is used by production site 'main'") {
		t.Errorf("dev → prod-used db = %q; want the production user named", got)
	}
	if !strings.Contains(got, "can corrupt real data") || !strings.Contains(got, "separate database for 'dev'") {
		t.Errorf("dev → prod-used db = %q; want the risk and the recommendation", got)
	}

	// staging counts as non-production for this purpose.
	if got := crossEnvWarning("shop-pg", staging, []siteKind{prod}); !strings.Contains(got, "A staging site sharing a production database") {
		t.Errorf("staging → prod-used db = %q; want a warning naming staging", got)
	}

	// production → a database a development site already uses (the reverse).
	got = crossEnvWarning("shop-pg", prod, []siteKind{dev})
	if !strings.Contains(got, "used by development site 'dev'") || !strings.Contains(got, "separate database for 'dev'") {
		t.Errorf("prod → dev-used db = %q; want the reverse warning pointing at the dev site", got)
	}

	// Same side of the line: nothing to say, in either direction.
	if got := crossEnvWarning("shop-pg", dev, []siteKind{staging}); got != "" {
		t.Errorf("dev → dev/staging-only db = %q; want silence", got)
	}
	if got := crossEnvWarning("shop-pg", prod, []siteKind{otherProd}); got != "" {
		t.Errorf("prod → prod-only db = %q; want silence", got)
	}

	// Nothing else uses it, and the site itself does not count.
	if got := crossEnvWarning("shop-pg", dev, nil); got != "" {
		t.Errorf("unused db = %q; want silence", got)
	}
	if got := crossEnvWarning("shop-pg", dev, []siteKind{{Slug: "dev", Environment: "development"}}); got != "" {
		t.Errorf("the target site itself must not warn about itself, got %q", got)
	}

	// An unknown kind reads as production, the same defensive direction the
	// backend takes: the warning may say too much, never too little.
	if got := crossEnvWarning("shop-pg", dev, []siteKind{{Slug: "legacy"}}); !strings.Contains(got, "production site 'legacy'") {
		t.Errorf("site with no kind = %q; want it treated as production", got)
	}
}

// TestConfirmCrossEnv: --yes skips the QUESTION, not the warning.
func TestConfirmCrossEnv(t *testing.T) {
	out := captureStdout(t, func() {
		if !confirmCrossEnv("⚠️  careful", true, func() string { t.Fatal("--yes must not prompt"); return "" }) {
			t.Error("--yes must proceed")
		}
	})
	if !strings.Contains(out, "⚠️  careful") {
		t.Errorf("output = %q; --yes still shows what is happening", out)
	}

	captureStdout(t, func() {
		if confirmCrossEnv("⚠️  careful", false, func() string { return "" }) {
			t.Error("an empty answer must cancel")
		}
		if confirmCrossEnv("⚠️  careful", false, func() string { return "n" }) {
			t.Error("'n' must cancel")
		}
		if !confirmCrossEnv("⚠️  careful", false, func() string { return "y" }) {
			t.Error("'y' must proceed")
		}
	})

	// No warning, no question.
	if !confirmCrossEnv("", false, func() string { t.Fatal("nothing to confirm"); return "" }) {
		t.Error("an empty warning must proceed silently")
	}
}

// TestConnectDatabase_WarnsAndStopsWithoutAnAnswer: the connection is not made
// until the user says so. Stdin is not a terminal here, so the answer is empty
// — which is the N of [y/N].
func TestConnectDatabase_WarnsAndStopsWithoutAnAnswer(t *testing.T) {
	ts, seen := crossEnvStub(t, twoSites, dbOnMain)
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, devLinkedDir(t), "connect", "database", "shop-pg")

	if !strings.Contains(out, "shop-pg is used by production site 'main'") {
		t.Fatalf("output = %q; want the cross-environment warning", out)
	}
	if !strings.Contains(out, "❌ Cancelled.") {
		t.Errorf("output = %q; an unanswered confirmation cancels", out)
	}
	for _, got := range *seen {
		if strings.HasPrefix(got, "POST") {
			t.Errorf("requests = %v; a cancelled connect sends nothing", *seen)
		}
	}
}

// TestConnectDatabase_YesBypassesTheQuestion.
func TestConnectDatabase_YesBypassesTheQuestion(t *testing.T) {
	ts, seen := crossEnvStub(t, twoSites, dbOnMain)
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, devLinkedDir(t), "connect", "database", "shop-pg", "--yes")

	if !strings.Contains(out, "shop-pg is used by production site 'main'") {
		t.Errorf("output = %q; --yes still prints the warning", out)
	}
	if !containsPath(*seen, "POST /api/v1/projects/p1/sites/s2/connections") {
		t.Errorf("requests = %v; --yes connects", *seen)
	}
	if !strings.Contains(out, "✅ Connected database 'shop-pg' to 'dev'") {
		t.Errorf("output = %q", out)
	}
}

// TestConnectDatabase_SameEnvironmentIsSilent: two development sites sharing a
// database is ordinary, and a warning that fires there is noise people learn to
// ignore.
func TestConnectDatabase_SameEnvironmentIsSilent(t *testing.T) {
	const dbOnAnotherDev = `{"connections":[{"site_id":"s3","site_slug":"preview","kind":"database","resource_id":"d1","resource_name":"shop-pg","level":"connect"}]}`
	sites := `[{"id":"s1","slug":"main","is_default":true,"environment":"production"},` +
		`{"id":"s2","slug":"dev","environment":"development"},` +
		`{"id":"s3","slug":"preview","environment":"development"}]`
	ts, seen := crossEnvStub(t, sites, dbOnAnotherDev)
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, devLinkedDir(t), "connect", "database", "shop-pg")

	if strings.Contains(out, "⚠️") {
		t.Errorf("output = %q; two development sites sharing a database is not a warning", out)
	}
	if !containsPath(*seen, "POST /api/v1/projects/p1/sites/s2/connections") {
		t.Errorf("requests = %v; the connect must proceed with no question", *seen)
	}
}

// TestConnectDatabase_PreflightFailureIsSilentAndConnects: the pre-flight is an
// extra, not a gate. A connections read that errors must never turn a working
// command into a scary one.
func TestConnectDatabase_PreflightFailureIsSilentAndConnects(t *testing.T) {
	ts, seen := crossEnvStub(t, twoSites, "")
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, devLinkedDir(t), "connect", "database", "shop-pg")

	if strings.Contains(out, "⚠️") || strings.Contains(out, "unavailable") {
		t.Errorf("output = %q; a broken pre-flight says nothing", out)
	}
	if !containsPath(*seen, "POST /api/v1/projects/p1/sites/s2/connections") {
		t.Errorf("requests = %v; a broken pre-flight must not block the connect", *seen)
	}
}

// TestConnectBucket_IsNotPreflighted: the warning is about DATABASES — a shared
// bucket is not the same accident, and reading connections for every connect
// would cost a round-trip for nothing.
func TestConnectBucket_IsNotPreflighted(t *testing.T) {
	ts, seen := crossEnvStub(t, twoSites, dbOnMain)
	cliHome(t, ts.URL)
	noPrompt(t)

	runCLI(t, devLinkedDir(t), "connect", "bucket", "shop-pg")

	for _, got := range *seen {
		if got == "GET /api/v1/projects/p1/connections" {
			t.Errorf("requests = %v; only a database connect runs the pre-flight", *seen)
		}
	}
}
