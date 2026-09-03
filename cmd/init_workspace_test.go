package cmd

import (
	"strings"
	"testing"
)

// Workspace detection for init/link (2026-09-03). A plain `create-next-app` +
// pnpm 10 project carries a settings-only pnpm-workspace.yaml, and the CLI read
// its mere existence as "monorepo root": init announced one, demanded an app
// subdirectory, and hard-exited on a wrong answer — while "app", the Next.js
// router folder, was accepted and got the config written inside it.

// forceAppSubdirPrompt substitutes the app-subdirectory prompt and records the
// defaults it was offered, so the re-prompt loop is drivable without a TTY.
func forceAppSubdirPrompt(t *testing.T, fn func(def string) (string, error)) *[]string {
	t.Helper()
	orig := promptAppSubdirFn
	t.Cleanup(func() { promptAppSubdirFn = orig })

	var defaults []string
	promptAppSubdirFn = func(def string) (string, error) {
		defaults = append(defaults, def)
		return fn(def)
	}
	return &defaults
}

// TestValidateAppSubdir: an answer only counts when it is a real directory that
// holds a package.json — the two ways the operator's answers went wrong.
func TestValidateAppSubdir(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"app/page.tsx":          "export default function Page() {}\n",
		"apps/web/package.json": `{"name":"web"}`,
	})

	cases := []struct {
		name    string
		answer  string
		wantErr string
	}{
		{"missing directory", "apps/admin", "apps/admin does not exist here"},
		{"router folder, no package.json", "app", "app has no package.json"},
		{"real app directory", "apps/web", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAppSubdir(root, tc.answer)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateAppSubdir(%q) = %v; want nil", tc.answer, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateAppSubdir(%q) = nil; want %q", tc.answer, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("validateAppSubdir(%q) = %q; want it to contain %q", tc.answer, err, tc.wantErr)
			}
		})
	}
}

// TestAppSubdirForWorkspace_Reprompts: a wrong answer is asked again, not
// fatal. The operator typed the suggested default (which did not exist) and the
// CLI killed the process instead of letting them correct it.
func TestAppSubdirForWorkspace_Reprompts(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"pnpm-workspace.yaml": realPnpmWorkspaceYAML,
		"package.json":        `{"name":"workspace"}`,
		"app/page.tsx":        "export default function Page() {}\n",
	})

	answers := []string{"apps/web", "app", "apps/web"}
	call := 0
	defaults := forceAppSubdirPrompt(t, func(string) (string, error) {
		answer := answers[call]
		call++
		if call == len(answers) { // the user creates the app before answering again
			writeFiles(t, root, map[string]string{"apps/web/package.json": `{"name":"web"}`})
		}
		return answer, nil
	})

	dir, ok := appSubdirForWorkspace(root)
	if !ok {
		t.Fatal("appSubdirForWorkspace should let the command continue after a corrected answer")
	}
	if dir != "apps/web" {
		t.Errorf("appSubdirForWorkspace = %q; want %q", dir, "apps/web")
	}
	if call != 3 {
		t.Errorf("prompt ran %d times; want 3 (two bad answers re-asked)", call)
	}
	if len(*defaults) > 0 && (*defaults)[0] != "apps/web" {
		t.Errorf("first default = %q; want %q when the workspace lists no app dirs yet", (*defaults)[0], "apps/web")
	}
}

// TestAppSubdirForWorkspace_GivesUpCleanly: three bad answers stop the command
// with an answer, not with os.Exit.
func TestAppSubdirForWorkspace_GivesUpCleanly(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"pnpm-workspace.yaml": realPnpmWorkspaceYAML,
		"package.json":        `{"name":"workspace"}`,
		"app/page.tsx":        "export default function Page() {}\n",
	})

	call := 0
	forceAppSubdirPrompt(t, func(string) (string, error) {
		call++
		return "app", nil
	})

	dir, ok := appSubdirForWorkspace(root)
	if dir != "" {
		t.Errorf("appSubdirForWorkspace = %q; want %q after three bad answers", dir, "")
	}
	if ok {
		t.Error("appSubdirForWorkspace should stop the command rather than initialise at the root")
	}
	if call != 3 {
		t.Errorf("prompt ran %d times; want 3 attempts and no more", call)
	}
}

