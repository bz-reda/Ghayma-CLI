package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// buildCmdTree constructs `ghayma <group> <child>` just deep enough for
// the helper to walk parents when testing hint fallbacks.
func buildCmdTree(groupName, childName string) *cobra.Command {
	root := &cobra.Command{Use: "ghayma"}
	group := &cobra.Command{Use: groupName}
	child := &cobra.Command{Use: childName}
	root.AddCommand(group)
	group.AddCommand(child)
	return child
}

func TestRequireOneArg_MissingArg_MentionsListCmd(t *testing.T) {
	cmd := buildCmdTree("auth", "info")
	err := requireOneArg("name", "auth list")(cmd, []string{})
	if err == nil {
		t.Fatal("expected error for missing arg")
	}
	msg := err.Error()
	if !strings.Contains(msg, "ghayma auth info requires a name") {
		t.Errorf("missing descriptive prefix: %q", msg)
	}
	if !strings.Contains(msg, "Run 'ghayma auth list'") {
		t.Errorf("missing list-cmd hint: %q", msg)
	}
	if !cmd.SilenceUsage || !cmd.SilenceErrors {
		t.Error("expected SilenceUsage and SilenceErrors to be set")
	}
}

func TestRequireOneArg_MissingArg_FallsBackToGroupHelp(t *testing.T) {
	cmd := buildCmdTree("db", "create")
	err := requireOneArg("name", "")(cmd, []string{})
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Run 'ghayma db --help'") {
		t.Errorf("expected fallback to group --help, got: %q", msg)
	}
	if strings.Contains(msg, "to see available") {
		t.Errorf("must not suggest listing when listCmd is empty: %q", msg)
	}
}

func TestRequireOneArg_ExtraArgs_Rejected(t *testing.T) {
	cmd := buildCmdTree("db", "info")
	err := requireOneArg("name", "db list")(cmd, []string{"foo", "bar"})
	if err == nil {
		t.Fatal("expected error for too many args")
	}
	if !strings.Contains(err.Error(), "accepts at most 1") {
		t.Errorf("expected too-many-args message, got: %q", err.Error())
	}
}

func TestRequireOneArg_HappyPath(t *testing.T) {
	cmd := buildCmdTree("db", "info")
	if err := requireOneArg("name", "db list")(cmd, []string{"foo"}); err != nil {
		t.Errorf("unexpected error for valid single arg: %v", err)
	}
}

func TestRequireAtLeastOneArg_AllowsMany(t *testing.T) {
	cmd := buildCmdTree("env", "set")
	if err := requireAtLeastOneArg("KEY=VALUE", "")(cmd, []string{"A=1", "B=2", "C=3"}); err != nil {
		t.Errorf("unexpected error for multi-arg call: %v", err)
	}
}

func TestRequireAtLeastOneArg_MissingArg(t *testing.T) {
	cmd := buildCmdTree("env", "remove")
	err := requireAtLeastOneArg("KEY", "env list")(cmd, []string{})
	if err == nil {
		t.Fatal("expected error for missing arg")
	}
	if !strings.Contains(err.Error(), "ghayma env remove requires a KEY") {
		t.Errorf("missing descriptive prefix: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "Run 'ghayma env list'") {
		t.Errorf("missing list-cmd hint: %q", err.Error())
	}
}
