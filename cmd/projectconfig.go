package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// projectConfigName is the current project-config filename written by init/link.
const projectConfigName = ".ghayma.json"

// legacyProjectConfigName is the pre-rename filename. Still read for
// back-compat so existing customer projects keep working.
const legacyProjectConfigName = ".espacetech.json"

// findProjectConfig resolves the project-config path in dir, preferring the
// current .ghayma.json and falling back to the legacy .espacetech.json. When
// neither exists it returns the os.Stat error for the new name, which is
// os.IsNotExist-compatible.
func findProjectConfig(dir string) (string, error) {
	newPath := filepath.Join(dir, projectConfigName)
	if _, err := os.Stat(newPath); err == nil {
		return newPath, nil
	}

	legacyPath := filepath.Join(dir, legacyProjectConfigName)
	if _, err := os.Stat(legacyPath); err == nil {
		return legacyPath, nil
	}

	_, err := os.Stat(newPath)
	return "", err
}

// findProjectConfigUp resolves the NEAREST project config walking up from dir,
// stopping at the filesystem root. Either shape ends the walk: a per-app config
// and a workspace manifest both carry project_id, which is all the
// project-scoped commands (db, storage, points, logs, domains, …) need.
//
// Deliberately separate from findProjectConfig, which keeps its exact-directory
// meaning: init/link's "already linked" checks, the write-back path of
// `site use`, and step 1 of resolveSiteContext all mean "a config for THIS
// directory", and walking up would make them claim a parent's project
// (2026-08-16).
func findProjectConfigUp(dir string) (string, error) {
	current, err := filepath.Abs(dir)
	if err != nil {
		current = dir
	}

	// Keep the first miss: it is the os.IsNotExist error for the directory the
	// user actually ran in, which is what callers report on.
	var firstErr error
	for {
		path, err := findProjectConfig(current)
		if err == nil {
			return path, nil
		}
		if firstErr == nil {
			firstErr = err
		}
		// A repository boundary ends the walk: a config above the repo belongs
		// to some other project, and silently acting on it from a subdirectory
		// would be far worse than "no project config found".
		if _, gitErr := os.Stat(filepath.Join(current, ".git")); gitErr == nil {
			return "", firstErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", firstErr
		}
		current = parent
	}
}

// readProjectConfigUp reads the nearest project config at or above dir.
func readProjectConfigUp(dir string) ([]byte, error) {
	path, err := findProjectConfigUp(dir)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// projectConfigWritePath returns the path new projects are written to —
// always the current .ghayma.json name.
func projectConfigWritePath(dir string) string {
	return filepath.Join(dir, projectConfigName)
}

// readProjectConfig resolves and reads the project config in dir, returning
// its raw bytes. The error is os.IsNotExist-compatible when no config exists.
func readProjectConfig(dir string) ([]byte, error) {
	path, err := findProjectConfig(dir)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// writeProjectConfigUpdate writes an update back to the EXISTING project config
// in dir (update-in-place). It resolves the current path via findProjectConfig
// so a legacy .espacetech.json project stays on that file instead of silently
// migrating to .ghayma.json. Use projectConfigWritePath (not this) for brand-new
// configs created by init/link.
//
// cfg is untyped so callers round-trip the FULL shape they read (perAppConfig)
// instead of a narrower struct: marshalling a subset here is exactly how
// `site use` used to delete dockerfile_path and crons (2026-08-16).
func writeProjectConfigUpdate(dir string, cfg any) error {
	path, err := findProjectConfig(dir)
	if err != nil {
		return err
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0644)
}
