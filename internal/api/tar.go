package api

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"

	gitignore "github.com/sabhiram/go-gitignore"
)

// BaselineIgnoreDirs are directory names always excluded from the upload,
// regardless of user ignore rules. OR'd with any rules from .ghaymaignore,
// the legacy .espacetechignore, or .dockerignore — user rules cannot
// re-include these.
var BaselineIgnoreDirs = []string{
	"node_modules", ".next", ".git", ".turbo", "dist",
}

var baselineSet = func() map[string]bool {
	m := make(map[string]bool, len(BaselineIgnoreDirs))
	for _, d := range BaselineIgnoreDirs {
		m[d] = true
	}
	return m
}()

// localEnvFile matches `.env*.local` names (.env.local,
// .env.production.local, …) — Next.js-convention local secret files.
// Plain `.env` is NOT matched: committed defaults are a legitimate
// build input.
func localEnvFile(name string) bool {
	return strings.HasPrefix(name, ".env") && strings.HasSuffix(name, ".local")
}

// IgnoreRules holds user-supplied ignore patterns loaded from the source dir.
type IgnoreRules struct {
	Source   string   // ".ghaymaignore", ".espacetechignore", ".dockerignore", or "" when none found
	Patterns []string // patterns as loaded, for display at deploy-time
	matcher  *gitignore.GitIgnore
}

// LoadIgnoreRules reads .ghaymaignore first, then the legacy .espacetechignore,
// then .dockerignore, from sourceDir. Returns empty rules (Source == "") when
// none exists.
func LoadIgnoreRules(sourceDir string) *IgnoreRules {
	for _, name := range []string{".ghaymaignore", ".espacetechignore", ".dockerignore"} {
		data, err := os.ReadFile(filepath.Join(sourceDir, name))
		if err != nil {
			continue
		}
		patterns := parseIgnoreLines(string(data))
		rules := &IgnoreRules{Source: name, Patterns: patterns}
		if len(patterns) > 0 {
			rules.matcher = gitignore.CompileIgnoreLines(patterns...)
		}
		return rules
	}
	return &IgnoreRules{}
}

func parseIgnoreLines(content string) []string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

// alwaysUploaded exempts a FILE from user ignore rules. Docker itself exempts
// the Dockerfile from .dockerignore (the daemon reads it out-of-band, so
// excluding it from the build context is standard practice) — but on Ghayma
// the upload IS the transport, so honoring that exclusion strips the very
// file a custom-Dockerfile build needs (2026-08-30 taarefni incident:
// `.dockerignore` listing `Dockerfile` made detection fail with "custom
// dockerfile_path not found"). Project manifests and the ignore files
// themselves ride along for the same reason. Baseline rules still win:
// a Dockerfile under node_modules never ships.
func alwaysUploaded(relPath string, isDir bool) bool {
	if isDir {
		return false
	}
	base := relPath
	if i := strings.LastIndex(relPath, "/"); i >= 0 {
		base = relPath[i+1:]
	}
	switch base {
	case "Dockerfile", ".ghayma.json", ".espacetech.json",
		".dockerignore", ".ghaymaignore", ".espacetechignore":
		return true
	}
	lower := strings.ToLower(base)
	return strings.HasPrefix(base, "Dockerfile.") || strings.HasSuffix(lower, ".dockerfile")
}

// matches returns true when the relative path is excluded by user rules.
// For directories, the path is also tried with a trailing slash so that
// gitignore dir-only patterns (e.g. "android/") match.
func (r *IgnoreRules) matches(relPath string, isDir bool) bool {
	if r == nil || r.matcher == nil {
		return false
	}
	if r.matcher.MatchesPath(relPath) {
		return true
	}
	if isDir && r.matcher.MatchesPath(relPath+"/") {
		return true
	}
	return false
}

func createTarball(sourceDir, tarPath string, rules *IgnoreRules) error {
	file, err := os.Create(tarPath)
	if err != nil {
		return err
	}
	defer file.Close()

	gw := gzip.NewWriter(file)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	return filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, _ := filepath.Rel(sourceDir, path)
		if relPath == "." {
			return nil
		}

		// Tar entries and gitignore matching are both POSIX forward-slash.
		// On Windows filepath.Rel yields backslash paths, so normalize once
		// here — otherwise nested files ship as flat "src\lib\x.ts" entries
		// that the Linux build host can't resolve back into directories.
		relPath = filepath.ToSlash(relPath)

		// Baseline: always skip these names at any depth. localEnvFile
		// rides along: `.env*.local` files are local-only secrets by
		// convention and must never reach the build context.
		for _, part := range strings.Split(relPath, "/") {
			if baselineSet[part] || localEnvFile(part) {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}

		// User rules: OR'd on top of the baseline — except the files a
		// build cannot live without (see alwaysUploaded).
		if !alwaysUploaded(relPath, info.IsDir()) && rules.matches(relPath, info.IsDir()) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = relPath

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		_, err = io.Copy(tw, f)
		return err
	})
}
