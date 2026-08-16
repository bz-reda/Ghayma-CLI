package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/manifoldco/promptui"
)

// Workspace manifest (2026-08-16). A Ghayma project can hold several sites, and
// in a monorepo each app subdirectory is one site. Until now the CLI only knew
// "one directory ↔ one site": init/link wrote a per-app .ghayma.json inside
// apps/<x>/, so linking a whole workspace meant walking every app by hand and
// deploying from the root only worked for turbo repos. The manifest is a
// root-level .ghayma.json that lists every site and the directory that backs it,
// so `deploy` at the root can ask which site to ship.
//
// Both files share the .ghayma.json name; isManifest tells them apart by shape.

// Upload modes for a site entry: what gets tarred and uploaded.
//   - uploadModeApp:       tar the app directory; root_directory is not sent.
//   - uploadModeWorkspace: tar the workspace root and build root_directory.
const (
	uploadModeApp       = "app"
	uploadModeWorkspace = "workspace"
)

// SiteEntry is one site's config. It deliberately reuses the json keys of
// today's per-app config so an existing per-app .ghayma.json unmarshals into it
// (embedded in perAppConfig) unchanged — `upload` is the only manifest-only key.
type SiteEntry struct {
	SiteID   string `json:"site_id,omitempty"`
	SiteName string `json:"site_name,omitempty"`
	SiteSlug string `json:"site_slug,omitempty"`
	// RootDirectory is the app directory relative to the workspace root, always
	// forward-slash: the platform joins it onto a POSIX source path server-side.
	RootDirectory string `json:"root_directory,omitempty"`
	// Upload is manifest-only ("app" | "workspace"); empty falls back to
	// defaultUploadMode for hand-written manifests.
	Upload          string          `json:"upload,omitempty"`
	Framework       string          `json:"framework,omitempty"`
	DockerfilePath  string          `json:"dockerfile_path,omitempty"`
	BuildCommand    string          `json:"build_command,omitempty"`
	InstallCommand  string          `json:"install_command,omitempty"`
	StartCommand    string          `json:"start_command,omitempty"`
	OutputDirectory string          `json:"output_directory,omitempty"`
	Port            int             `json:"port,omitempty"`
	Crons           json.RawMessage `json:"crons,omitempty"`
}

// ProjectManifest is the root-level file: one project, every site it owns.
type ProjectManifest struct {
	ProjectID string      `json:"project_id"`
	Name      string      `json:"name"`
	Slug      string      `json:"slug"`
	Sites     []SiteEntry `json:"sites"`
}

// perAppConfig reads today's per-app config. The embedded SiteEntry flattens,
// so the existing keys (site_id, root_directory, build_command, crons, …) land
// where they always did.
type perAppConfig struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	SiteEntry
}

// SiteContext is one resolved deploy target: which site, what to upload, and
// what the server should build inside it.
type SiteContext struct {
	ProjectID   string
	ProjectName string
	ProjectSlug string
	Site        SiteEntry
	// SourceDir is the directory tarred and uploaded.
	SourceDir string
	// RootDirectory is the build target inside the upload (forward-slash),
	// empty when SourceDir is itself the app directory.
	RootDirectory string
	ConfigPath    string
	FromManifest  bool
	// RootFromConfig records that RootDirectory came from the config file
	// rather than the filesystem layout, purely so deploy keeps printing its
	// existing "(from config)" headline.
	RootFromConfig bool
	WorkspaceRoot  string
}

// errNoProjectConfig means nothing here or above describes a site — the signal
// that keeps deploy's legacy turbo scan-and-pick fallback reachable.
var errNoProjectConfig = errors.New("no project config found")

// promptManifestSiteFn / stdinIsTerminalFn are indirected so tests can drive
// both the interactive and non-interactive branches without a TTY.
var (
	promptManifestSiteFn = promptManifestSite
	stdinIsTerminalFn    = stdinIsTerminal
)

