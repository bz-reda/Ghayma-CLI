package cmd

import (
	"os"
	"strings"
	"testing"
)

func readCmdSource(t *testing.T, file string) string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	return string(data)
}

// TestInit_NoSwallowedCancel pins that init no longer throws away the promptui
// interrupt on its free-text prompts. Swallowing it (`x, _ := prompt.Run()`)
// let a Ctrl-C at the very first prompt fall through into the billing/plan
// pickers with no clean way out but killing the terminal (2026-07-12).
func TestInit_NoSwallowedCancel(t *testing.T) {
	src := readCmdSource(t, "init.go")
	for _, bad := range []string{
		"name, _ := namePrompt.Run()",
		"siteName, _ := sitePrompt.Run()",
		"domain, _ := domainPrompt.Run()",
	} {
		if strings.Contains(src, bad) {
			t.Errorf("init.go still swallows a prompt cancel: %q", bad)
		}
	}
}

// TestInit_OffersExistingProject pins the use-existing branch: init lists the
// caller's projects and routes an existing choice through the shared attach
// flow instead of only ever creating a brand-new project.
func TestInit_OffersExistingProject(t *testing.T) {
	src := readCmdSource(t, "init.go")
	for _, want := range []string{
		"client.ListProjects()",
		"promptProjectChoiceFn(",
		"attachToExistingProject(",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("init.go should reference %q for the use-existing flow", want)
		}
	}
}

// TestLink_CanCreateNewSite pins that link resolves the site through the shared
// attach flow (attachToExistingProject → resolveOrCreateSite), so the user can
// create a NEW site (e.g. admin) under an existing project — not only pick an
// already-existing one — and drops its old pick-only site picker.
func TestLink_CanCreateNewSite(t *testing.T) {
	src := readCmdSource(t, "link.go")
	if !strings.Contains(src, "attachToExistingProject(") {
		t.Error("link.go should route through attachToExistingProject so a new site can be created during link")
	}
	if strings.Contains(src, `Label: "Select a site"`) {
		t.Error("link.go should no longer use its own pick-only site picker")
	}
}
