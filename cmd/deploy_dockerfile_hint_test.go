package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

// writeDockerfile drops a file at appDir/rel so a case can exercise the
// "present on disk" branches.
func writeDockerfile(t *testing.T, appDir, rel string) {
	t.Helper()
	path := filepath.Join(appDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

const (
	missingPathWarning = `⚠️  dockerfile_path "%s" not found under APPDIR — the path is relative to the app directory; the build will fail if this site builds with your Dockerfile.`
	autoNotice         = `ℹ️  Dockerfile found (%s) but this site builds with Auto-detect — the platform will generate the build. To build from your Dockerfile, set "builder": "dockerfile" in .ghayma.json or choose My Dockerfile in the site's Settings → Build settings.`
	unknownNotice      = `ℹ️  Dockerfile found (%s) — it is used only when this site builds with your Dockerfile (could not verify the setting).`
)

func withPath(format, path string) string { return strings.Replace(format, "%s", path, 1) }

// TestDockerfileHint is the regression pin for the 2026-08-17 incident: deploy
// printed "🐳 Using your Dockerfile" whenever a Dockerfile existed, whatever
// the server would do with it. The hint follows the site's builder, and says
// so honestly when it could not be read.
func TestDockerfileHint(t *testing.T) {
	cases := []struct {
		name         string
		onDisk       string // relative path to create, "" = create nothing
		explicitPath string
		builder      string
		want         []string
	}{
		{
			name: "no dockerfile, no explicit path, auto — silent",
			want: nil,
		},
		{
			name:    "no dockerfile, auto — silent",
			builder: builderAuto,
			want:    nil,
		},
		{
			name:    "no dockerfile, builder dockerfile — the server generates instead",
			builder: builderDockerfile,
			want:    []string{"ℹ️  No Dockerfile found under APPDIR — the platform will generate the build instead."},
		},
		{
			name:         "explicit path missing on disk — warn, never abort",
			explicitPath: "docker/Dockerfile.web",
			builder:      builderDockerfile,
			want:         []string{withPath(missingPathWarning, "docker/Dockerfile.web")},
		},
		{
			name:         "explicit path missing, builder unknown — same warning",
			explicitPath: "Dockerfile.prod",
			want:         []string{withPath(missingPathWarning, "Dockerfile.prod")},
		},
		{
			name:         "explicit path missing, auto — same warning",
			explicitPath: "Dockerfile.prod",
			builder:      builderAuto,
			want:         []string{withPath(missingPathWarning, "Dockerfile.prod")},
		},
		{
			name:         "explicit path present, builder dockerfile",
			onDisk:       "docker/Dockerfile.web",
			explicitPath: "docker/Dockerfile.web",
			builder:      builderDockerfile,
			want:         []string{"🐳 Building with your Dockerfile at docker/Dockerfile.web (dockerfile_path)."},
		},
		{
			name:    "convention Dockerfile, builder dockerfile",
			onDisk:  "Dockerfile",
			builder: builderDockerfile,
			want:    []string{"🐳 Building with your Dockerfile."},
		},
		{
			name:    "convention Dockerfile, auto — the incident case",
			onDisk:  "Dockerfile",
			builder: builderAuto,
			want:    []string{withPath(autoNotice, "Dockerfile")},
		},
		{
			name:         "explicit path present, auto",
			onDisk:       "docker/Dockerfile.web",
			explicitPath: "docker/Dockerfile.web",
			builder:      builderAuto,
			want:         []string{withPath(autoNotice, "docker/Dockerfile.web")},
		},
		{
			name:   "convention Dockerfile, builder unknown",
			onDisk: "Dockerfile",
			want:   []string{withPath(unknownNotice, "Dockerfile")},
		},
		{
			name:         "explicit path present, builder unknown",
			onDisk:       "docker/Dockerfile.web",
			explicitPath: "docker/Dockerfile.web",
			want:         []string{withPath(unknownNotice, "docker/Dockerfile.web")},
		},
		{
			name:   "convention Dockerfile ignored when an explicit path is set",
			onDisk: "Dockerfile",
			// The server resolves dockerfile_path only; a stray root
			// Dockerfile must not be reported as the one being built.
			explicitPath: "docker/Dockerfile.web",
			builder:      builderDockerfile,
			want:         []string{withPath(missingPathWarning, "docker/Dockerfile.web")},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			appDir := t.TempDir()
			if tc.onDisk != "" {
				writeDockerfile(t, appDir, tc.onDisk)
			}

			got := dockerfileHint(appDir, tc.explicitPath, tc.builder)

			want := make([]string, len(tc.want))
			for i, line := range tc.want {
				want[i] = strings.ReplaceAll(line, "APPDIR", appDir)
			}
			if len(got) != len(want) {
				t.Fatalf("got %d line(s) %q; want %d %q", len(got), got, len(want), want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("line %d:\n got  %q\n want %q", i, got[i], want[i])
				}
			}
		})
	}
}

// TestDockerfileHint_HonestWording pins the words the incident turned on: the
// hint never claims the Dockerfile is in use unless the site builds with it,
// and it never names the pre-rebrand config file or the retired project toggle.
func TestDockerfileHint_HonestWording(t *testing.T) {
	appDir := t.TempDir()
	writeDockerfile(t, appDir, "Dockerfile")

	for _, b := range []string{"", builderAuto} {
		for _, line := range dockerfileHint(appDir, "", b) {
			if strings.Contains(line, "Building with your Dockerfile") {
				t.Errorf("builder=%q claims the Dockerfile is in use: %q", b, line)
			}
		}
	}
	for _, b := range []string{"", builderAuto, builderDockerfile} {
		for _, line := range dockerfileHint(appDir, "", b) {
			for _, gone := range []string{"espacetech", "Use custom Dockerfile", "Settings → Advanced"} {
				if strings.Contains(line, gone) {
					t.Errorf("builder=%q names %q: %q", b, gone, line)
				}
			}
		}
	}
}

// TestDeployBuilder: the config's builder rides on this upload, so it wins;
// then the site's stored builder; only a backend that sends none leaves the
// call to the project's custom_dockerfile_enabled, which is read only then.
func TestDeployBuilder(t *testing.T) {
	flag := func(on bool, err error) (func() (bool, error), *int) {
		calls := 0
		return func() (bool, error) { calls++; return on, err }, &calls
	}
	cases := []struct {
		name          string
		config, site  string
		projectOn     bool
		projectErr    error
		want          string
		wantFlagReads int
	}{
		{"config dockerfile beats site auto", builderDockerfile, builderAuto, false, nil, builderDockerfile, 0},
		{"config auto beats site dockerfile", builderAuto, builderDockerfile, true, nil, builderAuto, 0},
		{"site dockerfile", "", builderDockerfile, false, nil, builderDockerfile, 0},
		{"site auto, project flag on", "", builderAuto, true, nil, builderAuto, 0},
		{"unknown site value reads as auto", "", "railpack", true, nil, builderAuto, 0},
		{"older backend, flag on", "", "", true, nil, builderDockerfile, 1},
		{"older backend, flag off", "", "", false, nil, builderAuto, 1},
		{"older backend, flag unreadable", "", "", false, errors.New("boom"), "", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			read, calls := flag(tc.projectOn, tc.projectErr)
			if got := deployBuilder(tc.config, tc.site, read); got != tc.want {
				t.Errorf("deployBuilder = %q; want %q", got, tc.want)
			}
			if *calls != tc.wantFlagReads {
				t.Errorf("project flag read %d time(s); want %d", *calls, tc.wantFlagReads)
			}
		})
	}
}

