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
func writeProjectConfigUpdate(dir string, cfg ProjectConfig) error {
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
