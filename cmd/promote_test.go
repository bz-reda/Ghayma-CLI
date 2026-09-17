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

// Promotion (ENVIRONMENTS-DESIGN-2026-09-17 §4). Two things decide whether this
// command is safe: WHICH SITE it lands on, and that the resulting row is polled
// like any other deployment. Both are pinned here.

// promoteStub serves the site list, the promote entry and the polling routes.
func promoteStub(t *testing.T, sites string, status int, response, deployment string) (*httptest.Server, *[]string, *map[string]any) {
	t.Helper()
	var seen []string
	body := map[string]any{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/promote"):
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			if status != 0 {
				w.WriteHeader(status)
			} else {
				w.WriteHeader(http.StatusCreated)
			}
			io.WriteString(w, response)
		case strings.HasSuffix(r.URL.Path, "/sites"):
			io.WriteString(w, sites)
		case strings.HasSuffix(r.URL.Path, "/logs"):
			io.WriteString(w, `{"logs":"admission refused the promoted manifest"}`)
		case strings.HasPrefix(r.URL.Path, "/api/v1/deployments/"):
			io.WriteString(w, deployment)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"unexpected `+r.URL.Path+`"}`)
		}
	}))
	t.Cleanup(ts.Close)
	return ts, &seen, &body
}

const promoteAccepted = `{"id":"dep-9","site_id":"s1","status":"queued","trigger":"promote",` +
	`"source_image_ref":"p1-dev:20260917-1","source_image_digest":"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",` +
	`"source_site":"dev","source_deployment_id":"dep-1"}`

// TestResolvePromoteTarget covers the rule that decides where a promotion
// lands. The default is the project's DEFAULT site rather than the directory's
// linked one: promotion is run from a development context, where the linked
// site is the SOURCE, and defaulting to it would make the common invocation a
// self-promotion the server refuses.
func TestResolvePromoteTarget(t *testing.T) {
	sites := []api.Site{
		{ID: "s2", Slug: "dev", Environment: "development"},
		{ID: "s1", Slug: "main", IsDefault: true, Environment: "production"},
	}

	site, notice, err := resolvePromoteTarget(sites, "")
	if err != nil || site.ID != "s1" {
		t.Fatalf("no --site → %+v, %v; want the default site", site, err)
	}
	if !strings.Contains(notice, "'main'") || !strings.Contains(notice, "production") || !strings.Contains(notice, "--site") {
		t.Errorf("notice = %q; a defaulted target must name itself, its kind and how to change it", notice)
	}

	site, notice, err = resolvePromoteTarget(sites, "dev")
	if err != nil || site.ID != "s2" || notice != "" {
		t.Errorf("--site dev → %+v, %q, %v; want the named site and no notice", site, notice, err)
	}

	if _, _, err = resolvePromoteTarget(sites, "nope"); err == nil || !strings.Contains(err.Error(), "available: dev, main") {
		t.Errorf("unknown --site error = %v; want the available slugs", err)
	}

	// An older platform marks no default: there is nothing to infer, so ask.
	noDefault := []api.Site{{ID: "s1", Slug: "main"}, {ID: "s2", Slug: "dev"}}
	if _, _, err = resolvePromoteTarget(noDefault, ""); err == nil || !strings.Contains(err.Error(), "--site") {
		t.Errorf("no default site → %v; want a request for --site rather than a guess", err)
	}

	if _, _, err = resolvePromoteTarget(nil, ""); err == nil {
		t.Error("a site-less project cannot be a promotion target")
	}
}

// TestPromote_DefaultsToTheDefaultSiteAndSaysSo is the whole-command shape:
// the target is named before anything is sent, the payload carries the source,
// and the result is polled like any deploy.
func TestPromote_DefaultsToTheDefaultSiteAndSaysSo(t *testing.T) {
	ts, seen, body := promoteStub(t, twoSites, 0, promoteAccepted, `{"id":"dep-9","status":"live","domains":["shop.ghayma.app"]}`)
	cliHome(t, ts.URL)
	noPrompt(t)
	fastPolls(t)

	out := runCLI(t, linkedDir(t), "promote", "--from", "dev")

	if !containsPath(*seen, "POST /api/v1/projects/p1/sites/s1/promote") {
		t.Fatalf("requests = %v; want the promote entry on the DEFAULT site", *seen)
	}
	if (*body)["from_site"] != "dev" {
		t.Errorf("body = %v; want the source site", *body)
	}
	if _, present := (*body)["deployment_id"]; present {
		t.Errorf("body = %v; without --deployment the server picks the live one", *body)
	}
	if !strings.Contains(out, "Target: the project's default site 'main' (production)") {
		t.Errorf("output = %q; want the target named before the request", out)
	}
	if !strings.Contains(out, "🚀 Promoting dev → main (image sha256:0123456789ab)") {
		t.Errorf("output = %q; want the promotion headline with the short digest", out)
	}
	if !strings.Contains(out, "✅ Deployed successfully!") || !strings.Contains(out, "https://shop.ghayma.app") {
		t.Errorf("output = %q; a promotion reports like any other deploy", out)
	}
}

// TestPromote_SiteAndDeploymentFlagsTravel.
func TestPromote_SiteAndDeploymentFlagsTravel(t *testing.T) {
	ts, seen, body := promoteStub(t, twoSites, 0, promoteAccepted, `{"id":"dep-9","status":"live"}`)
	cliHome(t, ts.URL)
	noPrompt(t)
	fastPolls(t)

	out := runCLI(t, linkedDir(t), "promote", "--from", "main", "--site", "dev", "--deployment", "dep-42")

	if !containsPath(*seen, "POST /api/v1/projects/p1/sites/s2/promote") {
		t.Errorf("requests = %v; --site must choose the target", *seen)
	}
	if (*body)["from_site"] != "main" || (*body)["deployment_id"] != "dep-42" {
		t.Errorf("body = %v; want both flags on the wire", *body)
	}
	if strings.Contains(out, "Target: the project's default site") {
		t.Errorf("output = %q; an explicit --site needs no defaulting notice", out)
	}
}

// TestPromote_RequiresFrom: there is no sensible default source — the whole
// command is "from where".
func TestPromote_RequiresFrom(t *testing.T) {
	ts, seen, _ := promoteStub(t, twoSites, 0, promoteAccepted, `{}`)
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, linkedDir(t), "promote")

	if !strings.Contains(out, "--from") {
		t.Errorf("output = %q; want the missing-source message", out)
	}
	if len(*seen) != 0 {
		t.Errorf("requests = %v; nothing should be sent", *seen)
	}
	if lastExitCode != 1 {
		t.Errorf("exit code = %d; want 1", lastExitCode)
	}
}

// TestPromote_ServerRefusalIsTheUsersAnswer: every refusal is written for the
// customer, and the 409 gains the CLI's own way out because the server's
// sentence names the API field, not the flag.
func TestPromote_ServerRefusalIsTheUsersAnswer(t *testing.T) {
	const refusal = `{"error":"dev has no live deployment to promote. Deploy it first, or name a deployment with \"deployment_id\"."}`
	ts, _, _ := promoteStub(t, twoSites, http.StatusConflict, refusal, `{}`)
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, linkedDir(t), "promote", "--from", "dev")

	if !strings.Contains(out, "dev has no live deployment to promote") {
		t.Errorf("output = %q; want the server's own refusal", out)
	}
	if !strings.Contains(out, "ghayma promote --from dev --deployment <id>") {
		t.Errorf("output = %q; want the CLI's way out", out)
	}
	if lastExitCode != 1 {
		t.Errorf("exit code = %d; want 1", lastExitCode)
	}
}

// TestPromote_FailedPromotionPrintsTheBuildLogs: admission re-runs against the
// target (design D5), and a refusal lands as a FAILED deployment whose logs say
// why — the same reporting an image deploy gets.
func TestPromote_FailedPromotionPrintsTheBuildLogs(t *testing.T) {
	ts, _, _ := promoteStub(t, twoSites, 0, promoteAccepted, `{"id":"dep-9","status":"failed"}`)
	cliHome(t, ts.URL)
	noPrompt(t)
	fastPolls(t)

	out := runCLI(t, linkedDir(t), "promote", "--from", "dev")

	if !strings.Contains(out, "❌ Deployment failed!") || !strings.Contains(out, "admission refused the promoted manifest") {
		t.Errorf("output = %q; want the failure and its logs", out)
	}
}

func TestShortDigestAndHeadline(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	if got := shortDigest(digest); got != "sha256:aaaaaaaaaaaa" {
		t.Errorf("shortDigest = %q", got)
	}
	if got := shortDigest("p1-dev:v1"); got != "p1-dev:v1" {
		t.Errorf("a tag must survive untouched, got %q", got)
	}
	// No digest yet (an older server, or a body that carried only the ref):
	// the headline still says what is happening.
	p := &api.Promotion{SourceSite: "dev"}
	if got := promoteHeadline(p, "dev", "main"); got != "🚀 Promoting dev → main" {
		t.Errorf("headline without an image = %q", got)
	}
}
