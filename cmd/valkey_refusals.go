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

// notRunningToConnect answers a connection refused with database_not_running.
func notRunningToConnect(name string) string {
	return fmt.Sprintf("%s is not running yet, so no app can connect to it. Wait until ghayma db info %s shows running, then run this again.", name, name)
}

func valkeyNoShrink(name string, diskGB int) string {
	text := "A Valkey disk can grow, but it cannot shrink yet."
	if diskGB > 0 {
		text += fmt.Sprintf(" %s keeps its %d GB disk.", name, diskGB)
	}
	return text
}