// TestConfigBuilder: "auto" and "dockerfile" pass (trimmed), empty leaves the
// choice to the site, anything else stops the deploy naming the file.
func TestConfigBuilder(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"auto", builderAuto},
		{"dockerfile", builderDockerfile},
		{" dockerfile ", builderDockerfile},
	} {
		ctx := &SiteContext{ConfigPath: "/w/.ghayma.json", Site: SiteEntry{Builder: tc.in}}
		got, err := configBuilder(ctx)
		if err != nil || got != tc.want {
			t.Errorf("configBuilder(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}

	for _, tc := range []struct {
		ctx  *SiteContext
		want string
	}{
		{
			&SiteContext{ConfigPath: "/w/.ghayma.json", FromManifest: true, Site: SiteEntry{SiteSlug: "web", Builder: "Dockerfile"}},
			`site "web" has an unknown builder "Dockerfile" in /w/.ghayma.json (expected "auto" or "dockerfile")`,
		},
		{
			&SiteContext{ConfigPath: "/w/.ghayma.json", Site: SiteEntry{SiteSlug: "web", Builder: "railpack"}},
			`unknown builder "railpack" in /w/.ghayma.json (expected "auto" or "dockerfile")`,
		},
	} {
		_, err := configBuilder(tc.ctx)
		if err == nil || err.Error() != tc.want {
			t.Errorf("configBuilder(%q) error = %v; want %q", tc.ctx.Site.Builder, err, tc.want)
		}
	}
}

// TestDeploy_DockerfileHintWiring pins the call site: the misleading
// pre-2026-08-17 strings are gone for good, and the project is read only
// through the fallback.
func TestDeploy_DockerfileHintWiring(t *testing.T) {
	src := readCmdSource(t, "deploy.go")
	for _, gone := range []string{
		".espacetech.json dockerfile_path",
		"must be enabled per-project in the dashboard",
		"printCustomDockerfileHint",
	} {
		if strings.Contains(src, gone) {
			t.Errorf("deploy.go still carries the misleading hint: %q", gone)
		}
	}
	for _, want := range []string{"GetProject(", "dockerfileHint(", "deployBuilder(", "configBuilder("} {
		if !strings.Contains(src, want) {
			t.Errorf("deploy.go should reference %q", want)
		}
	}
}
