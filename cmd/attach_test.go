package cmd

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"paas-cli/internal/api"

	"github.com/manifoldco/promptui"
)

// TestIsCancel pins the Ctrl-C / Ctrl-D detection the whole cancel discipline
// rests on: promptui returns ErrInterrupt (^C) / ErrEOF (^D) and nothing else
// counts as a user cancel.
func TestIsCancel(t *testing.T) {
	if !isCancel(promptui.ErrInterrupt) {
		t.Error("ErrInterrupt (Ctrl-C) must be a cancel")
	}
	if !isCancel(promptui.ErrEOF) {
		t.Error("ErrEOF (Ctrl-D) must be a cancel")
	}
	if isCancel(nil) {
		t.Error("nil is not a cancel")
	}
	if isCancel(errors.New("boom")) {
		t.Error("a generic error is not a cancel")
	}
}

// TestProjectChoiceLabels: the existing-project picker offers "create new"
// FIRST, then one line per project carrying name/slug/framework.
func TestProjectChoiceLabels(t *testing.T) {
	projects := []api.Project{
		{ID: "p1", Name: "forge", Slug: "forge", Framework: "nextjs"},
		{ID: "p2", Name: "shop", Slug: "shop", Framework: "static"},
	}
	labels := projectChoiceLabels(projects)
	if len(labels) != 3 {
		t.Fatalf("got %d labels; want 3 (create-new + 2 projects)", len(labels))
	}
	if !strings.Contains(labels[0], "Create a new project") {
		t.Errorf("labels[0] = %q; want the create-new option first", labels[0])
	}
	if !strings.Contains(labels[1], "forge") || !strings.Contains(labels[1], "nextjs") {
		t.Errorf("labels[1] = %q; want it to name the project + framework", labels[1])
	}
}

// TestSiteChoiceLabels: the site picker offers "create new" FIRST, then one
// line per existing site.
func TestSiteChoiceLabels(t *testing.T) {
	sites := []api.Site{
		{ID: "s1", Name: "main", Slug: "forge", Status: "running"},
	}
	labels := siteChoiceLabels(sites)
	if len(labels) != 2 {
		t.Fatalf("got %d labels; want 2 (create-new + 1 site)", len(labels))
	}
	if !strings.Contains(labels[0], "Create a new site") {
		t.Errorf("labels[0] = %q; want the create-new option first", labels[0])
	}
	if !strings.Contains(labels[1], "main") {
		t.Errorf("labels[1] = %q; want it to name the site", labels[1])
	}
}

func attachTestClient(url string) *api.Client { return planTestClient(url) }

// TestResolveOrCreateSite_PickExisting: choosing an existing site returns it
// and never calls CreateSite.
func TestResolveOrCreateSite_PickExisting(t *testing.T) {
	created := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			created = true
		}
		_, _ = w.Write([]byte(`[{"id":"s1","name":"main","slug":"forge","status":"running"},{"id":"s2","name":"admin","slug":"forge-admin","status":"running"}]`))
	}))
	defer ts.Close()

	orig := promptSiteChoiceFn
	defer func() { promptSiteChoiceFn = orig }()
	promptSiteChoiceFn = func(sites []api.Site) (int, error) { return 1, nil } // pick "admin"

	site, err := resolveOrCreateSite(attachTestClient(ts.URL), "p1")
	if err != nil {
		t.Fatalf("resolveOrCreateSite: %v", err)
	}
	if site.ID != "s2" {
		t.Errorf("picked site = %q; want s2 (admin)", site.ID)
	}
	if created {
		t.Error("picking an existing site must not POST a new site")
	}
}

// TestResolveOrCreateSite_CreateNew: choosing "create new" prompts for a name
// and creates the site via the API.
func TestResolveOrCreateSite_CreateNew(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"s9","name":"admin","slug":"forge-admin","status":"creating"}`))
			return
		}
		_, _ = w.Write([]byte(`[{"id":"s1","name":"main","slug":"forge","status":"running"}]`))
	}))
	defer ts.Close()

	origChoice := promptSiteChoiceFn
	origName := promptNewSiteNameFn
	defer func() { promptSiteChoiceFn = origChoice; promptNewSiteNameFn = origName }()
	promptSiteChoiceFn = func(sites []api.Site) (int, error) { return createNewIdx, nil }
	promptNewSiteNameFn = func() (string, error) { return "admin", nil }

	site, err := resolveOrCreateSite(attachTestClient(ts.URL), "p1")
	if err != nil {
		t.Fatalf("resolveOrCreateSite(create): %v", err)
	}
	if site.ID != "s9" || site.Name != "admin" {
		t.Errorf("created site = %+v; want the new admin site (s9)", site)
	}
}

// TestResolveOrCreateSite_Cancel: a Ctrl-C at the site picker aborts with
// errAttachCancelled — it must never fall through to a default.
func TestResolveOrCreateSite_Cancel(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"s1","name":"main","slug":"forge","status":"running"}]`))
	}))
	defer ts.Close()

	orig := promptSiteChoiceFn
	defer func() { promptSiteChoiceFn = orig }()
	promptSiteChoiceFn = func(sites []api.Site) (int, error) { return 0, promptui.ErrInterrupt }

	_, err := resolveOrCreateSite(attachTestClient(ts.URL), "p1")
	if !errors.Is(err, errAttachCancelled) {
		t.Fatalf("err = %v; want errAttachCancelled", err)
	}
}

// TestAttachToExistingProject_WritesConfig: the shared attach flow writes a
// .ghayma.json in the target dir pointing at the chosen project + site.
func TestAttachToExistingProject_WritesConfig(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"s1","name":"main","slug":"forge","status":"running"}]`))
	}))
	defer ts.Close()

	orig := promptSiteChoiceFn
	defer func() { promptSiteChoiceFn = orig }()
	promptSiteChoiceFn = func(sites []api.Site) (int, error) { return 0, nil } // pick main

	dir := t.TempDir()
	project := &api.Project{ID: "p1", Name: "forge", Slug: "forge", Framework: "nextjs"}
	if err := attachToExistingProject(attachTestClient(ts.URL), project, dir); err != nil {
		t.Fatalf("attachToExistingProject: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, projectConfigName))
	if err != nil {
		t.Fatalf("expected %s written: %v", projectConfigName, err)
	}
	var cfg ProjectConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("config not valid JSON: %v", err)
	}
	if cfg.ProjectID != "p1" || cfg.SiteID != "s1" || cfg.SiteName != "main" {
		t.Errorf("config = %+v; want project p1 + site main(s1)", cfg)
	}
}
