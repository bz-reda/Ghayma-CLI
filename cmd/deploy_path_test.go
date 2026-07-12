package cmd

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestMonorepoRelDir_ForwardSlash pins the 2026-07-12 Windows deploy fix: the
// monorepo root_directory sent to the build server must be a forward-slash
// POSIX path. filepath.Rel yields OS-native separators (backslashes on
// Windows), and the platform joins root_directory onto a POSIX source path
// server-side — so `apps\admin` becomes `/tmp/sources/<id>/apps\admin`, a
// literal filename that doesn't exist on Linux. Meaningful on the
// windows-latest CI runner (fails without ToSlash); trivially green elsewhere.
// Same class as the #7 tar-name fix.
func TestMonorepoRelDir_ForwardSlash(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, "apps", "admin")

	got := monorepoRelDir(root, appDir)

	if got != "apps/admin" {
		t.Errorf("monorepoRelDir = %q, want %q (must be forward-slash for the Linux builder)", got, "apps/admin")
	}
	if strings.ContainsRune(got, '\\') {
		t.Errorf("monorepoRelDir leaked a backslash separator: %q", got)
	}
}
