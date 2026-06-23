package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// requireOneArg returns a cobra.PositionalArgs that requires exactly one
// positional argument. Produces a helpful stderr message when the arg is
// missing or extra args are provided.
//
// listCmd is an optional follow-up hint, e.g. "auth list". When empty, the
// fallback is `ghayma <group> --help`. This makes the helper useful for
// commands where there's no meaningful list to point at (e.g. `db create`
// takes a user-chosen new name).
func requireOneArg(argName, listCmd string) cobra.PositionalArgs {
	return argChecker(argName, listCmd, 1, 1)
}

// requireAtLeastOneArg is the same as requireOneArg but allows multiple
// positional arguments (e.g. `env set KEY1=VAL KEY2=VAL`).
func requireAtLeastOneArg(argName, listCmd string) cobra.PositionalArgs {
	return argChecker(argName, listCmd, 1, -1)
}

// argChecker enforces a [min, max] positional-arg count (max == -1 means
// unlimited). All error output goes to stderr (cobra default when the Args
// func returns error — we silence usage/errors so only our message prints).
func argChecker(argName, listCmd string, min, max int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) >= min && (max < 0 || len(args) <= max) {
			return nil
		}

		// Prevent cobra from dumping usage text + "Error: …" around our
		// carefully-formatted message. Our rootCmd.Execute() prints the
		// error to stderr itself.
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true

		hint := buildArgHint(cmd, listCmd)

		if len(args) < min {
			if min == 1 {
				return fmt.Errorf("%s requires a %s.\n%s", cmd.CommandPath(), argName, hint)
			}
			return fmt.Errorf("%s requires at least %d %s(s).\n%s", cmd.CommandPath(), min, argName, hint)
		}
		return fmt.Errorf("%s accepts at most %d %s(s), got %d.\n%s",
			cmd.CommandPath(), max, argName, len(args), hint)
	}
}

// buildArgHint picks the best follow-up hint: an explicit listCmd when the
// caller provided one, otherwise the parent group's --help, otherwise the
// command's own --help.
func buildArgHint(cmd *cobra.Command, listCmd string) string {
	if listCmd != "" {
		return fmt.Sprintf("Run 'ghayma %s' to see available.", listCmd)
	}
	if parent := cmd.Parent(); parent != nil && parent != cmd.Root() {
		group := strings.TrimPrefix(parent.CommandPath(), cmd.Root().Name()+" ")
		return fmt.Sprintf("Run 'ghayma %s --help' for options.", group)
	}
	return fmt.Sprintf("Run '%s --help' for options.", cmd.CommandPath())
}
