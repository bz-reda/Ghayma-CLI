package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A per-app config that names no site — every config init wrote before
// 2026-07-23 looks like that — leaves `deploy --site` to the project's live
// site list. The upload must carry the site the flag picked: an upload without
// a site_id lets the server pick the default site, which is how a test deploy
// with --site tier-lab replaced docs.ghayma.cloud on 2026-09-29.

const mainAndLab = `[{"id":"s1","name":"main","slug":"main"},{"id":"s2","name":"tier-lab","slug":"tier-lab"}]`

// uploadStub serves the site list, the upload entry and the deployment poll.
// It records every request, and the upload's site_id form field ("" when the
// upload carried none).
func uploadStub(t *testing.T, sites string) (*httptest.Server, *[]string, *string) {
	t.Helper()
	var seen []string
	var uploadSiteID string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/deploy/upload":
			if err := r.ParseMultipartForm(32 << 20); err != nil {
				t.Errorf("upload is not a multipart form: %v", err)
			}
			uploadSiteID = r.FormValue("site_id")
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"deployment_id":"dep-1","status":"queued"}`)
		case strings.HasSuffix(r.URL.Path, "/sites"):
			io.WriteString(w, sites)
		case r.URL.Path == "/api/v1/deployments/dep-1":
			io.WriteString(w, `{"id":"dep-1","status":"live","domains":["shop.ghayma.app"]}`)
		case r.URL.Path == "/api/v1/projects/p1":
			io.WriteString(w, `{"id":"p1","custom_dockerfile_enabled":false}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"unexpected `+r.URL.Path+`"}`)
		}
	}))
	t.Cleanup(ts.Close)
	return ts, &seen, &uploadSiteID
}

func uploaded(seen []string) bool {
	return containsPath(seen, "POST /api/v1/deploy/upload")
}

func TestDeploy_SiteFlagOnSiteLessConfigDeploysThatSite(t *testing.T) {
	ts, seen, siteID := uploadStub(t, mainAndLab)
	cliHome(t, ts.URL)
	noPrompt(t)
	fastPolls(t)

	out := runCLI(t, legacyDir(t), "deploy", "--site", "tier-lab")

	if !uploaded(*seen) {
		t.Fatalf("requests = %v; want the upload\n%s", *seen, out)
	}
	if *siteID != "s2" {
		t.Errorf("upload site_id = %q; want s2, the site --site named", *siteID)
	}
	if !strings.Contains(out, "[site: tier-lab]") {
		t.Errorf("output = %q; want the headline to name the target site", out)
	}
}

// The docs promise --site takes a slug, a name or an id.
func TestDeploy_SiteFlagOnSiteLessConfigAcceptsAnID(t *testing.T) {
	ts, seen, siteID := uploadStub(t, mainAndLab)
	cliHome(t, ts.URL)
	noPrompt(t)
	fastPolls(t)

	out := runCLI(t, legacyDir(t), "deploy", "--site", "s2")

	if !uploaded(*seen) {
		t.Fatalf("requests = %v; want the upload\n%s", *seen, out)
	}
	if *siteID != "s2" {
		t.Errorf("upload site_id = %q; want s2", *siteID)
	}
}

func TestDeploy_UnknownSiteFlagOnSiteLessConfigUploadsNothing(t *testing.T) {
	ts, seen, _ := uploadStub(t, mainAndLab)
	cliHome(t, ts.URL)
	noPrompt(t)
	fastPolls(t)

	out := runCLI(t, legacyDir(t), "deploy", "--site", "nope")

	if uploaded(*seen) {
		t.Errorf("requests = %v; an unknown --site must stop before the upload", *seen)
	}
	if !strings.Contains(out, `site "nope" not found in this project (available: main, tier-lab)`) {
		t.Errorf("output = %q; want the unknown site named with the available ones", out)
	}
	if lastExitCode != 1 {
		t.Errorf("exit code = %d; want 1", lastExitCode)
	}
}

// A site-less project has nothing --site can name: deploying would create
// `main`, not the site the user asked for.
func TestDeploy_SiteFlagOnSiteLessProjectUploadsNothing(t *testing.T) {
	ts, seen, _ := uploadStub(t, `[]`)
	cliHome(t, ts.URL)
	noPrompt(t)
	fastPolls(t)

	out := runCLI(t, legacyDir(t), "deploy", "--site", "web")

	if uploaded(*seen) {
		t.Errorf("requests = %v; --site on a project with no site must stop before the upload", *seen)
	}
	if !strings.Contains(out, noSiteMessage) {
		t.Errorf("output = %q; want the site-less line", out)
	}
	if lastExitCode != 1 {
		t.Errorf("exit code = %d; want 1", lastExitCode)
	}
}

// Without --site a config naming no site keeps deploying where it always has:
// the server resolves the project's default site.
func TestDeploy_SiteLessConfigWithoutFlagLeavesTheSiteToTheServer(t *testing.T) {
	ts, seen, siteID := uploadStub(t, mainAndLab)
	cliHome(t, ts.URL)
	noPrompt(t)
	fastPolls(t)

	out := runCLI(t, legacyDir(t), "deploy")

	if !uploaded(*seen) {
		t.Fatalf("requests = %v; want the upload\n%s", *seen, out)
	}
	if *siteID != "" {
		t.Errorf("upload site_id = %q; want none, so the server picks the default site", *siteID)
	}
}
