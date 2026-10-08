package cmd

import (
	"errors"
	"fmt"

	"paas-cli/internal/api"
)

// Valkey answers some database commands differently from Postgres and MongoDB:
// no shared credential, no public access yet, a disk that cannot shrink yet,
// and no app connects before it runs. These sentences point at what works.

const valkeyNoPublicAccess = "Public access is not available for Valkey yet — use ghayma connect --local."

// hasAPICode reports whether err is a refusal carrying the server's code.
func hasAPICode(err error, code string) bool {
	var apiErr *api.APIError
	return errors.As(err, &apiErr) && apiErr.Code == code
}

// notRunningToConnect answers a connection refused with database_not_running
// when the database's status is not known.
func notRunningToConnect(name string) string {
	return fmt.Sprintf("%s is not running, so no app can connect to it yet. Start it if it is stopped (ghayma db start %s), or wait until ghayma db info %s shows running.", name, name, name)
}

// sitesNotRunning is the same refusal for db sites, which knows the status.
func sitesNotRunning(name, status string) string {
	if status == dbStatusStopped {
		return fmt.Sprintf("%s is stopped. Start it with: ghayma db start %s, then run this again.", name, name)
	}
	return fmt.Sprintf("%s is not running yet, so no app can connect to it. Wait until ghayma db info %s shows running, then run this again.", name, name)
}

// valkeyNoShrink is the server's shrink_unsupported refusal. It names no size:
// the row read before the request may be stale.
const valkeyNoShrink = "A Valkey disk can grow, but it cannot shrink yet."

// valkeyKeepsDisk is the local refusal, from the size just read.
func valkeyKeepsDisk(name string, diskGB int) string {
	return fmt.Sprintf("%s %s keeps its %d GB disk.", valkeyNoShrink, name, diskGB)
}
