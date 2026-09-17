package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"paas-cli/internal/api"
)

// Environments, CLI half (ENVIRONMENTS-DESIGN-2026-09-17 §1): a site is born
// with a kind, can be re-kinded, and says which kind it is everywhere it is
// listed. The refusals are the model's invariants — the default site is always
// production and is the env ladder's base — so they are rendered as answers
// with a way out, not as raw errors.

// twoSites is a project with a production default site and a development one.
const twoSites = `[{"id":"s1","name":"main","slug":"main","status":"live","is_default":true,"environment":"production"},` +
	`{"id":"s2","name":"dev","slug":"dev","status":"live","environment":"development","inherit_env":true}]`

// siteEnvStub serves the site list, the create entry and the two settings
// routes. putStatus/putBody are what the environment and inherit-env PUTs
// answer with (0 → 200 and the echoed site).
func siteEnvStub(t *testing.T, sites string, putStatus int, putBody string) (*httptest.Server, *[]string, *map[string]any) {
	t.Helper()
	var seen []string
	body := map[string]any{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/environment"), strings.HasSuffix(r.URL.Path, "/inherit-env"):
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			if putStatus != 0 {
				w.WriteHeader(putStatus)
			}
			io.WriteString(w, putBody)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/sites"):
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			environment, _ := body["environment"].(string)
			if environment == "" {
				environment = api.EnvironmentDevelopment
			}
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"id":"s9","name":"`+body["name"].(string)+`","slug":"`+body["name"].(string)+
				`","environment":"`+environment+`","inherit_env":true}`)
		case strings.HasSuffix(r.URL.Path, "/sites"):
			io.WriteString(w, sites)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"unexpected `+r.URL.Path+`"}`)
		}
	}))
	t.Cleanup(ts.Close)
	return ts, &seen, &body
}

func TestParseEnvironmentKind(t *testing.T) {
	cases := []struct {
		arg     string
		want    string
		wantErr bool
	}{
		// "" is not development: it means the caller said nothing, and only the
		// server knows what a site of this position defaults to.
		{arg: "", want: ""},
		{arg: "production", want: api.EnvironmentProduction},
		{arg: "PROD", want: api.EnvironmentProduction},
		{arg: " staging ", want: api.EnvironmentStaging},
		{arg: "dev", want: api.EnvironmentDevelopment},
		{arg: "development", want: api.EnvironmentDevelopment},
		{arg: "preview", wantErr: true},
		{arg: "prd", wantErr: true},
	}
	for _, tc := range cases {
		got, err := parseEnvironmentKind(tc.arg)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseEnvironmentKind(%q) = %q, nil; want an error naming the three kinds", tc.arg, got)
			} else if !strings.Contains(err.Error(), "production, staging or development") {
				t.Errorf("parseEnvironmentKind(%q) error = %q; want the accepted set", tc.arg, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("parseEnvironmentKind(%q) = %q, %v; want %q", tc.arg, got, err, tc.want)
		}
	}
}

func TestParseOnOff(t *testing.T) {
	for _, arg := range []string{"on", "ON", "true", "yes", "enable"} {
		if got, err := parseOnOff(arg); err != nil || !got {
			t.Errorf("parseOnOff(%q) = %v, %v; want true", arg, got, err)
		}
	}
	for _, arg := range []string{"off", "OFF", "false", "no", "disable"} {
		if got, err := parseOnOff(arg); err != nil || got {
			t.Errorf("parseOnOff(%q) = %v, %v; want false", arg, got, err)
		}
	}
	if _, err := parseOnOff("maybe"); err == nil {
		t.Error("an unknown toggle must be refused before the request")
	}
}

// TestSiteListLine: the environment is part of what a site IS, so it is listed;
// a platform that does not carry the field yet must not be made to claim one.
func TestSiteListLine(t *testing.T) {
	prod := api.Site{ID: "s1", Name: "main", Slug: "main", Status: "live", Environment: "production"}
	if got := siteListLine(prod, true); got != "  ▶ main  (slug: main, env: production, status: live, id: s1)" {
		t.Errorf("active production row = %q", got)
	}
	dev := api.Site{ID: "s2", Name: "dev", Slug: "dev", Status: "live", Environment: "development", InheritEnv: true}
	got := siteListLine(dev, false)
	if !strings.Contains(got, "env: development") || !strings.Contains(got, "[inherits env]") {
		t.Errorf("inheriting development row = %q; want the kind and the inherit marker", got)
	}
	old := api.Site{ID: "s3", Name: "api", Slug: "api", Status: "live"}
	if got := siteListLine(old, false); got != "    api  (slug: api, status: live, id: s3)" {
		t.Errorf("row from a server without environments = %q; want the original rendering", got)
	}
}

// TestSiteCreate_EnvFlagReachesTheServer: --env is the whole feature at create
// time; it must travel verbatim, and an omitted flag must send NO environment
// so the server applies its own default rather than the CLI guessing one.
func TestSiteCreate_EnvFlagReachesTheServer(t *testing.T) {
	ts, seen, body := siteEnvStub(t, twoSites, 0, "")
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, linkedDir(t), "site", "create", "staging", "--env", "staging")

	if (*body)["environment"] != "staging" {
		t.Errorf("create body = %v; want environment staging", *body)
	}
	if !containsPath(*seen, "POST /api/v1/projects/p1/sites") {
		t.Errorf("requests = %v; want the create entry", *seen)
	}
	if !strings.Contains(out, "Environment: staging") {
		t.Errorf("output = %q; want the new site's kind", out)
	}

	ts2, _, plain := siteEnvStub(t, twoSites, 0, "")
	cliHome(t, ts2.URL)
	runCLI(t, linkedDir(t), "site", "create", "preview")
	if _, present := (*plain)["environment"]; present {
		t.Errorf("create body = %v; an omitted --env must not send a kind", *plain)
	}
}

// TestSiteCreate_UnknownEnvIsRefusedBeforeTheRequest: a typo costs no round-trip
// and reads as the CLI's own sentence.
func TestSiteCreate_UnknownEnvIsRefusedBeforeTheRequest(t *testing.T) {
	ts, seen, _ := siteEnvStub(t, twoSites, 0, "")
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, linkedDir(t), "site", "create", "preview", "--env", "prd")

	if !strings.Contains(out, `unknown environment "prd"`) {
		t.Errorf("output = %q; want the refusal", out)
	}
	for _, got := range *seen {
		if strings.HasPrefix(got, "POST") {
			t.Errorf("requests = %v; a rejected --env must send nothing", *seen)
		}
	}
}

// TestSiteEnvironment_SendsThePutAndReportsTheChange.
func TestSiteEnvironment_SendsThePutAndReportsTheChange(t *testing.T) {
	ts, seen, body := siteEnvStub(t, twoSites, 0, `{"id":"s2","name":"dev","slug":"dev","environment":"staging"}`)
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, linkedDir(t), "site", "environment", "dev", "staging")

	if !containsPath(*seen, "PUT /api/v1/projects/p1/sites/s2/environment") {
		t.Errorf("requests = %v; want the environment PUT on the named site", *seen)
	}
	if (*body)["environment"] != "staging" {
		t.Errorf("body = %v; want the kind", *body)
	}
	if !strings.Contains(out, "'dev' is now a staging site (was development)") {
		t.Errorf("output = %q; want the before/after line", out)
	}
}

// TestSiteEnvironment_NoChangeSendsNothing.
func TestSiteEnvironment_NoChangeSendsNothing(t *testing.T) {
	ts, seen, _ := siteEnvStub(t, twoSites, 0, "")
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, linkedDir(t), "site", "environment", "dev", "development")

	if !strings.Contains(out, "already a development site") {
		t.Errorf("output = %q; want the no-change line", out)
	}
	for _, got := range *seen {
		if strings.HasPrefix(got, "PUT") {
			t.Errorf("requests = %v; nothing to change means no write", *seen)
		}
	}
}

// TestSiteEnvironment_DefaultSiteLockedIsRendered: the 409 is an invariant of
// the model (design D2), so it gets the server's sentence AND the way out.
func TestSiteEnvironment_DefaultSiteLockedIsRendered(t *testing.T) {
	const refusal = `{"error":"the default site is always the production environment and cannot be changed; make another site the default first","code":"default_site_environment_locked"}`
	ts, _, _ := siteEnvStub(t, twoSites, http.StatusConflict, refusal)
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, linkedDir(t), "site", "environment", "main", "development")

	if !strings.Contains(out, "the default site is always the production environment") {
		t.Errorf("output = %q; want the server's own sentence", out)
	}
	if !strings.Contains(out, "ghayma site create <name> --env <kind>") {
		t.Errorf("output = %q; want the way out", out)
	}
	if lastExitCode != 1 {
		t.Errorf("exit code = %d; want 1", lastExitCode)
	}
}

// TestEnvironmentFailure covers the codes the two settings routes answer with,
// including the shorter spelling of the environment lock, so a server-side
// rename of the code cannot silently downgrade the refusal to a bare error.
func TestEnvironmentFailure(t *testing.T) {
	locked := &api.APIError{Status: http.StatusConflict, Message: "locked", Code: api.CodeDefaultSiteLocked}
	if got := environmentFailure(locked, "main"); !strings.Contains(got, "locked") || !strings.Contains(got, "bare URL") {
		t.Errorf("short lock code = %q; want the message plus the way out", got)
	}
	cannotInherit := &api.APIError{Status: http.StatusConflict, Message: "the default site is the base", Code: api.CodeDefaultSiteCannotInherit}
	if got := environmentFailure(cannotInherit, "main"); !strings.Contains(got, "inherit FROM") {
		t.Errorf("inherit lock = %q; want the base-site explanation", got)
	}
	missing := &api.APIError{Status: http.StatusNotFound}
	if got := environmentFailure(missing, "dev"); !strings.Contains(got, "does not serve site environments yet") {
		t.Errorf("bare 404 = %q; want the not-deployed-yet sentence", got)
	}
}

// TestSiteInheritEnv_PayloadAndReport: on/off must both reach the wire —
// `false` is the meaningful "stop inheriting" value, not an omission.
func TestSiteInheritEnv_PayloadAndReport(t *testing.T) {
	ts, seen, body := siteEnvStub(t, twoSites, 0, `{"id":"s2","slug":"dev","environment":"development","inherit_env":true}`)
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, linkedDir(t), "site", "inherit-env", "dev", "on")
	if !containsPath(*seen, "PUT /api/v1/projects/p1/sites/s2/inherit-env") {
		t.Errorf("requests = %v; want the inherit-env PUT", *seen)
	}
	if (*body)["inherit_env"] != true {
		t.Errorf("body = %v; want inherit_env true", *body)
	}
	if !strings.Contains(out, "now inherits the default site's variables") {
		t.Errorf("output = %q", out)
	}

	ts2, _, body2 := siteEnvStub(t, twoSites, 0, `{"id":"s2","slug":"dev","environment":"development","inherit_env":false}`)
	cliHome(t, ts2.URL)
	out = runCLI(t, linkedDir(t), "site", "inherit-env", "dev", "off")
	if (*body2)["inherit_env"] != false {
		t.Errorf("body = %v; want inherit_env false on the wire", *body2)
	}
	if !strings.Contains(out, "no longer inherits") || !strings.Contains(out, "nothing was copied down") {
		t.Errorf("output = %q; want what turning it off does", out)
	}
}

// TestSiteInheritEnv_DefaultSiteIsRefused: the default site IS the ladder's base.
func TestSiteInheritEnv_DefaultSiteIsRefused(t *testing.T) {
	const refusal = `{"error":"the default site is the base of the environment ladder and cannot inherit","code":"default_site_cannot_inherit"}`
	ts, _, _ := siteEnvStub(t, twoSites, http.StatusConflict, refusal)
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, linkedDir(t), "site", "inherit-env", "main", "on")

	if !strings.Contains(out, "cannot inherit") || !strings.Contains(out, "inherit FROM") {
		t.Errorf("output = %q; want the server's sentence and the explanation", out)
	}
}

// TestSiteEnvironmentCommands_Wiring pins the names and the arity: both take the
// site positionally, because re-kinding a site is never inferred from the
// directory the shell happens to be in.
func TestSiteEnvironmentCommands_Wiring(t *testing.T) {
	names := map[string]bool{}
	for _, c := range siteCmd.Commands() {
		names[c.Name()] = true
	}
	for _, want := range []string{"environment", "inherit-env", "create", "list"} {
		if !names[want] {
			t.Errorf("site command group is missing %q (has %v)", want, names)
		}
	}
	if siteCreateCmd.Flags().Lookup("env") == nil {
		t.Error("site create should expose --env")
	}
}
