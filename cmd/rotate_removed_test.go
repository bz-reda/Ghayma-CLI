package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// No shared credentials, part 4: a database's own login and a bucket's own key
// are never handed out, so rotating them is not a user command any more. The
// old commands stay as hidden stubs that point at per-connection rotation and
// make no request.

func TestDBRotate_RemovedPointsToConnectionsRotate(t *testing.T) {
	ts, calls := detailsStub(t, "/api/v1/databases", `{"databases":[`+shopDB+`]}`, `{"connections":[]}`)
	cliHome(t, ts.URL)
	forceStdin(t, true)

	out := runCLI(t, linkedDir(t), "db", "rotate", "shop")

	want := "❌ 'ghayma db rotate' was removed: a database's own login is never handed out. To give one app a new credential: ghayma connections rotate database shop --site <slug>\n"
	if out != want || lastExitCode != 1 {
		t.Fatalf("exit=%d output = %q; want %q", lastExitCode, out, want)
	}
	if len(*calls) != 0 {
		t.Errorf("requests = %v; want none", *calls)
	}
}

func TestStorageRotate_RemovedPointsToConnectionsRotate(t *testing.T) {
	ts, calls := detailsStub(t, "/api/v1/storage", `{"buckets":[`+uploadsBucket+`]}`, `{"connections":[]}`)
	cliHome(t, ts.URL)
	forceStdin(t, true)

	out := runCLI(t, linkedDir(t), "storage", "rotate", "uploads")

	want := "❌ 'ghayma storage rotate' was removed: a bucket's own key is never handed out. To give one app a new key: ghayma connections rotate bucket uploads --site <slug>\n"
	if out != want || lastExitCode != 1 {
		t.Fatalf("exit=%d output = %q; want %q", lastExitCode, out, want)
	}
	if len(*calls) != 0 {
		t.Errorf("requests = %v; want none", *calls)
	}
}

// Without a name the pointer keeps its placeholder.
func TestRotateRemoved_WithoutANameKeepsThePlaceholder(t *testing.T) {
	out := runCLI(t, t.TempDir(), "db", "rotate")
	if !strings.Contains(out, "ghayma connections rotate database <name> --site <slug>") || lastExitCode != 1 {
		t.Fatalf("exit=%d output = %q", lastExitCode, out)
	}
}

// helpText is what `<command> --help` prints, read without running the tree:
// a --help through runCLI would stay set on the command for later tests.
func helpText(t *testing.T, c *cobra.Command) string {
	t.Helper()
	var buf bytes.Buffer
	c.SetOut(&buf)
	defer c.SetOut(nil)
	if err := c.Help(); err != nil {
		t.Fatalf("help: %v", err)
	}
	return buf.String()
}

func TestRotateRemoved_HiddenFromHelp(t *testing.T) {
	for _, c := range []*cobra.Command{dbCmd, storageCmd} {
		out := helpText(t, c)
		if !strings.Contains(out, "credentials") {
			t.Fatalf("%s --help = %q; want the command listing", c.Name(), out)
		}
		if strings.Contains(out, "rotate") {
			t.Errorf("%s --help lists rotate:\n%s", c.Name(), out)
		}
	}
}
