package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"paas-cli/internal/api"

	"github.com/manifoldco/promptui"
)

// Linking a whole workspace (2026-08-16). At a workspace root `link` used to
// have exactly one shape: pick ONE app subdirectory and write a per-app config
// into it. A 4-app pnpm workspace therefore took four runs, and a repo without
// turbo.json could never be deployed from its root. The whole-project flow maps
// every site to the directory that builds it and writes that as one manifest.

// Link modes offered at a workspace root.
const (
	linkModeWholeProject = iota
	linkModeAppSubdir
)

// skipSiteIdx is the sentinel the directory picker returns for "⏭ skip this
// site": the site is left out of the manifest rather than mapped to a
// directory the user didn't choose.
const skipSiteIdx = -1

// promptLinkModeFn / promptSiteDirFn are indirected so tests can drive the
// interactive flow without a TTY.
var (
	promptLinkModeFn = promptLinkMode
	promptSiteDirFn  = promptSiteDir
)

// linkModeLabels names both outcomes, including the file each one writes —
// the difference between them is where the config lands.
func linkModeLabels() []string {
	return []string{
		linkModeWholeProject: fmt.Sprintf("Whole project — every site, one manifest at ./%s (recommended for monorepos)", projectConfigName),
		linkModeAppSubdir:    fmt.Sprintf("One app subdirectory — writes apps/<dir>/%s", projectConfigName),
	}
}

func promptLinkMode() (int, error) {
	sel := promptui.Select{
		Label: "How do you want to link this workspace?",
		Items: linkModeLabels(),
	}
	idx, _, err := sel.Run()
	return idx, err
}

// siteDirChoiceLabels lists the directories still unclaimed, plus a skip.
func siteDirChoiceLabels(dirs []string) []string {
	labels := make([]string, 0, len(dirs)+1)
	labels = append(labels, dirs...)
	return append(labels, "⏭ skip this site")
}

// promptSiteDir asks which directory backs a site, returning an index into
// dirs or skipSiteIdx.
func promptSiteDir(site api.Site, dirs []string) (int, error) {
	sel := promptui.Select{
		Label: fmt.Sprintf("Which directory holds site %q?", siteIdentity(site)),
		Items: siteDirChoiceLabels(dirs),
		Size:  10,
	}
	idx, _, err := sel.Run()
	if err != nil {
		return 0, err
	}
	if idx >= len(dirs) {
		return skipSiteIdx, nil
	}
	return idx, nil
}

// siteIdentity is how a site is named back to the user: its slug, else name.
func siteIdentity(site api.Site) string {
	if site.Slug != "" {
		return site.Slug
	}
	return site.Name
}

// linkWorkspaceRoot runs the workspace-root branch of `link`. It reports
// whether it handled the command: false means the user chose the per-app
// subdirectory flow, which the caller continues with unchanged.
func linkWorkspaceRoot(client *api.Client, projects []api.Project, args []string, root string) (bool, error) {
	if existingPath, err := findProjectConfig(root); err == nil {
		data, readErr := os.ReadFile(existingPath)
		if readErr == nil && isManifest(data) {
			fmt.Printf("⚠️  This workspace is already linked (./%s manifest). Delete it to re-link.\n", filepath.Base(existingPath))
			return true, nil
		}
	}

	// The picker is the whole interface here; without a terminal there is no
	// safe default to fall back on.
	if !stdinIsTerminalFn() {
		return true, fmt.Errorf("run 'ghayma link' interactively at a workspace root")
	}

	mode, err := promptLinkModeFn()
	if err != nil {
		return true, errAttachCancelled
	}
	if mode != linkModeWholeProject {
		return false, nil
	}

	// Whole-project linking writes ./.ghayma.json, so a per-app config sitting
	// at the root would be overwritten. Stop instead.
	if existingPath, err := findProjectConfig(root); err == nil {
		return true, fmt.Errorf("%s already exists and is a per-app config; delete or move it before linking the whole workspace", existingPath)
	}

	project, err := selectLinkProject(projects, args)
	if err != nil {
		return true, err
	}

	sites, err := client.ListSites(project.ID)
	if err != nil {
		return true, fmt.Errorf("failed to list sites: %w", err)
	}
	if len(sites) == 0 {
		return true, fmt.Errorf("project '%s' has no sites yet — create one with 'ghayma site create <name>', then re-run link", project.Name)
	}

	dirs := workspaceAppDirs(root)
	if len(dirs) == 0 {
		return true, fmt.Errorf("no app directories found in this workspace (looked for a package.json under the workspace globs)")
	}

	existing := existingAppConfigs(root, dirs)
	mapped, unmatched, remaining := autoMapSites(sites, dirs, existing)

	for _, site := range unmatched {
		if len(remaining) == 0 {
			fmt.Printf("ℹ️  No directory left for site '%s' — left out of the manifest.\n", siteIdentity(site))
			continue
		}
		idx, err := promptSiteDirFn(site, remaining)
		if err != nil {
			return true, errAttachCancelled
		}
		if idx == skipSiteIdx || idx < 0 || idx >= len(remaining) {
			fmt.Printf("ℹ️  Site '%s' skipped — left out of the manifest.\n", siteIdentity(site))
			continue
		}
		mapped[site.ID] = remaining[idx]
		remaining = append(remaining[:idx], remaining[idx+1:]...)
	}

	if len(mapped) == 0 {
		return true, fmt.Errorf("no site was mapped to a directory; nothing written")
	}

	manifest := buildWorkspaceManifest(project, sites, mapped, existing, defaultUploadMode(root))
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return true, err
	}
	manifestPath := projectConfigWritePath(root)
	if err := os.WriteFile(manifestPath, data, 0644); err != nil {
		return true, err
	}

	fmt.Printf("✅ Linked workspace to project '%s' (slug: %s)\n", project.Name, project.Slug)
	for _, line := range manifestSummaryLines(manifest) {
		fmt.Println(line)
	}
	fmt.Printf("📁 Manifest saved to %s\n", manifestPath)
	if len(existing) > 0 {
		fmt.Println("ℹ️  Existing per-app config files were left in place; the manifest now covers those directories, so you may delete them.")
	}
	fmt.Println("\nNext: run 'ghayma deploy' here and pick a site, or 'ghayma deploy --site <slug> --prod'")
	return true, nil
}

