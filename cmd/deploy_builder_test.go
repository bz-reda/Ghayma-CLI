package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// builderStub serves a deploy end to end: the site list, an empty unconnected
// listing, the upload and a poll that walks queued → building → deploying →
// live. It records every request and the upload's `builder` form values (nil
// when the upload carried none).
func builderStub(t *testing.T, sites string) (*httptest.Server, *[]string, *[]string) {
	t.Helper()
	var seen []string
	var builder []string
	statuses := []string{"queued", "building", "deploying", "live"}
	polls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/deploy/upload":
			if err := r.ParseMultipartForm(32 << 20); err != nil {
				t.Errorf("upload is not a multipart form: %v", err)
			}
			builder = r.MultipartForm.Value["builder"]
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"deployment_id":"dep-1","status":"queued","site_id":"s1"}`)
		case r.URL.Path == "/api/v1/projects/p1/sites":
			io.WriteString(w, sites)
		case r.URL.Path == "/api/v1/projects/p1/connections/unconnected":
			io.WriteString(w, `{"unconnected":[]}`)
		case r.URL.Path == "/api/v1/deployments/dep-1":
			status := statuses[min(polls, len(statuses)-1)]
			polls++
			io.WriteString(w, `{"id":"dep-1","status":"`+status+`","queue_position":1,"queue_size":1,"domains":["shop.ghayma.app"]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"unexpected `+r.URL.Path+`"}`)
		}
	}))
	t.Cleanup(ts.Close)
	return ts, &seen, &builder
}

const (
	webSiteAuto       = `[{"id":"s1","name":"web","slug":"web","is_default":true,"build_settings":{"builder":"auto"}}]`
	webSiteDockerfile = `[{"id":"s1","name":"web","slug":"web","is_default":true,"build_settings":{"builder":"dockerfile"}}]`
)

// workspaceWithBuilder is a workspace manifest whose one site sets builder.
func workspaceWithBuilder(t *testing.T, builder string) string {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		projectConfigName: `{"project_id":"p1","name":"shop","slug":"shop","sites":[` +
			`{"site_id":"s1","site_slug":"web","root_directory":"apps/web","upload":"app","builder":"` + builder + `"}]}`,
		"apps/web/package.json": `{"name":"web"}`,
		"apps/web/Dockerfile":   "FROM scratch\n",
	})
	return dir
}

// perAppWithBuilder is the single-site root form: a per-app config at the root.
func perAppWithBuilder(t *testing.T, builder string) string {
	t.Helper()
	dir := t.TempDir()
	cfg := `{"project_id":"p1","name":"shop","slug":"shop","site_id":"s1","site_slug":"web"`
	if builder != "" {
		cfg += `,"builder":"` + builder + `"`
	}
	writeFiles(t, dir, map[string]string{
		projectConfigName: cfg + "}",
		"Dockerfile":      "FROM scratch\n",
	})
	return dir
}

func deployQuietly(t *testing.T, url string) {
	t.Helper()
	cliHome(t, url)
	noPrompt(t)
	forceStdin(t, false)
	fastPolls(t)
}

// A manifest site's builder rides on the upload: the server persists it onto
// the site before this build runs.
func TestDeploy_ManifestBuilderIsSent(t *testing.T) {
	ts, seen, builder := builderStub(t, webSiteAuto)
	deployQuietly(t, ts.URL)

	out := runCLI(t, workspaceWithBuilder(t, "dockerfile"), "deploy")

	if !uploaded(*seen) {
		t.Fatalf("requests = %v; want the upload\n%s", *seen, out)
	}
	if !reflect.DeepEqual(*builder, []string{"dockerfile"}) {
		t.Errorf("form builder = %q; want [dockerfile]", *builder)
	}
}

// The single-site root form carries the same key.
func TestDeploy_PerAppBuilderIsSent(t *testing.T) {
	ts, seen, builder := builderStub(t, webSiteDockerfile)
	deployQuietly(t, ts.URL)

	out := runCLI(t, perAppWithBuilder(t, "auto"), "deploy")

	if !uploaded(*seen) {
		t.Fatalf("requests = %v; want the upload\n%s", *seen, out)
	}
	if !reflect.DeepEqual(*builder, []string{"auto"}) {
		t.Errorf("form builder = %q; want [auto]", *builder)
	}
}

// Without a builder in the config the field is left out and the site's stored
// choice applies.
func TestDeploy_NoConfigBuilderSendsNoField(t *testing.T) {
	ts, seen, builder := builderStub(t, webSiteDockerfile)
	deployQuietly(t, ts.URL)

	out := runCLI(t, perAppWithBuilder(t, ""), "deploy")

	if !uploaded(*seen) {
		t.Fatalf("requests = %v; want the upload\n%s", *seen, out)
	}
	if *builder != nil {
		t.Errorf("form builder = %q; want no field", *builder)
	}
}

// A deploy prints its headline, one dot per poll on the same line, then the
// result: no ignore rules, no Dockerfile notice, no deployment id, no queue
// position — even with a Dockerfile and a .dockerignore in the upload.
func TestDeploy_PrintsOnlyTheHeadlineAndProgress(t *testing.T) {
	ts, seen, _ := builderStub(t, webSiteAuto)
	deployQuietly(t, ts.URL)
	dir := perAppWithBuilder(t, "")
	writeFiles(t, dir, map[string]string{".dockerignore": "node_modules\n*.md\n"})

	out := runCLI(t, dir, "deploy")

	want := "🚀 Deploying shop......\n✅ Deployed successfully!\n🌐 Your app is live at:\n   https://shop.ghayma.app\n"
	if out != want {
		t.Errorf("output:\n got  %q\n want %q", out, want)
	}
	if containsPath(*seen, "GET /api/v1/projects/p1") {
		t.Errorf("requests = %v; the deploy has no reason to read the project", *seen)
	}
}

// A builder the platform does not know stops the deploy before anything is
// uploaded, naming the file.
func TestDeploy_UnknownBuilderUploadsNothing(t *testing.T) {
	ts, seen, _ := builderStub(t, webSiteAuto)
	deployQuietly(t, ts.URL)

	out := runCLI(t, workspaceWithBuilder(t, "docker"), "deploy")

	if uploaded(*seen) {
		t.Errorf("requests = %v; an unknown builder must stop before the upload", *seen)
	}
	if !strings.Contains(out, `site "web" has an unknown builder "docker" in `) ||
		!strings.Contains(out, `(expected "auto" or "dockerfile")`) {
		t.Errorf("output = %q; want the unknown builder named", out)
	}
	if lastExitCode != 1 {
		t.Errorf("exit code = %d; want 1", lastExitCode)
	}
}