// isManifest reports whether a project config is a workspace manifest rather
// than a per-app config. Both use the .ghayma.json name, so the shape decides:
// a `sites` key holding an ARRAY. Anything else (absent, null, object, string)
// is a per-app config and must keep its existing semantics.
func isManifest(data []byte) bool {
	var probe struct {
		Sites json.RawMessage `json:"sites"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}
	trimmed := strings.TrimSpace(string(probe.Sites))
	return strings.HasPrefix(trimmed, "[")
}

// findWorkspaceRoot walks up from dir looking for a JS workspace root:
// turbo.json, pnpm-workspace.yaml, or a package.json declaring `workspaces`.
//
// Deliberately distinct from findMonorepoRoot, which stays turbo-only: that one
// decides the UPLOAD MODE for per-app configs, and widening it would silently
// switch pnpm-only repos from "upload the app dir" to "upload the whole
// workspace", changing live builds for projects nobody touched (2026-08-16).
// This one only decides where a manifest may live and which dirs to offer.
func findWorkspaceRoot(dir string) string {
	current := dir
	for {
		if isWorkspaceRoot(current) {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

func isWorkspaceRoot(dir string) bool {
	for _, marker := range []string{"turbo.json", "pnpm-workspace.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	_, declared := packageJSONWorkspaces(dir)
	return declared
}

// packageJSONWorkspaces returns the globs from a root package.json `workspaces`
// key (array form, or the object form with `packages`), plus whether the key
// exists at all — a repo can declare workspaces with a shape we don't parse and
// still be a workspace root.
func packageJSONWorkspaces(dir string) ([]string, bool) {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil, false
	}
	var probe struct {
		Workspaces json.RawMessage `json:"workspaces"`
	}
	if err := json.Unmarshal(data, &probe); err != nil || len(probe.Workspaces) == 0 {
		return nil, false
	}

	var globs []string
	if err := json.Unmarshal(probe.Workspaces, &globs); err == nil {
		return globs, true
	}
	var object struct {
		Packages []string `json:"packages"`
	}
	if err := json.Unmarshal(probe.Workspaces, &object); err == nil {
		return object.Packages, true
	}
	return nil, true
}

// workspaceGlobs resolves the workspace member globs, pnpm file first, then
// package.json, then the conventional layout — a turbo repo often declares its
// members only in package.json, and some declare them nowhere useful.
func workspaceGlobs(root string) []string {
	if data, err := os.ReadFile(filepath.Join(root, "pnpm-workspace.yaml")); err == nil {
		if globs := parsePnpmPackages(data); len(globs) > 0 {
			return globs
		}
	}
	if globs, _ := packageJSONWorkspaces(root); len(globs) > 0 {
		return globs
	}
	return []string{"apps/*", "packages/*"}
}

// parsePnpmPackages pulls the `packages:` list out of a pnpm-workspace.yaml
// without taking on a yaml dependency. The real files carry comments and other
// top-level keys (patchedDependencies, allowUnusedPatches, …), so blank and
// commented lines are skipped and the first other top-level key ends the list.
func parsePnpmPackages(data []byte) []string {
	var globs []string
	inPackages := false
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !inPackages {
			if line == "packages:" {
				inPackages = true
			}
			continue
		}
		if !strings.HasPrefix(line, "- ") {
			break // a sibling key ends the list
		}
		if glob := yamlScalar(line[2:]); glob != "" {
			globs = append(globs, glob)
		}
	}
	return globs
}

// yamlScalar trims an inline comment and surrounding quotes off a list item.
func yamlScalar(s string) string {
	if i := strings.Index(s, " #"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	return strings.Trim(s, `"'`)
}

// workspaceAppDirs lists the candidate app directories of a workspace: expand
// the member globs, keep only real packages (a directory holding a
// package.json), and return sorted forward-slash paths relative to root.
func workspaceAppDirs(root string) []string {
	var dirs []string
	seen := map[string]bool{}

	for _, glob := range workspaceGlobs(root) {
		if strings.HasPrefix(glob, "!") { // pnpm exclusion
			continue
		}
		matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(glob)))
		if err != nil {
			continue
		}
		for _, match := range matches {
			if info, err := os.Stat(match); err != nil || !info.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(match, "package.json")); err != nil {
				continue
			}
			rel := monorepoRelDir(root, match)
			if rel == "" || rel == "." || seen[rel] {
				continue
			}
			seen[rel] = true
			dirs = append(dirs, rel)
		}
	}

	sort.Strings(dirs)
	return dirs
}

