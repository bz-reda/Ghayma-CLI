package cmd

import (
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

// TestDockerfileHint is the regression pin for the 2026-08-17 incident: deploy
// printed "🐳 Using your Dockerfile" whenever a Dockerfile existed, without
// knowing whether the project's "Use custom Dockerfile" toggle was on. The
// operator read that as a platform bug when the server had (correctly)
// generated its own Dockerfile. The hint must now reflect the actual flag, and
// say so honestly when it could not be fetched.
func TestDockerfileHint(t *testing.T) {
	cases := []struct {
		name         string
		onDisk       string // relative path to create, "" = create nothing
		explicitPath string
		flag         *bool
		want         []string
	}{
		{
			name: "no dockerfile, no explicit path — silent",
			want: nil,
		},
		{
			name:         "explicit path missing on disk — warn, never abort",
			explicitPath: "docker/Dockerfile.web",
			flag:         boolPtr(true),
			want: []string{
				`⚠️  dockerfile_path "docker/Dockerfile.web" not found under APPDIR — the path is relative to the app directory; the build will fail if "Use custom Dockerfile" is enabled.`,
			},
		},
		{
			name:         "explicit path missing, flag unknown — same warning",
			explicitPath: "Dockerfile.prod",
			want: []string{
				`⚠️  dockerfile_path "Dockerfile.prod" not found under APPDIR — the path is relative to the app directory; the build will fail if "Use custom Dockerfile" is enabled.`,
			},
		},
		{
			name:         "explicit path present, flag ON",
			onDisk:       "docker/Dockerfile.web",
			explicitPath: "docker/Dockerfile.web",
			flag:         boolPtr(true),
			want:         []string{"🐳 Using your Dockerfile at docker/Dockerfile.web (dockerfile_path)."},
		},
		{
			name:   "convention Dockerfile, flag ON",
			onDisk: "Dockerfile",
			flag:   boolPtr(true),
			want:   []string{"🐳 Using your Dockerfile (custom Dockerfile enabled for this project)."},
		},
		{
			name:   "convention Dockerfile, flag OFF — the incident case",
			onDisk: "Dockerfile",
			flag:   boolPtr(false),
			want: []string{
				`ℹ️  Dockerfile found (Dockerfile) but "Use custom Dockerfile" is OFF for this project — the platform will generate one. Enable it in the Dashboard → project → Settings → Advanced.`,
			},
		},
		{
			name:         "explicit path present, flag OFF",
			onDisk:       "docker/Dockerfile.web",
			explicitPath: "docker/Dockerfile.web",
			flag:         boolPtr(false),
			want: []string{
				`ℹ️  Dockerfile found (docker/Dockerfile.web) but "Use custom Dockerfile" is OFF for this project — the platform will generate one. Enable it in the Dashboard → project → Settings → Advanced.`,
			},
		},
		{
			name:   "convention Dockerfile, flag unknown",
			onDisk: "Dockerfile",
			want: []string{
				`ℹ️  Dockerfile found (Dockerfile) — it is used only when "Use custom Dockerfile" is enabled for this project (could not verify the setting).`,
			},
		},
		{
			name:         "explicit path present, flag unknown",
			onDisk:       "docker/Dockerfile.web",
			explicitPath: "docker/Dockerfile.web",
			want: []string{
				`ℹ️  Dockerfile found (docker/Dockerfile.web) — it is used only when "Use custom Dockerfile" is enabled for this project (could not verify the setting).`,
			},
		},
		{
			name:   "convention Dockerfile ignored when an explicit path is set",
			onDisk: "Dockerfile",
			// The server resolves dockerfile_path only; a stray root
			// Dockerfile must not be reported as the one being built.
			explicitPath: "docker/Dockerfile.web",
			flag:         boolPtr(true),
			want: []string{
				`⚠️  dockerfile_path "docker/Dockerfile.web" not found under APPDIR — the path is relative to the app directory; the build will fail if "Use custom Dockerfile" is enabled.`,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			appDir := t.TempDir()
			if tc.onDisk != "" {
				writeDockerfile(t, appDir, tc.onDisk)
			}

			got := dockerfileHint(appDir, tc.explicitPath, tc.flag)

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
// hint never claims the Dockerfile is in use without a confirmed flag, and it
// never names the pre-rebrand config file.
func TestDockerfileHint_HonestWording(t *testing.T) {
	appDir := t.TempDir()
	writeDockerfile(t, appDir, "Dockerfile")

	for _, flag := range []*bool{nil, boolPtr(false)} {
		for _, line := range dockerfileHint(appDir, "", flag) {
			if strings.Contains(line, "Using your Dockerfile") {
				t.Errorf("flag=%v claims the Dockerfile is in use: %q", flag, line)
			}
		}
	}
	for _, flag := range []*bool{nil, boolPtr(false), boolPtr(true)} {
		for _, line := range dockerfileHint(appDir, "", flag) {
			if strings.Contains(line, "espacetech") {
				t.Errorf("flag=%v names the legacy config file: %q", flag, line)
			}
		}
	}
}

// TestDeploy_DockerfileHintWiring pins the call site: deploy asks the API for
// the project's custom-Dockerfile flag instead of guessing, and the misleading
// pre-2026-08-17 strings are gone for good.
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
	for _, want := range []string{"GetProject(", "dockerfileHint("} {
		if !strings.Contains(src, want) {
			t.Errorf("deploy.go should reference %q", want)
		}
	}
}
