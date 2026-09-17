package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"paas-cli/internal/api"
)

// Env var inheritance, CLI half (ENVIRONMENTS-DESIGN-2026-09-17 §3). The
// listing gained a resolved ladder and the delete gained a per-key endpoint, so
// each surface has TWO paths to keep honest: the new one, and the one an older
// platform still answers with.

// envStub serves a site's env listing, the per-key delete and the replace-all
// PUT. delStatus/delBody drive the DELETE; a PUT is recorded so the tests can
// prove which path a delete took.
func envStub(t *testing.T, listing string, delStatus int, delBody string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "DELETE":
			if delStatus != 0 {
				w.WriteHeader(delStatus)
			}
			io.WriteString(w, delBody)
		case r.Method == "PUT":
			io.WriteString(w, `{}`)
		case strings.HasSuffix(r.URL.Path, "/env"):
			io.WriteString(w, listing)
		case strings.HasSuffix(r.URL.Path, "/sites"):
			io.WriteString(w, twoSites)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"unexpected `+r.URL.Path+`"}`)
		}
	}))
	t.Cleanup(ts.Close)
	return ts, &seen
}

// ladderListing is what an inheriting site's env endpoint answers with: its OWN
// rows in env_vars (what the replace-all PUT takes) and the RESOLVED ladder in
// vars.
const ladderListing = `{"env_vars":{"API_URL":"https://dev"},"build_time_keys":[],` +
	`"vars":[{"key":"API_URL","value":"https://dev","source":"own"},` +
	`{"key":"NEXT_PUBLIC_NAME","value":"shop","build_time":true,"source":"inherited"},` +
	`{"key":"REGION","value":"eu-central","source":"inherited"}],` +
	`"inherit_env":true,"inherited_from":{"site_id":"s1","slug":"main"}}`

// TestEnvListLines_OldShape: a platform that predates inheritance sends no
// `vars`, and its own rows ARE the whole truth — the original rendering stays.
func TestEnvListLines_OldShape(t *testing.T) {
	snap := &api.EnvVarsSnapshot{
		Values:        map[string]string{"B": "2", "A": "1"},
		BuildTimeKeys: []string{"A"},
	}
	got := strings.Join(envListLines("shop", snap), "\n")
	if !strings.Contains(got, "   A=1  [build-time]") || !strings.Contains(got, "   B=2") {
		t.Errorf("old-shape listing = %q", got)
	}
	if strings.Contains(got, "inherited") {
		t.Errorf("nothing may claim inheritance on a server that does not report it: %q", got)
	}
	if lines := envListLines("shop", &api.EnvVarsSnapshot{Values: map[string]string{}}); lines[0] != "No environment variables set." {
		t.Errorf("empty listing = %v", lines)
	}
}

// TestEnvListLines_ResolvedShape: inherited rows are marked because they are not
// editable here — the value lives on the base site — and are not what the
// replace-all write path carries.
func TestEnvListLines_ResolvedShape(t *testing.T) {
	snap := &api.EnvVarsSnapshot{
		Values:        map[string]string{"API_URL": "https://dev"},
		InheritEnv:    true,
		InheritedFrom: &api.EnvBaseSite{SiteID: "s1", Slug: "main"},
		Vars: []api.ResolvedEnvVar{
			{Key: "API_URL", Value: "https://dev", Source: api.SourceOwn},
			{Key: "NEXT_PUBLIC_NAME", Value: "shop", BuildTime: true, Source: api.SourceInherited},
			{Key: "REGION", Value: "eu-central", Source: api.SourceInherited},
		},
	}
	got := strings.Join(envListLines("dev", snap), "\n")

	if !strings.Contains(got, "   API_URL=https://dev\n") {
		t.Errorf("an own row must read as it always did: %q", got)
	}
	if !strings.Contains(got, "REGION=eu-central  (inherited from main)") {
		t.Errorf("inherited row = %q; want the source named", got)
	}
	if !strings.Contains(got, "NEXT_PUBLIC_NAME=shop  [build-time]  (inherited from main)") {
		t.Errorf("an inherited build-time row must carry BOTH marks: %q", got)
	}
	if !strings.Contains(got, "2 of 3 variable(s) are inherited from 'main'") {
		t.Errorf("summary = %q; want the count and the base site", got)
	}
	if !strings.Contains(got, "credentials are never inherited") {
		t.Errorf("summary = %q; the isolation rule is the point of the feature", got)
	}
}

// TestEnvList_RendersTheLadderEndToEnd.
func TestEnvList_RendersTheLadderEndToEnd(t *testing.T) {
	ts, _ := envStub(t, ladderListing, 0, "")
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, linkedDir(t), "env", "list")

	if !strings.Contains(out, "REGION=eu-central  (inherited from main)") {
		t.Errorf("output = %q; want the ladder rendered", out)
	}
}

// TestEnvDeleteLines is the per-key rendering matrix. The 409's sentence is the
// server's, verbatim: it names the site that actually holds the variable.
func TestEnvDeleteLines(t *testing.T) {
	// An override removed: saying "removed" alone would misdescribe what the
	// pod will now receive.
	lines, refused := envDeleteLines("API_URL", &api.EnvVarDeletion{Key: "API_URL", Deleted: true, InheritedValueRestored: true, InheritedFrom: "main"}, nil)
	if refused || !strings.Contains(lines[0], "the override is gone — the value inherited from 'main' applies again") {
		t.Errorf("restored = %v, %v", lines, refused)
	}

	lines, refused = envDeleteLines("ONLY_MINE", &api.EnvVarDeletion{Key: "ONLY_MINE", Deleted: true}, nil)
	if refused || lines[0] != "✅ ONLY_MINE removed" {
		t.Errorf("plain delete = %v, %v", lines, refused)
	}

	inherited := &api.APIError{
		Status:  http.StatusConflict,
		Message: "REGION: this variable is inherited from main; override it or edit it there",
		Code:    api.CodeEnvVarInherited,
	}
	lines, refused = envDeleteLines("REGION", nil, inherited)
	if !refused || lines[0] != "❌ REGION: this variable is inherited from main; override it or edit it there" {
		t.Errorf("inherited refusal = %v, %v; want the server's sentence verbatim", lines, refused)
	}
	if len(lines) < 2 || !strings.Contains(lines[1], "ghayma env set REGION=") {
		t.Errorf("inherited refusal = %v; want the override hint", lines)
	}

	notFound := &api.APIError{Status: http.StatusNotFound, Message: "no such environment variable on this site", Code: api.CodeEnvVarNotFound}
	lines, refused = envDeleteLines("NOPE", nil, notFound)
	if refused || !strings.Contains(lines[0], "is not set on this site — no change") {
		t.Errorf("unknown key = %v, %v; deleting nothing is not a failure", lines, refused)
	}
}

// TestEnvRouteMissing: only a 404 with NO code is the route being absent — the
// per-key refusals all carry one, and mistaking them for an old server would
// send the delete down the path that cannot express them.
func TestEnvRouteMissing(t *testing.T) {
	if !envRouteMissing(&api.APIError{Status: http.StatusNotFound}) {
		t.Error("a bare 404 is a platform without the route")
	}
	if envRouteMissing(&api.APIError{Status: http.StatusNotFound, Code: api.CodeEnvVarNotFound}) {
		t.Error("a coded 404 is an answer about the KEY, not the route")
	}
	if envRouteMissing(&api.APIError{Status: http.StatusConflict, Code: api.CodeEnvVarInherited}) {
		t.Error("a 409 is not a missing route")
	}
}

// TestEnvDelete_UsesThePerKeyEndpoint: the replace-all PUT cannot express the
// two delete rules, so a platform that serves the per-key route must never see
// a PUT from this command.
func TestEnvDelete_UsesThePerKeyEndpoint(t *testing.T) {
	ts, seen := envStub(t, ladderListing, 0, `{"key":"API_URL","deleted":true,"inherited_value_restored":true,"inherited_from":"main"}`)
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, linkedDir(t), "env", "delete", "API_URL")

	if !containsPath(*seen, "DELETE /api/v1/projects/p1/sites/s1/env/API_URL") {
		t.Fatalf("requests = %v; want the per-key delete", *seen)
	}
	for _, got := range *seen {
		if strings.HasPrefix(got, "PUT") {
			t.Errorf("requests = %v; the per-key path must not also rewrite the whole set", *seen)
		}
	}
	if !strings.Contains(out, "the value inherited from 'main' applies again") {
		t.Errorf("output = %q", out)
	}
}

// TestEnvDelete_InheritedKeyIsRefusedVerbatim: the user is told where the
// variable actually lives, and nothing is written.
func TestEnvDelete_InheritedKeyIsRefusedVerbatim(t *testing.T) {
	const refusal = `{"error":"REGION: this variable is inherited from main; override it or edit it there","code":"env_var_inherited","inherited_from":"main"}`
	ts, seen := envStub(t, ladderListing, http.StatusConflict, refusal)
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, linkedDir(t), "env", "delete", "REGION")

	if !strings.Contains(out, "REGION: this variable is inherited from main; override it or edit it there") {
		t.Errorf("output = %q; want the server's sentence verbatim", out)
	}
	for _, got := range *seen {
		if strings.HasPrefix(got, "PUT") {
			t.Errorf("requests = %v; a refused delete writes nothing", *seen)
		}
	}
	if lastExitCode != 1 {
		t.Errorf("exit code = %d; a refusal is a failure", lastExitCode)
	}
}

// TestEnvDelete_FallsBackOnAPlatformWithoutTheRoute: the old read-modify-write
// path is still exactly right on a site that inherits nothing, so a bare 404
// must not become an error the user has to decode.
func TestEnvDelete_FallsBackOnAPlatformWithoutTheRoute(t *testing.T) {
	ts, seen := envStub(t, `{"env_vars":{"API_URL":"https://dev","REGION":"eu"},"build_time_keys":[]}`, http.StatusNotFound, "404 page not found")
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, linkedDir(t), "env", "delete", "API_URL")

	if !containsPath(*seen, "PUT /api/v1/projects/p1/sites/s1/env") {
		t.Errorf("requests = %v; an older platform still gets the replace-all write", *seen)
	}
	if !strings.Contains(out, "Removing 1 env var(s): API_URL") || !strings.Contains(out, "✅ Environment variables updated") {
		t.Errorf("output = %q; want the pre-inheritance reporting", out)
	}
	if lastExitCode != 0 {
		t.Errorf("exit code = %d; the fallback succeeded", lastExitCode)
	}
}

// TestEnvPullProvenance: `env pull` writes exactly what it always wrote — the
// EFFECTIVE environment — and now says where each value came from, because a
// site that inherits and derives has three kinds of row in that file.
func TestEnvPullProvenance(t *testing.T) {
	env := map[string]string{"API_URL": "https://dev", "REGION": "eu", "DATABASE_URL": "postgresql://dev"}
	sources := map[string]string{"API_URL": api.SourceOwn, "REGION": api.SourceInherited, "DATABASE_URL": api.SourceDerived}

	if got := provenanceSummary(env, sources); !strings.Contains(got, "1 own · 1 inherited · 1 derived") {
		t.Errorf("summary = %q; want a count per source", got)
	}
	if got := provenanceSummary(env, nil); got != "" {
		t.Errorf("no provenance from the server → no summary, got %q", got)
	}
	if got := sourceBadge(api.SourceDerived); got != "  (derived)" {
		t.Errorf("derived badge = %q", got)
	}
	if got := sourceBadge(""); got != "" {
		t.Errorf("an unknown source must print nothing, got %q", got)
	}
}