// defaultUploadMode is the mode written for a freshly linked workspace, chosen
// to preserve exactly what each repo kind does today (2026-08-16): a turbo repo
// already ships the whole workspace (findMonorepoRoot walks up to turbo.json),
// while a pnpm-only repo ships just the app directory. The manifest must not be
// the thing that changes a live build.
func defaultUploadMode(root string) string {
	if _, err := os.Stat(filepath.Join(root, "turbo.json")); err == nil {
		return uploadModeWorkspace
	}
	return uploadModeApp
}

// stdinIsTerminal reports whether stdin is a terminal, so the resolver knows if
// it may ask a question. No new dependency: a pipe or a file is not a character
// device, so it cannot be a terminal.
//
// /dev/null needs its own answer: it IS a character device, and it is what CI
// and `ghayma deploy < /dev/null` hand the process. Treating it as a terminal
// turns the actionable "pass --site <slug>" error into a picker that reads EOF
// and aborts with "Cancelled" (2026-08-16).
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	if devNull, err := os.Stat(os.DevNull); err == nil && os.SameFile(fi, devNull) {
		return false
	}
	return true
}

// resolveSiteContext is the single "which site am I acting on?" resolver, used
// by deploy (verb "deploy"). Resolution order:
//
//  1. a per-app config in cwd — the directory pins the site, exactly as today;
//  2. a manifest in cwd — --site, or the only site, or ask (TTY), never guess;
//  3. no config in cwd — the nearest ancestor manifest, matched on the
//     directory we're standing in;
//  4. nothing — errNoProjectConfig, so callers can keep their own fallbacks.
func resolveSiteContext(cwd, siteFlag, verb string) (*SiteContext, error) {
	if abs, err := filepath.Abs(cwd); err == nil {
		cwd = abs
	}

	if path, err := findProjectConfig(cwd); err == nil {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("could not read %s: %w", path, err)
		}
		if !isManifest(data) {
			return perAppSiteContext(cwd, path, data, siteFlag, verb)
		}
		manifest, err := parseManifest(data, path)
		if err != nil {
			return nil, err
		}
		entry, err := selectManifestSite(manifest, siteFlag, verb)
		if err != nil {
			return nil, err
		}
		return manifestSiteContext(cwd, path, manifest, *entry)
	}

	root, path, manifest, err := findAncestorManifest(cwd)
	if err != nil {
		return nil, err
	}
	entry := nearestListedEntry(manifest.Sites, monorepoRelDir(root, cwd))
	if entry == nil {
		return nil, fmt.Errorf("this directory is not listed in %s — run 'ghayma link' from the workspace root to add it", path)
	}
	if err := checkSiteFlag(*entry, siteFlag, verb); err != nil {
		return nil, err
	}
	return manifestSiteContext(root, path, manifest, *entry)
}

// nearestListedEntry finds the site that owns the directory we're standing in:
// the exact directory first, then each parent up to (but not including) the
// workspace root. People work from apps/web/src, not apps/web, and matching
// only the exact directory made every command there fail (2026-08-16). The
// deepest match wins, so a nested app still beats the app that contains it.
//
// An entry with no root_directory is skipped: it describes the workspace root
// itself and would otherwise swallow every unlisted directory in the repo.
func nearestListedEntry(sites []SiteEntry, rel string) *SiteEntry {
	for candidate := normalizeRelDir(rel); candidate != ""; candidate = normalizeRelDir(path.Dir(candidate)) {
		for i := range sites {
			if sites[i].RootDirectory != "" && sites[i].RootDirectory == candidate {
				return &sites[i]
			}
		}
	}
	return nil
}

