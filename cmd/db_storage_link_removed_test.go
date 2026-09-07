package cmd

import (
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// A managed database or storage bucket belongs to exactly one project for its
// whole life (decided 2026-09-07). Re-pointing one was never a safe operation:
// `unlink` cleared the resource's project and left it running outside billing
// and invisible in the dashboard — a real customer database ran that way for
// five weeks. The backend has dropped POST /databases/:id/link, /unlink and
// POST /storage/:id/link, /unlink, so a CLI that still offered them would only
// buy the user a 404. These tests pin the removal against reintroduction.

// subcommandNames returns every name AND alias registered under parent, so an
// alias can't smuggle a removed verb back onto the command tree.
func subcommandNames(parent *cobra.Command) []string {
	var names []string
	for _, sub := range parent.Commands() {
		names = append(names, sub.Name())
		names = append(names, sub.Aliases...)
	}
	return names
}

func TestDBCommand_NoLinkOrUnlink(t *testing.T) {
	for _, name := range subcommandNames(dbCmd) {
		if name == "link" || name == "unlink" {
			t.Errorf("`ghayma db %s` is still registered; the endpoint it calls no longer exists", name)
		}
	}
	// Positive pin: the sibling verbs that share the removal's blast radius
	// must survive — this is a targeted removal, not a pruning of `db`.
	for _, want := range []string{"list", "info", "create", "delete", "sites"} {
		if !containsName(subcommandNames(dbCmd), want) {
			t.Errorf("`ghayma db %s` went missing; only link/unlink were removed", want)
		}
	}
}

func TestStorageCommand_NoLinkOrUnlink(t *testing.T) {
	for _, name := range subcommandNames(storageCmd) {
		if name == "link" || name == "unlink" {
			t.Errorf("`ghayma storage %s` is still registered; the endpoint it calls no longer exists", name)
		}
	}
	for _, want := range []string{"list", "info", "create", "delete"} {
		if !containsName(subcommandNames(storageCmd), want) {
			t.Errorf("`ghayma storage %s` went missing; only link/unlink were removed", want)
		}
	}
}

// The `--project` flags existed only to pick a link target. A flag left behind
// on some other command would be accepted and silently ignored.
func TestDBAndStorage_NoLinkProjectFlags(t *testing.T) {
	for _, parent := range []*cobra.Command{dbCmd, storageCmd} {
		for _, sub := range parent.Commands() {
			if f := sub.Flags().Lookup("project"); f != nil {
				t.Errorf("`ghayma %s %s` still registers --project (%q); it only ever chose a link target",
					parent.Name(), sub.Name(), f.Usage)
			}
		}
	}
}

// No user-facing string may point at a command that no longer exists. `ghayma
// db sites` printed exactly such a hint when a database had no reachable sites.
func TestNoLinkCommandHints(t *testing.T) {
	dead := []string{"ghayma db link", "ghayma db unlink", "ghayma storage link", "ghayma storage unlink"}
	for _, name := range cmdSourceFiles(t) {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		src := string(data)
		for i, line := range strings.Split(src, "\n") {
			for _, hint := range dead {
				if strings.Contains(line, hint) {
					t.Errorf("%s:%d hints at the removed command %q: %s",
						name, i+1, hint, strings.TrimSpace(line))
				}
			}
		}
	}
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}
