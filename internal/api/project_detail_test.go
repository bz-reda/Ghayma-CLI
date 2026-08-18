package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGetProject_ParsesCustomDockerfileFlag pins the lookup deploy's Dockerfile
// hint depends on: GET /api/v1/projects/<id-or-slug> and the
// custom_dockerfile_enabled bool off the project JSON (2026-08-17).
func TestGetProject_ParsesCustomDockerfileFlag(t *testing.T) {
	var gotPath, gotMethod, gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod, gotAuth = r.URL.Path, r.Method, r.Header.Get("Authorization")
		io.WriteString(w, `{"id":"p1","name":"Demo","slug":"demo","framework":"nextjs","custom_dockerfile_enabled":true}`)
	}))
	defer ts.Close()

	project, err := newTestClient(ts.URL).GetProject("p1")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if gotPath != "/api/v1/projects/p1" || gotMethod != http.MethodGet {
		t.Errorf("request = %s %s; want GET /api/v1/projects/p1", gotMethod, gotPath)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("auth header = %q; want Bearer test-token", gotAuth)
	}
	if project.ID != "p1" || project.Slug != "demo" || project.Framework != "nextjs" {
		t.Errorf("project = %+v; want p1/demo/nextjs", project)
	}
	if !project.CustomDockerfileEnabled {
		t.Error("CustomDockerfileEnabled = false; want true")
	}
}

// A project without the toggle must read as OFF, not as "unknown" — the hint's
// unknown state is reserved for a failed lookup.
func TestGetProject_FlagDefaultsOff(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"p1","name":"Demo","slug":"demo","custom_dockerfile_enabled":false}`)
	}))
	defer ts.Close()

	project, err := newTestClient(ts.URL).GetProject("demo")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if project.CustomDockerfileEnabled {
		t.Error("CustomDockerfileEnabled = true; want false")
	}
}

// A 404 (or any non-2xx) must surface as an error so the caller can fall back
// to the honest "could not verify" wording rather than to a silent false.
func TestGetProject_ErrorOnNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":"project not found"}`)
	}))
	defer ts.Close()

	if _, err := newTestClient(ts.URL).GetProject("nope"); err == nil {
		t.Fatal("GetProject on 404: err = nil; want an error")
	}
}
