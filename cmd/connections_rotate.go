package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

var (
	connectionsRotateSite string
	connectionsRotateYes  bool
)

var connectionsRotateCmd = &cobra.Command{
	Use:   "rotate <database|bucket|auth> <name>",
	Short: "Replace this app's credential for a database or bucket",
	Long: `Rotate the credential one app holds for one service. Only this connection
changes: the other apps using the same database or bucket keep theirs, and
the service's own shared credential is untouched.

The new value reaches the running app immediately — its pods restart with it
— and every later deploy. A locally pulled .env.local keeps the old value
until you run 'ghayma env pull' again. Asks for confirmation unless --yes.

Auth apps have no per-connection credential; rotate their keys with
'ghayma auth rotate-keys'.

Examples:
  ghayma connections rotate database my-postgres
  ghayma connections rotate bucket uploads --site admin --yes`,
	Args: argChecker("argument", "connections", 2, 2),
	Run:  runConnectionsRotate,
}

// confirmRotate is the two-step confirm, skipped by --yes. Rotation has no
// preview step, so the prompt says what changes the moment it is answered.
// ask reads one token from the terminal; tests inject it.
func confirmRotate(kind, name, siteSlug string, yes bool, ask func() string) bool {
	if yes {
		return true
	}
	fmt.Printf("⚠️  This replaces the credential '%s' uses for %s '%s' now: the running app is updated immediately (its pods restart with the new value), and anything using a locally pulled .env.local needs 'ghayma env pull' again. Continue? [y/N] ", siteSlug, kindLabel(kind), name)
	answer := strings.TrimSpace(ask())
	return answer == "y" || answer == "Y"
}

// serviceRotateHint names the command that rotates the service's own shared
// credential — the way out of the 409.
func serviceRotateHint(kind, name string) string {
	if kind == "bucket" {
		return "ghayma storage rotate " + name
	}
	return "ghayma db rotate " + name
}

// rotateFailure turns the server's refusal into the sentence that names the
// way out. 409 is a connection served by the service's shared credential
// (standalone MongoDB, engines from before per-connection credentials): there
// is nothing of its own to rotate, so the service-level command is the one to
// run. 503 is an engine that cannot be reached right now — a stopped database
// — or a rotation that did not complete; the server's own text says a retry
// converges, so it is printed as it came.
func rotateFailure(err error, kind, name string) string {
	var apiErr *api.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case http.StatusConflict:
			return fmt.Sprintf("This connection uses the %s's shared credential — it has nothing of its own to rotate.\n   Rotate that credential with: %s", kindLabel(kind), serviceRotateHint(kind, name))
		case http.StatusServiceUnavailable:
			msg := apiErr.Message
			if msg == "" {
				msg = fmt.Sprintf("the %s could not be reached to rotate the credential", kindLabel(kind))
			}
			return fmt.Sprintf("%s\n   Nothing was changed — run the same command again once it answers; repeating the rotation is safe.", msg)
		case http.StatusNotFound:
			return fmt.Sprintf("%s '%s' is not connected to this app — nothing to rotate.", capitalize(kindLabel(kind)), name)
		}
	}
	return fmt.Sprintf("Failed to rotate: %v", err)
}

// rotateConnection is the acting half of the command, on an already-resolved
// (project, site): find the connection the user named, confirm, rotate, render.
func rotateConnection(client *api.Client, target *connectionTarget, kind, name string, yes bool, ask func() string) {
	view, err := client.GetSiteConnections(target.ProjectID, target.Site.ID)
	if err != nil {
		failf("Failed to load connections: %v", err)
		return
	}
	held, _, err := findConnectable(view, kind, name)
	if err != nil {
		failf("%v", err)
		return
	}
	if held == nil {
		fmt.Printf("ℹ️  %s '%s' is not connected to '%s' — nothing to rotate.\n", kindLabel(kind), name, target.Site.Slug)
		return
	}
	if !confirmRotate(kind, held.ResourceName, target.Site.Slug, yes, ask) {
		fmt.Println("❌ Cancelled.")
		return
	}

	if err := client.RotateSiteConnection(target.ProjectID, target.Site.ID, kind, held.ResourceID); err != nil {
		failf("%s", rotateFailure(err, kind, held.ResourceName))
		return
	}
	fmt.Printf("✅ Rotated the credential of %s '%s' for '%s'\n", kindLabel(kind), held.ResourceName, target.Site.Slug)
	fmt.Println("   Variables are live on the running app; pull them locally with: ghayma env pull")
}

func runConnectionsRotate(cmd *cobra.Command, args []string) {
	cfg := config.Load()
	if !cfg.LoggedIn() {
		failf("Please login first: ghayma login")
		return
	}
	kind, err := parseConnectionKind(args[0])
	if err != nil {
		failf("%v", err)
		return
	}
	// An auth app's credential model is not per-connection, so there is nothing
	// here to rotate. The server would answer 409 anyway; refusing before any
	// request costs no round-trip and buys a sentence that names the command
	// that does rotate an auth app's keys.
	if kind == "auth_app" {
		failf("Rotation applies to database and bucket connections — an auth app has no per-connection credential.\n   Rotate its keys with: ghayma auth rotate-keys %s", args[1])
		return
	}
	client := api.NewClient(cfg)
	target, err := resolveConnectionTarget(client, connectionsRotateSite, "rotate a credential for")
	if err != nil {
		reportSiteError(err)
		exitFn(1)
		return
	}
	rotateConnection(client, target, kind, args[1], connectionsRotateYes, readAnswer)
}

func init() {
	connectionsRotateCmd.Flags().StringVar(&connectionsRotateSite, "site", "", "Site (app) whose credential to rotate, by name or slug")
	connectionsRotateCmd.Flags().BoolVar(&connectionsRotateYes, "yes", false, "Skip the confirmation")
	connectionsCmd.AddCommand(connectionsRotateCmd)
}