// TestAppSubdirForWorkspace_SettingsOnlyPnpm is the reported bug: a single
// Next.js app whose pnpm-workspace.yaml only carries settings must never be
// asked for an app subdirectory.
func TestAppSubdirForWorkspace_SettingsOnlyPnpm(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"pnpm-workspace.yaml": settingsOnlyPnpmWorkspaceYAML,
		"package.json":        `{"name":"my-app"}`,
		"app/page.tsx":        "export default function Page() {}\n",
	})

	forceAppSubdirPrompt(t, func(string) (string, error) {
		t.Fatal("a settings-only pnpm-workspace.yaml must not trigger the monorepo prompt")
		return "", nil
	})

	dir, ok := appSubdirForWorkspace(root)
	if dir != "" || !ok {
		t.Errorf("appSubdirForWorkspace = (%q, %v); want (\"\", true) — init here, no question asked", dir, ok)
	}
}

// TestAppSubdirForWorkspace_DefaultsToAnAppDir: the suggested answer comes from
// the workspace itself when it has app directories, so the default is typeable.
func TestAppSubdirForWorkspace_DefaultsToAnAppDir(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"pnpm-workspace.yaml":       realPnpmWorkspaceYAML,
		"package.json":              `{"name":"workspace"}`,
		"apps/planify/package.json": `{"name":"planify"}`,
	})

	defaults := forceAppSubdirPrompt(t, func(def string) (string, error) { return def, nil })

	dir, ok := appSubdirForWorkspace(root)
	if !ok || dir != "apps/planify" {
		t.Errorf("appSubdirForWorkspace = (%q, %v); want (%q, true)", dir, ok, "apps/planify")
	}
	if len(*defaults) != 1 || (*defaults)[0] != "apps/planify" {
		t.Errorf("prompt defaults = %v; want one offer of %q", *defaults, "apps/planify")
	}
}

// funcBody slices one function out of a source file, so a pin can talk about
// what a specific function does rather than the whole file.
func funcBody(t *testing.T, src, header string) string {
	t.Helper()
	start := strings.Index(src, header)
	if start < 0 {
		t.Fatalf("%q not found in source", header)
	}
	rest := src[start+len(header):]
	if end := strings.Index(rest, "\nfunc "); end >= 0 {
		return rest[:end]
	}
	return rest
}

// TestWorkspaceDetection_OnePredicate pins the two halves of the fix: init no
// longer keeps its own stat check (or kills the process), and the shared
// predicate demands a declared packages list rather than a filename.
func TestWorkspaceDetection_OnePredicate(t *testing.T) {
	initSrc := readCmdSource(t, "init.go")
	if strings.Contains(initSrc, "os.Exit(") {
		t.Error("init.go must not exit the process on a bad app-subdirectory answer; re-prompt instead")
	}
	if !strings.Contains(initSrc, "isWorkspaceRoot(") {
		t.Error("init.go should ask isWorkspaceRoot() rather than stat workspace files itself")
	}
	for _, own := range []string{`"turbo.json"`, `"pnpm-workspace.yaml"`} {
		if strings.Contains(initSrc, own) {
			t.Errorf("init.go still carries its own %s check; there must be one predicate", own)
		}
	}

	manifestSrc := readCmdSource(t, "manifest.go")
	if !strings.Contains(funcBody(t, manifestSrc, "func isWorkspaceRoot("), "workspaceMarker(") {
		t.Error("isWorkspaceRoot should delegate to workspaceMarker so both agree on the rule")
	}
	marker := funcBody(t, manifestSrc, "func workspaceMarker(")
	if !strings.Contains(marker, "parsePnpmPackages(") {
		t.Error("the predicate must read pnpm-workspace.yaml's packages list, not just stat the file")
	}
	for _, want := range []string{`"turbo.json"`, `"pnpm-workspace.yaml"`, `package.json "workspaces"`} {
		if !strings.Contains(marker, want) {
			t.Errorf("workspaceMarker should be able to name %s", want)
		}
	}
}

// TestLinkWorkspaceRoot_NoAppDirsMessage: the dead end the operator hit told
// them nothing. It must name the globs it consulted and the way out.
func TestLinkWorkspaceRoot_NoAppDirsMessage(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"turbo.json": `{}`, "package.json": `{"name":"solo"}`})

	err := noAppDirsError(root)
	if err == nil {
		t.Fatal("noAppDirsError should describe the empty workspace")
	}
	for _, want := range []string{"apps/*", "packages/*", "package.json", "ghayma link", "ghayma init"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}

	pnpm := t.TempDir()
	writeFiles(t, pnpm, map[string]string{"pnpm-workspace.yaml": realPnpmWorkspaceYAML})
	if got := noAppDirsError(pnpm).Error(); !strings.Contains(got, "apps/*") || !strings.Contains(got, "packages/*") {
		t.Errorf("error should print the workspace's own globs: %q", got)
	}

	src := readCmdSource(t, "link_manifest.go")
	if !strings.Contains(src, "noAppDirsError(root)") {
		t.Error("linkWorkspaceRoot should raise noAppDirsError so the message stays testable")
	}
}
