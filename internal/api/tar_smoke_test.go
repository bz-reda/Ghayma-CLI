package api

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// tarEntryNames reads a gzip tarball and returns its entry names, sorted.
func tarEntryNames(t *testing.T, tarPath string) []string {
	t.Helper()
	f, err := os.Open(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()

	var names []string
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
	sort.Strings(names)
	return names
}

func TestCreateTarball_IgnoreRules(t *testing.T) {
	src := t.TempDir()
	write := func(path, content string) {
		if err := os.MkdirAll(strings.TrimSuffix(path[:strings.LastIndex(path, "/")], "/"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	mkd := func(p string) {
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatal(err)
		}
	}

	mkd(src + "/apps/web")
	mkd(src + "/apps/mobile/android/.gradle")
	mkd(src + "/apps/mobile/ios/Pods")
	mkd(src + "/node_modules/foo")
	mkd(src + "/.git/objects")
	mkd(src + "/packages/shared")

	write(src+"/package.json", "root")
	write(src+"/apps/web/index.js", "web")
	write(src+"/apps/web/keep.txt", "keep me")
	write(src+"/apps/web/discard.txt", "discard")
	write(src+"/apps/mobile/package.json", "mobile")
	write(src+"/apps/mobile/android/.gradle/cache.bin", "gradle")
	write(src+"/apps/mobile/ios/Pods/blob.bin", "ios")
	write(src+"/node_modules/foo/bar.js", "hardcoded excluded")
	write(src+"/.git/objects/deadbeef", "git")
	write(src+"/packages/shared/index.js", "shared")
	write(src+"/debug.log", "log file")

	write(src+"/.espacetechignore", `# mobile platform dirs
apps/mobile/android/
apps/mobile/ios/
# all logs
**/*.log
# exclude text files, but keep one via negation
*.txt
!apps/web/keep.txt
`)

	rules := LoadIgnoreRules(src)
	if rules.Source != ".espacetechignore" {
		t.Fatalf("expected .espacetechignore, got %q", rules.Source)
	}
	if len(rules.Patterns) != 5 {
		t.Fatalf("expected 5 patterns, got %d: %v", len(rules.Patterns), rules.Patterns)
	}

	tarPath := src + "/../out.tar.gz"
	if err := createTarball(src, tarPath, rules); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tarPath)

	names := tarEntryNames(t, tarPath)

	excluded := map[string]string{
		"apps/mobile/android":            "trailing-slash dir rule",
		"apps/mobile/android/.gradle":    "descendant of ignored dir",
		"apps/mobile/ios/Pods/blob.bin":  "descendant of ignored dir",
		"node_modules/foo/bar.js":        "baseline node_modules",
		".git/objects/deadbeef":          "baseline .git",
		"debug.log":                      "**/*.log glob",
		"apps/web/discard.txt":           "*.txt glob",
	}
	included := []string{
		"package.json",
		"apps/web/index.js",
		"apps/web/keep.txt", // negation re-include
		"apps/mobile/package.json",
		"packages/shared/index.js",
		".espacetechignore",
	}

	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}

	for bad, reason := range excluded {
		if set[bad] {
			t.Errorf("expected %q to be excluded (%s), but it was in the tarball", bad, reason)
		}
	}
	for _, good := range included {
		if !set[good] {
			t.Errorf("expected %q to be included, but it was missing. Tarball: %v", good, names)
		}
	}
}

// TestCreateTarball_ForwardSlashPaths guards the Windows deploy regression:
// filepath.Rel returns backslash paths on Windows, and writing those verbatim
// as tar entry names made nested files (e.g. src/lib/security/headers.ts) land
// as a single flat "src\lib\security\headers.ts" file on the Linux build host,
// so next.config.ts could no longer resolve its imports. Tar names must be
// POSIX forward-slash on every OS.
func TestCreateTarball_ForwardSlashPaths(t *testing.T) {
	src := t.TempDir()
	mkfile := func(rel, content string) {
		full := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	mkfile("next.config.ts", "root config")
	mkfile("src/lib/security/headers.ts", "headers")
	mkfile("src/i18n/request.ts", "i18n")

	tarPath := filepath.Join(t.TempDir(), "out.tar.gz")
	if err := createTarball(src, tarPath, &IgnoreRules{}); err != nil {
		t.Fatal(err)
	}

	names := tarEntryNames(t, tarPath)
	set := map[string]bool{}
	for _, n := range names {
		if strings.Contains(n, "\\") {
			t.Errorf("tar entry %q contains a backslash; names must be POSIX forward-slash", n)
		}
		set[n] = true
	}

	for _, want := range []string{
		"next.config.ts",
		"src/lib/security/headers.ts",
		"src/i18n/request.ts",
	} {
		if !set[want] {
			t.Errorf("expected %q in tarball, got %v", want, names)
		}
	}
}

func TestLoadIgnoreRules_DockerignoreFallback(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(src+"/.dockerignore", []byte("*.tmp\n"), 0644); err != nil {
		t.Fatal(err)
	}
	rules := LoadIgnoreRules(src)
	if rules.Source != ".dockerignore" {
		t.Fatalf("expected .dockerignore fallback, got %q", rules.Source)
	}
}

func TestLoadIgnoreRules_Prefers_Espacetechignore(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(src+"/.espacetechignore", []byte("foo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src+"/.dockerignore", []byte("bar\n"), 0644); err != nil {
		t.Fatal(err)
	}
	rules := LoadIgnoreRules(src)
	if rules.Source != ".espacetechignore" {
		t.Fatalf("expected .espacetechignore preferred, got %q", rules.Source)
	}
}

func TestLoadIgnoreRules_Prefers_Ghaymaignore(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(src+"/.ghaymaignore", []byte("new\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src+"/.espacetechignore", []byte("legacy\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src+"/.dockerignore", []byte("docker\n"), 0644); err != nil {
		t.Fatal(err)
	}
	rules := LoadIgnoreRules(src)
	if rules.Source != ".ghaymaignore" {
		t.Fatalf("expected .ghaymaignore preferred, got %q", rules.Source)
	}
}

// TestLoadIgnoreRules_BackCompatEspacetechignore is the ignore-file back-compat
// gate: a project with ONLY the legacy .espacetechignore must still load it.
func TestLoadIgnoreRules_BackCompatEspacetechignore(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(src+"/.espacetechignore", []byte("legacy-only\n"), 0644); err != nil {
		t.Fatal(err)
	}
	rules := LoadIgnoreRules(src)
	if rules.Source != ".espacetechignore" {
		t.Fatalf("back-compat broken: expected legacy .espacetechignore to load, got %q", rules.Source)
	}
	if len(rules.Patterns) != 1 || rules.Patterns[0] != "legacy-only" {
		t.Fatalf("expected legacy patterns to load, got %v", rules.Patterns)
	}
}
