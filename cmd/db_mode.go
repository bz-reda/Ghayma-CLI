package cmd

import (
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
	Args: requireTwoArgs("name", "mode (cache or store)", "db list"),
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
		if db.Status != dbStatusRunning {
			failf("%s", modeBlocked(name, db.Status))
			return
		}

		fmt.Printf("⏳ Switching %s to %s mode. It restarts: a few seconds of downtime...\n", name, mode)
		updated, err := client.SetValkeyMode(db.ID, mode)
		if err != nil {
			// It stopped or changed state since it was read: say what it is now.
			status := ""
			if hasAPICode(err, api.CodeDatabaseNotRunning) {
				if now, getErr := client.GetDatabase(db.ID); getErr == nil {
					status = now.Status
				}
			}
			failf("%s", modeFailure(name, mode, status, err))
			return
		}
		got := updated.ValkeyMode
		if got == "" {
			got = mode
		}
		fmt.Printf("✅ %s is in %s mode: %s\n", name, got, valkeyModeMeaning(got))
	},
}

// modeBlocked says why a database that is not running cannot switch mode. An
// unknown status keeps the generic sentence.
func modeBlocked(name, status string) string {
	switch status {
	case dbStatusStopped:
		return fmt.Sprintf("%s is stopped, so its mode cannot change. Start it first: ghayma db start %s", name, name)
	case "":
		return fmt.Sprintf("%s is not running, so its mode cannot change. Start it first: ghayma db start %s", name, name)
	case dbStatusError:
		status = "in error"
	}
	return fmt.Sprintf("%s is %s, so its mode cannot change now. Try again once ghayma db info %s shows running.", name, status, name)
}

// modeFailure words a refused mode switch; the server's not-running sentence
// speaks of disks, so it is replaced. status is the one read back after the
// refusal: "running" again means it was only briefly down.
func modeFailure(name, mode, status string, err error) string {
	if !hasAPICode(err, api.CodeDatabaseNotRunning) {
		return fmt.Sprintf("Mode switch failed: %v", err)
	}
	if status == dbStatusRunning {
		return fmt.Sprintf("%s was not running a moment ago. Try again: ghayma db mode %s %s", name, name, mode)
	}
	return modeBlocked(name, status)
}