// existingAppConfigs reads whatever per-app configs the workspace already has,
// keyed by root-relative directory. Manifests are skipped: a nested manifest
// describes its own workspace, not one app.
func existingAppConfigs(root string, dirs []string) map[string]perAppConfig {
	found := make(map[string]perAppConfig)
	for _, dir := range dirs {
		data, err := readProjectConfig(filepath.Join(root, filepath.FromSlash(dir)))
		if err != nil || isManifest(data) {
			continue
		}
		var cfg perAppConfig
		if json.Unmarshal(data, &cfg) != nil {
			continue
		}
		found[dir] = cfg
	}
	return found
}

// autoMapSites maps sites onto app directories without asking, returning the
// mapping (site id → directory), the sites it could not place, and the
// directories still free.
//
// Two passes, because the priority is across sites and not just within one: a
// directory the user already linked to a site (its per-app config carries that
// site_id) must win over any directory that merely looks like the site's name.
// A directory backs at most one site.
func autoMapSites(sites []api.Site, dirs []string, existing map[string]perAppConfig) (map[string]string, []api.Site, []string) {
	mapped := make(map[string]string, len(sites))
	taken := make(map[string]bool, len(dirs))

	for _, site := range sites {
		for _, dir := range dirs {
			if taken[dir] {
				continue
			}
			if cfg, ok := existing[dir]; ok && cfg.SiteID != "" && cfg.SiteID == site.ID {
				mapped[site.ID] = dir
				taken[dir] = true
				break
			}
		}
	}

	for _, site := range sites {
		if _, done := mapped[site.ID]; done {
			continue
		}
		for _, dir := range dirs {
			if taken[dir] {
				continue
			}
			base := path.Base(dir)
			if (site.Slug != "" && strings.EqualFold(base, site.Slug)) || (site.Name != "" && strings.EqualFold(base, site.Name)) {
				mapped[site.ID] = dir
				taken[dir] = true
				break
			}
		}
	}

	var unmatched []api.Site
	for _, site := range sites {
		if _, done := mapped[site.ID]; !done {
			unmatched = append(unmatched, site)
		}
	}
	var remaining []string
	for _, dir := range dirs {
		if !taken[dir] {
			remaining = append(remaining, dir)
		}
	}
	return mapped, unmatched, remaining
}

// buildWorkspaceManifest assembles the file: one entry per mapped site, in the
// server's site order. The upload mode is written EXPLICITLY so the file shows
// which tree a deploy uploads instead of depending on a fallback that could
// change later. An app's own build settings are carried over so linking the
// workspace never silently drops config-as-code.
func buildWorkspaceManifest(project *api.Project, sites []api.Site, mapped map[string]string, existing map[string]perAppConfig, upload string) ProjectManifest {
	manifest := ProjectManifest{ProjectID: project.ID, Name: project.Name, Slug: project.Slug}
	for _, site := range sites {
		dir, ok := mapped[site.ID]
		if !ok {
			continue
		}
		entry := SiteEntry{
			SiteID:        site.ID,
			SiteName:      site.Name,
			SiteSlug:      site.Slug,
			RootDirectory: dir,
			Upload:        upload,
		}
		if cfg, found := existing[dir]; found {
			entry.Framework = cfg.Framework
			entry.DockerfilePath = cfg.DockerfilePath
			entry.BuildCommand = cfg.BuildCommand
			entry.InstallCommand = cfg.InstallCommand
			entry.StartCommand = cfg.StartCommand
			entry.OutputDirectory = cfg.OutputDirectory
			entry.Port = cfg.Port
			entry.Crons = cfg.Crons
		}
		manifest.Sites = append(manifest.Sites, entry)
	}
	return manifest
}

// manifestSummaryLines renders what was written: site → directory → upload mode.
func manifestSummaryLines(manifest ProjectManifest) []string {
	lines := make([]string, 0, len(manifest.Sites)+1)
	lines = append(lines, "   site → directory (upload)")
	for _, entry := range manifest.Sites {
		lines = append(lines, fmt.Sprintf("   %s → %s (%s)", siteLabel(entry), entry.RootDirectory, entry.Upload))
	}
	return lines
}