// perAppSiteContext keeps today's per-app semantics byte for byte: an explicit
// root_directory points at a subdir from where the config lives; otherwise a
// turbo ancestor makes the workspace the upload root; otherwise the app
// directory is uploaded on its own.
func perAppSiteContext(cwd, configPath string, data []byte, siteFlag, verb string) (*SiteContext, error) {
	var cfg perAppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", configPath, err)
	}
	if err := checkSiteFlag(cfg.SiteEntry, siteFlag, verb); err != nil {
		return nil, err
	}

	ctx := &SiteContext{
		ProjectID:   cfg.ProjectID,
		ProjectName: cfg.Name,
		ProjectSlug: cfg.Slug,
		Site:        cfg.SiteEntry,
		SourceDir:   cwd,
		ConfigPath:  configPath,
	}
	if cfg.RootDirectory != "" {
		ctx.RootDirectory = filepath.ToSlash(cfg.RootDirectory)
		ctx.RootFromConfig = true
		return ctx, nil
	}
	if root := findMonorepoRoot(cwd); root != "" && root != cwd {
		ctx.SourceDir = root
		ctx.RootDirectory = monorepoRelDir(root, cwd)
	}
	return ctx, nil
}

// manifestSiteContext turns a manifest entry into an upload plan.
func manifestSiteContext(root, configPath string, manifest *ProjectManifest, entry SiteEntry) (*SiteContext, error) {
	ctx := &SiteContext{
		ProjectID:     manifest.ProjectID,
		ProjectName:   manifest.Name,
		ProjectSlug:   manifest.Slug,
		Site:          entry,
		ConfigPath:    configPath,
		FromManifest:  true,
		WorkspaceRoot: root,
	}

	mode := entry.Upload
	if mode == "" {
		mode = defaultUploadMode(root)
	}
	switch mode {
	case uploadModeWorkspace:
		ctx.SourceDir = root
		ctx.RootDirectory = entry.RootDirectory
	case uploadModeApp:
		appDir := root
		if entry.RootDirectory != "" {
			appDir = filepath.Join(root, filepath.FromSlash(entry.RootDirectory))
		}
		if info, err := os.Stat(appDir); err != nil || !info.IsDir() {
			return nil, fmt.Errorf("site %q points at %q, which is not a directory in this workspace", siteLabel(entry), entry.RootDirectory)
		}
		ctx.SourceDir = appDir
	default:
		// Fail loudly rather than pick a mode: the two modes upload different
		// trees, and guessing wrong ships the wrong build.
		return nil, fmt.Errorf("site %q has an unknown upload mode %q in %s (expected %q or %q)",
			siteLabel(entry), entry.Upload, configPath, uploadModeApp, uploadModeWorkspace)
	}
	return ctx, nil
}

// selectManifestSite picks the site to act on: --site wins, a lone site needs
// no question, several sites are asked about on a TTY and refused without one.
func selectManifestSite(manifest *ProjectManifest, siteFlag, verb string) (*SiteEntry, error) {
	if len(manifest.Sites) == 0 {
		return nil, fmt.Errorf("no sites listed in %s — run 'ghayma link' from the workspace root to rebuild it", projectConfigName)
	}

	if siteFlag != "" {
		if entry := matchSiteEntry(manifest.Sites, siteFlag); entry != nil {
			return entry, nil
		}
		return nil, fmt.Errorf("unknown site %q in this workspace (available: %s)", siteFlag, strings.Join(siteLabels(manifest.Sites), ", "))
	}

	if len(manifest.Sites) == 1 {
		return &manifest.Sites[0], nil
	}

	if !stdinIsTerminalFn() {
		return nil, fmt.Errorf("several sites in this workspace — pass --site <slug> (available: %s)", strings.Join(siteLabels(manifest.Sites), ", "))
	}

	idx, err := promptManifestSiteFn(verb, manifest.Sites)
	if err != nil {
		return nil, errAttachCancelled
	}
	if idx < 0 || idx >= len(manifest.Sites) {
		return nil, fmt.Errorf("invalid site selection")
	}
	return &manifest.Sites[idx], nil
}

