package cmd

import (
	"errors"
	"fmt"
	"os"
	"time"

	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

// Set via ldflags at build time
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

var rootCmd = &cobra.Command{
	Use:   "ghayma",
	Short: "Ghayma CLI",
	Long:  "Deploy and manage your applications on Ghayma",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		if err := sessionPreflight(config.Load(), cmd.Name(), time.Now()); err != nil {
			fmt.Printf("❌ %v\n", err)
			os.Exit(1)
		}
	},
}

// unauthenticatedCommands don't need a live session — and two of them are how
// the user recovers from a dead one, so the pre-flight must never block them.
var unauthenticatedCommands = map[string]bool{
	"login":      true,
	"logout":     true,
	"register":   true,
	"version":    true,
	"whoami":     true,
	"help":       true,
	"completion": true,
}

// sessionPreflight stops a command whose stored session JWT has already
// lapsed. Without it the request goes out anyway, comes back 401, and the
// command reports whatever an empty response happens to mean — which is how
// `ghayma link` came to tell a user with projects that they had none
// (2026-08-16). Only a *known* expiry blocks; the server stays the authority.
func sessionPreflight(cfg *config.Config, cmdName string, now time.Time) error {
	if unauthenticatedCommands[cmdName] {
		return nil
	}
	if cfg.SessionExpired(now) {
		return errors.New("Your CLI session has expired (sessions last 7 days). Run: ghayma login")
	}
	return nil
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the CLI version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("ghayma %s\n", version)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
