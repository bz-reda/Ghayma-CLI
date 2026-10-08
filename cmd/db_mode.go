package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"paas-cli/internal/api"
	"paas-cli/internal/config"
)

var dbModeCmd = &cobra.Command{
	Use:   "mode [name] cache|store",
	Short: "Switch a Valkey database between cache and store",
	Long: `Switch a Valkey database between its two modes.

  cache  evicts the least-recently-used keys when memory is full; snapshots only.
  store  never evicts: writes fail when memory is full; a snapshot plus an
         append-only file synced every second.

The database restarts into the new mode: a few seconds of downtime. Moving to
store first writes the append-only file, which takes longer on a large dataset.`,
	Args: argChecker("argument", "db list", 2, 2),
	Run: func(cmd *cobra.Command, args []string) {
		name, mode := args[0], args[1]
		if mode != "cache" && mode != "store" {
			failf("The mode is cache or store, not %q.", mode)
			return
		}

		cfg := config.Load()
		if !cfg.LoggedIn() {
			failf("Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		db, err := findDatabaseByName(client, name)
		if err != nil {
			failf("%v", err)
			return
		}
		if db.Type != dbTypeValkey {
			failf("%s is a %s database: only Valkey has a mode.", name, db.Type)
			return
		}
		if db.ValkeyMode == mode {
			fmt.Printf("ℹ️  %s is already in %s mode.\n", name, mode)
			return
		}

		fmt.Printf("⏳ Switching %s to %s mode. It restarts: a few seconds of downtime...\n", name, mode)
		updated, err := client.SetValkeyMode(db.ID, mode)
		if err != nil {
			failf("%s", modeFailure(name, err))
			return
		}
		got := updated.ValkeyMode
		if got == "" {
			got = mode
		}
		fmt.Printf("✅ %s is in %s mode: %s\n", name, got, valkeyModeMeaning(got))
	},
}

// modeFailure words a refused mode switch; the server's not-running sentence
// speaks of disks, so it is replaced.
func modeFailure(name string, err error) string {
	var apiErr *api.APIError
	if errors.As(err, &apiErr) && apiErr.Code == api.CodeDatabaseNotRunning {
		return fmt.Sprintf("%s is not running, so its mode cannot change. Start it first: ghayma db start %s", name, name)
	}
	return fmt.Sprintf("Mode switch failed: %v", err)
}