// findAncestorManifest walks up from dir's parent for the nearest workspace
// manifest. A per-app config on the way up describes its own directory, not
// ours, so it is skipped.
func findAncestorManifest(dir string) (string, string, *ProjectManifest, error) {
	current := filepath.Dir(dir)
	for {
		if path, err := findProjectConfig(current); err == nil {
			if data, readErr := os.ReadFile(path); readErr == nil && isManifest(data) {
				manifest, parseErr := parseManifest(data, path)
				if parseErr != nil {
					return "", "", nil, parseErr
				}
				return current, path, manifest, nil
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", "", nil, errNoProjectConfig
		}
		current = parent
	}
}

// parseManifest decodes a manifest and normalizes every root_directory to a
// clean forward-slash path, so entries hand-written as "./apps/web/" or with
// Windows separators still match a directory and still reach the Linux builder
// as a POSIX path.
func parseManifest(data []byte, path string) (*ProjectManifest, error) {
	var manifest ProjectManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("%s is not a valid workspace manifest: %w", path, err)
	}
	for i := range manifest.Sites {
		manifest.Sites[i].RootDirectory = normalizeRelDir(manifest.Sites[i].RootDirectory)
	}
	return &manifest, nil
}

// normalizeRelDir cleans a relative directory into forward-slash form ("." for
// the root itself becomes empty).
func normalizeRelDir(dir string) string {
	if dir == "" {
		return ""
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(dir)))
	if clean == "." {
		return ""
	}
	return clean
}

// matchSiteEntry resolves a --site value against slug, then name, then id.
func matchSiteEntry(sites []SiteEntry, want string) *SiteEntry {
	keys := []func(SiteEntry) string{
		func(s SiteEntry) string { return s.SiteSlug },
		func(s SiteEntry) string { return s.SiteName },
		func(s SiteEntry) string { return s.SiteID },
	}
	for _, key := range keys {
		for i := range sites {
			if value := key(sites[i]); value != "" && strings.EqualFold(value, want) {
				return &sites[i]
			}
		}
	}
	return nil
}

// checkSiteFlag guards the directory-pinned paths (a per-app config, or a
// manifest entry matched by the directory we're in): a --site naming a
// different site must never silently act on this one. The verb keeps the advice
// true for every caller — this used to say "deploy another site" to someone
// running `ghayma env list`.
func checkSiteFlag(entry SiteEntry, siteFlag, verb string) error {
	if siteFlag == "" || matchSiteEntry([]SiteEntry{entry}, siteFlag) != nil {
		return nil
	}
	return fmt.Errorf("this directory is linked to site %q; run from the workspace root (or without --site) to %s another site", siteLabel(entry), verb)
}

// siteLabel is how a site is named back to the user: slug, else name, else id.
func siteLabel(entry SiteEntry) string {
	for _, candidate := range []string{entry.SiteSlug, entry.SiteName, entry.SiteID} {
		if candidate != "" {
			return candidate
		}
	}
	return "(unnamed)"
}

func siteLabels(sites []SiteEntry) []string {
	labels := make([]string, len(sites))
	for i, s := range sites {
		labels[i] = siteLabel(s)
	}
	return labels
}

// manifestSiteChoiceLabels renders the site picker: the slug, then the
// directory it builds, so a monorepo with lookalike slugs is unambiguous.
func manifestSiteChoiceLabels(sites []SiteEntry) []string {
	labels := make([]string, len(sites))
	for i, s := range sites {
		dir := s.RootDirectory
		if dir == "" {
			dir = "."
		}
		labels[i] = fmt.Sprintf("%s  (%s)", siteLabel(s), dir)
	}
	return labels
}

// promptManifestSite asks which site to act on. A cancel surfaces the promptui
// error, which selectManifestSite turns into errAttachCancelled.
func promptManifestSite(verb string, sites []SiteEntry) (int, error) {
	sel := promptui.Select{
		Label: fmt.Sprintf("Which site do you want to %s?", verb),
		Items: manifestSiteChoiceLabels(sites),
		Size:  10,
	}
	idx, _, err := sel.Run()
	if err != nil {
		return 0, err
	}
	return idx, nil
}

// manifestHasNoActiveSite is `site use`'s permanent answer at a workspace root.
// Unlike the other site-scoped commands, `site use` has nothing to resolve: it
// PINS one site into a config file, and a manifest's whole point is that the
// root has no single active site. Say so, and point at the two ways to choose
// one (2026-08-16).
func manifestHasNoActiveSite(data []byte) error {
	if !isManifest(data) {
		return nil
	}
	return fmt.Errorf("./%s is a workspace manifest — there is no single active site here: every command takes --site or asks; run 'ghayma site use' inside the app's directory to pin its site", projectConfigName)
}
