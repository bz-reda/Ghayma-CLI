package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"paas-cli/internal/api"

	"github.com/manifoldco/promptui"
)

// Shared "attach the current directory to an EXISTING project" flow, used by
// both `init` (its use-existing branch) and `link`. init used to only ever
// create a brand-new project, so there was no way to add a second monorepo app
// (e.g. apps/admin) to an existing project — deploy only sees dirs that already
// carry a .ghayma.json (2026-07-12).

// errAttachCancelled marks a user cancel (Ctrl-C / Ctrl-D) during an
// interactive attach so the caller aborts cleanly instead of falling through.
var errAttachCancelled = errors.New("cancelled")

// isCancel reports whether err is a promptui interrupt (Ctrl-C) or EOF
// (Ctrl-D). Every interactive prompt must treat these as "abort the command",
// never swallow them — swallowing the interrupt on init's first prompt is what
// dragged users into the billing/plan pickers with no way out but killing the
// terminal.
func isCancel(err error) bool {
	return errors.Is(err, promptui.ErrInterrupt) || errors.Is(err, promptui.ErrEOF)
}

// createNewIdx is the sentinel index the project/site pickers return for the
// "➕ Create a new …" option (offered first). It maps onto no slice element.
const createNewIdx = -1

// promptProjectChoiceFn / promptSiteChoiceFn / promptNewSiteNameFn are
// indirected so tests can drive the interactive control flow without a TTY.
var (
	promptProjectChoiceFn = promptProjectChoice
	promptSiteChoiceFn    = promptSiteChoice
	promptNewSiteNameFn   = promptNewSiteName
)

// projectChoiceLabels renders the existing-project picker: "create new" first,
// then one line per project. Pure so the format is unit-testable.
func projectChoiceLabels(projects []api.Project) []string {
	labels := make([]string, 0, len(projects)+1)
	labels = append(labels, "➕ Create a new project")
	for _, p := range projects {
		labels = append(labels, fmt.Sprintf("%s  (slug: %s, framework: %s)", p.Name, p.Slug, p.Framework))
	}
	return labels
}

// siteChoiceLabels renders the site picker: "create new" first, then one line
// per existing site. Pure.
func siteChoiceLabels(sites []api.Site) []string {
	labels := make([]string, 0, len(sites)+1)
	labels = append(labels, "➕ Create a new site")
	for _, s := range sites {
		labels = append(labels, fmt.Sprintf("%s  (slug: %s, status: %s)", s.Name, s.Slug, s.Status))
	}
	return labels
}

// promptProjectChoice shows the project picker and returns the index into
// projects, or createNewIdx for "create a new project". A cancel surfaces the
// promptui error.
func promptProjectChoice(projects []api.Project) (int, error) {
	sel := promptui.Select{
		Label: "Select a project (or create a new one)",
		Items: projectChoiceLabels(projects),
		Size:  10,
	}
	idx, _, err := sel.Run()
	if err != nil {
		return 0, err
	}
	return idx - 1, nil // item 0 = create-new → createNewIdx
}

// promptSiteChoice shows the site picker and returns the index into sites, or
// createNewIdx for "create a new site". A cancel surfaces the promptui error.
func promptSiteChoice(sites []api.Site) (int, error) {
	sel := promptui.Select{
		Label: "Select a site (or create a new one)",
		Items: siteChoiceLabels(sites),
		Size:  10,
	}
	idx, _, err := sel.Run()
	if err != nil {
		return 0, err
	}
	return idx - 1, nil
}

// promptNewSiteName asks for a new site's name. A cancel surfaces the error.
func promptNewSiteName() (string, error) {
	p := promptui.Prompt{Label: "New site name (e.g. admin, api)"}
	return p.Run()
}

// resolveOrCreateSite lists the project's sites and lets the user pick an
// existing one or create a new one (the missing capability that blocked adding
// a second monorepo app as its own site under an existing project). A cancel at
// any picker aborts with errAttachCancelled.
func resolveOrCreateSite(client *api.Client, projectID string) (*api.Site, error) {
	sites, err := client.ListSites(projectID)
	if err != nil {
		return nil, err
	}

	idx, err := promptSiteChoiceFn(sites)
	if err != nil {
		return nil, errAttachCancelled
	}

	if idx == createNewIdx {
		name, err := promptNewSiteNameFn()
		if err != nil {
			return nil, errAttachCancelled
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("site name cannot be empty")
		}
		return client.CreateSite(projectID, name)
	}

	if idx < 0 || idx >= len(sites) {
		return nil, fmt.Errorf("invalid site selection")
	}
	return &sites[idx], nil
}

// attachToExistingProject resolves (or creates) a site under project, writes a
// .ghayma.json into configDir pointing at it, and prints next steps. Shared by
// init and link. root_directory is left unset: deploy derives it from the
// filesystem when the config sits in a monorepo subdir.
func attachToExistingProject(client *api.Client, project *api.Project, configDir string) error {
	site, err := resolveOrCreateSite(client, project.ID)
	if err != nil {
		return err
	}

	cfg := ProjectConfig{
		ProjectID: project.ID,
		Name:      project.Name,
		Slug:      project.Slug,
		Framework: project.Framework,
		SiteID:    site.ID,
		SiteName:  site.Name,
		SiteSlug:  site.Slug,
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	path := projectConfigWritePath(configDir)
	if err := os.WriteFile(path, data, 0644); err != nil {
		return err
	}

	fmt.Printf("✅ Linked to project '%s' (slug: %s)\n", project.Name, project.Slug)
	fmt.Printf("   Site: %s (slug: %s)\n", site.Name, site.Slug)
	fmt.Printf("📁 Config saved to %s\n", path)
	fmt.Println("\nNext: run 'ghayma deploy --prod' to deploy")
	return nil
}
