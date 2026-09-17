package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// `ghayma deploy --image` is an ordinary deployment created from a different
// entry point: these tests pin that it takes the IMAGE entry (never the upload
// one), that it resolves a live site, and that it then reports exactly like a
// source deploy — including printing the build logs of a refused image, which
// is where admission says why.

// imageDeployStub serves the site list, the image deploy entry, the upload
// entry (so a mistaken upload is recorded rather than 404'd) and the polling
// route. deployment is the JSON the poll answers with.
func imageDeployStub(t *testing.T, sites, deployment string) (*httptest.Server, *[]string, *map[string]any) {
	t.Helper()
	var seen []string
	body := map[string]any{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/deployments/image"):
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"id":"dep-1","site_id":"s1","status":"queued","trigger":"image"}`)
		case strings.HasSuffix(r.URL.Path, "/sites"):
			io.WriteString(w, sites)
		case strings.HasSuffix(r.URL.Path, "/logs"):
			io.WriteString(w, `{"logs":"this image has no linux/amd64 variant"}`)
		case strings.HasPrefix(r.URL.Path, "/api/v1/deployments/"):
			io.WriteString(w, deployment)
		case r.URL.Path == "/api/v1/projects/p1":
			io.WriteString(w, `{"id":"p1","custom_dockerfile_enabled":false}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"unexpected `+r.URL.Path+`"}`)
		}
	}))
	t.Cleanup(ts.Close)
	return ts, &seen, &body
}

// TestDeployImage_TakesTheImageEntryAndNeverUploads is the wiring that matters:
// --image must not tar and upload the working directory.
func TestDeployImage_TakesTheImageEntryAndNeverUploads(t *testing.T) {
	ts, seen, body := imageDeployStub(t, oneSite, `{"id":"dep-1","status":"live","domains":["shop.ghayma.app"]}`)
	cliHome(t, ts.URL)
	noPrompt(t)
	fastPolls(t)

	out := runCLI(t, linkedDir(t), "deploy", "--image", "v1", "--prod")

	if !containsPath(*seen, "POST /api/v1/projects/p1/sites/s1/deployments/image") {
		t.Fatalf("requests = %v; want the image deploy entry", *seen)
	}
	for _, got := range *seen {
		if strings.Contains(got, "/deploy/upload") {
			t.Errorf("requests = %v; --image must never upload the source tree", *seen)
		}
	}
	if (*body)["tag"] != "v1" {
		t.Errorf("body = %v; want the tag", *body)
	}
	if _, present := (*body)["digest"]; present {
		t.Errorf("body = %v; a tag must not be sent as a digest too", *body)
	}
	if (*body)["is_production"] != true {
		t.Errorf("body = %v; --prod must reach the image deploy unchanged", *body)
	}
	if !strings.Contains(out, "🚀 Deploying image v1 to shop [site: main]") {
		t.Errorf("output = %q; want the image headline", out)
	}
	if !strings.Contains(out, "✅ Deployed successfully!") || !strings.Contains(out, "https://shop.ghayma.app") {
		t.Errorf("output = %q; want the same success report a source deploy prints", out)
	}
}

// TestDeployImage_DigestGoesInItsOwnField: "deploy v1" and "deploy this exact
// image" are different intents and the endpoint takes exactly one of them.
func TestDeployImage_DigestGoesInItsOwnField(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	ts, _, body := imageDeployStub(t, oneSite, `{"id":"dep-1","status":"live"}`)
	cliHome(t, ts.URL)
	noPrompt(t)
	fastPolls(t)

	runCLI(t, linkedDir(t), "deploy", "--image", digest)

	if (*body)["digest"] != digest {
		t.Errorf("body = %v; want the digest in its own field", *body)
	}
	if _, present := (*body)["tag"]; present {
		t.Errorf("body = %v; a digest must not also be sent as a tag", *body)
	}
}

// TestDeployImage_RefusedImagePrintsTheBuildLogs: admission refusals are a
// FAILED deployment whose logs carry the reason, so the existing failure
// reporting is what has to surface them.
func TestDeployImage_RefusedImagePrintsTheBuildLogs(t *testing.T) {
	ts, _, _ := imageDeployStub(t, oneSite, `{"id":"dep-1","status":"failed"}`)
	cliHome(t, ts.URL)
	noPrompt(t)
	fastPolls(t)

	out := runCLI(t, linkedDir(t), "deploy", "--image", "v1")

	if !strings.Contains(out, "❌ Deployment failed!") {
		t.Errorf("output = %q; want the failure line", out)
	}
	if !strings.Contains(out, "this image has no linux/amd64 variant") {
		t.Errorf("output = %q; want the admission refusal verbatim", out)
	}
}

// TestDeployImage_SiteLessProjectIsToldHowToGetASite: the image entry acts on
// an EXISTING site, and the CLI must not guess which kind of site to create —
// 'site create' and a first source deploy materialize different things.
func TestDeployImage_SiteLessProjectIsToldHowToGetASite(t *testing.T) {
	ts, seen, _ := imageDeployStub(t, `[]`, `{}`)
	cliHome(t, ts.URL)
	noPrompt(t)

	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{projectConfigName: `{"project_id":"p1","name":"shop","slug":"shop"}`})
	out := runCLI(t, dir, "deploy", "--image", "v1")

	if !strings.Contains(out, "deploying an image needs one") {
		t.Errorf("output = %q; want the site-less explanation", out)
	}
	if !strings.Contains(out, "ghayma site create main") || !strings.Contains(out, "ghayma deploy") {
		t.Errorf("output = %q; want both ways to get a site", out)
	}
	for _, got := range *seen {
		if strings.Contains(got, "/deployments/image") {
			t.Errorf("requests = %v; nothing to deploy to means no deploy call", *seen)
		}
	}
	if lastExitCode != 1 {
		t.Errorf("exit code = %d; want 1", lastExitCode)
	}
}

// TestDeployImage_ServerRefusalIsReported: a 404/403/409 from the entry is the
// user's answer, not a silent no-op.
func TestDeployImage_ServerRefusalIsReported(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/sites") {
			io.WriteString(w, oneSite)
			return
		}
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, `{"error":"this site is being renamed; try again shortly"}`)
	}))
	t.Cleanup(ts.Close)
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, linkedDir(t), "deploy", "--image", "v1")

	if !strings.Contains(out, "this site is being renamed") {
		t.Errorf("output = %q; want the server's own refusal", out)
	}
	if lastExitCode != 1 {
		t.Errorf("exit code = %d; want 1", lastExitCode)
	}
}

// TestDeployImage_Wiring pins the flag and the branch that keeps the source
// path out of an image deploy.
func TestDeployImage_Wiring(t *testing.T) {
	if deployCmd.Flags().Lookup("image") == nil {
		t.Fatal("deploy should expose --image")
	}
	src := readCmdSource(t, "deploy.go")
	image := strings.Index(src, "runImageDeploy(client, ctx")
	upload := strings.Index(src, "client.Deploy(")
	if image < 0 || upload < 0 || image > upload {
		t.Error("deploy.go must branch to the image deploy BEFORE anything of the upload path runs")
	}
}
