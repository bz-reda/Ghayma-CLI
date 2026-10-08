package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"paas-cli/internal/api"
	"paas-cli/internal/config"
)

var (
	dbLogsLines  int
	dbLogsFollow bool
)

// dbLogsStopFn cancels ctx on Ctrl-C; tests replace it.
var dbLogsStopFn = func(cancel context.CancelFunc) func() {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		if _, ok := <-stop; ok {
			cancel()
		}
	}()
	return func() { signal.Stop(stop); close(stop) }
}

var dbLogsCmd = &cobra.Command{
	Use:   "logs [name]",
	Short: "Show a database's engine log",
	Long: `Show the last lines of a database's engine log (any engine).

--follow keeps printing new lines until Ctrl-C; the platform ends a follow
after 10 minutes, so run it again to keep watching.`,
	Args: requireOneArg("name", "db list"),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		lines := min(max(dbLogsLines, 1), 1000)

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

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		release := dbLogsStopFn(cancel)
		defer release()

		rc, err := client.StreamDatabaseLogs(ctx, db.ID, lines, dbLogsFollow)
		if err != nil {
			// Ctrl-C before the stream opened.
			if ctx.Err() != nil {
				return
			}
			failf("%s", logsFailure(name, db.Status, err))
			return
		}
		defer rc.Close()
		_, err = io.Copy(os.Stdout, rc)
		switch {
		case ctx.Err() != nil:
		case err != nil:
			failf("The log stream broke: %v", err)
		case dbLogsFollow:
			fmt.Println("ℹ️  The stream ended (the platform ends a follow after 10 minutes, or when the database restarts). Run the command again to keep following.")
		}
	},
}

// logsFailure words a refused log read; status is the database's known status.
func logsFailure(name, status string, err error) string {
	if hasAPICode(err, api.CodeNoPod) {
		if status == dbStatusStopped {
			return fmt.Sprintf("%s is stopped, so it has no running log. Start it with: ghayma db start %s", name, name)
		}
		return fmt.Sprintf("%s is not running yet, so it has no log. Check: ghayma db info %s", name, name)
	}
	return fmt.Sprintf("Could not read the log: %v", err)
}
