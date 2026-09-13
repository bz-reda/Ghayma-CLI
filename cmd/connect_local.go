package cmd

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

// `ghayma connect --local` (2026-09-13). The app's databases live inside the
// cluster and answer nothing from outside it. This opens a tunnel session,
// stands a loopback listener in front of each database, and writes the site's
// effective variables with those addresses swapped in — so the app you run on
// this machine is the deployed app, talking to the same data.

// openSessionFn / pullEnvFn / waitForStopFn are indirected so a test can drive
// a whole run without a platform, a signal or a TTY.
var (
	openSessionFn = func(client *api.Client, projectID, siteID string) (*api.TunnelSession, error) {
		return client.OpenTunnelSession(projectID, siteID)
	}
	pullEnvFn = func(client *api.Client, projectID, siteID string) (map[string]string, error) {
		return client.GetEffectiveSiteEnv(projectID, siteID)
	}
	waitForStopFn = waitForStop
)

func runConnectLocal(cmd *cobra.Command, args []string) {
	cfg := config.Load()
	if !cfg.LoggedIn() {
		failf("Please login first: ghayma login")
		return
	}
	client := api.NewClient(cfg)
	target, err := resolveConnectionTarget(client, connectSite, "tunnel into")
	if err != nil {
		reportSiteError(err)
		exitFn(1)
		return
	}
	tunnelSite(client, target)
}

// tunnelSite is the command once the site is known: open a session, bind a
// listener per database, write the rewritten environment, then serve until the
// user stops it or the session dies.
func tunnelSite(client *api.Client, target *connectionTarget) {
	session, err := openSessionFn(client, target.ProjectID, target.Site.ID)
	if err != nil {
		reportTunnelSessionError(err)
		return
	}

	// Every way out of here gives the session back rather than leaving it open
	// until it expires.
	var closeOnce sync.Once
	closeSession := func() {
		closeOnce.Do(func() { client.CloseTunnelSession(target.ProjectID, target.Site.ID, session.Token) })
	}
	defer closeSession()

	if len(session.Targets) == 0 {
		failf("'%s' has no database connection to tunnel — connect one with: ghayma connect database <name>", target.Site.Slug)
		return
	}

	listeners := planListeners(session.Targets, portFree)
	bound, err := bindListeners(listeners)
	if err != nil {
		failf("%v", err)
		return
	}
	defer closeListeners(bound)

	env, err := pullEnvFn(client, target.ProjectID, target.Site.ID)
	if err != nil {
		failf("Failed to read the app's variables: %v", err)
		return
	}
	local, changed := rewriteEnvForLocal(env, listeners)

	out := localEnvPath(connectLocalOut, target.AppDir)
	if err := writeLocalEnv(out, target.ProjectName, target.Site.Slug, local, listeners, connectLocalForce); err != nil {
		failf("%v", err)
		return
	}

	fatal := make(chan error, 1)
	for i, ln := range bound {
		go serveListener(ln, listeners[i], session, fatal)
	}

	fmt.Printf("🔌 Tunnel open for '%s' — its databases answer on this machine:\n", target.Site.Slug)
	for _, l := range listeners {
		fmt.Printf("   %s (%s) → %s\n", l.Target.Name, l.Target.Kind, l.Addr)
	}
	fmt.Printf("   Wrote %d variables to %s (hosts point at the listeners; restore with: ghayma env pull)\n", len(local), displayPath(out))
	if len(changed) == 0 {
		fmt.Println("   ⚠️  No variable named one of those databases, so nothing was rewritten.")
	}
	fmt.Println("   Leave this running; Ctrl-C closes the tunnel.")

	stopErr := waitForStopFn(fatal)
	closeListeners(bound)
	closeSession()
	if stopErr != nil {
		failf("%v", stopErr)
		return
	}
	fmt.Println("✅ Tunnel closed.")
}

// reportTunnelSessionError maps the ways a session is refused onto what the
// user can do about it. A bare 404 is the route itself missing: a CLI released
// ahead of the platform, not a site that is not there.
func reportTunnelSessionError(err error) {
	switch {
	case strings.Contains(err.Error(), "insufficient role"):
		failf("The project admin role is needed to tunnel into an app (the tunnel carries the app's database credentials) — ask the project owner")
	case strings.Contains(err.Error(), "404 page not found"):
		failf("This platform does not serve tunnel sessions yet — the update that adds them is not deployed. Until then: ghayma db expose <name>")
	default:
		failf("%v", err)
	}
}

// bindListeners opens every planned listener, unwinding the ones already open
// if one cannot bind — a half-bound tunnel would write an env file naming
// addresses that answer nothing.
func bindListeners(ls []localListener) ([]net.Listener, error) {
	bound := make([]net.Listener, 0, len(ls))
	for _, l := range ls {
		ln, err := net.Listen("tcp", l.Addr)
		if err != nil {
			closeListeners(bound)
			return nil, fmt.Errorf("cannot listen on %s for '%s': %v", l.Addr, l.Target.Name, err)
		}
		bound = append(bound, ln)
	}
	return bound, nil
}

func closeListeners(bound []net.Listener) {
	for _, ln := range bound {
		ln.Close()
	}
}

// serveListener gives every accepted connection its own gateway stream, and
// returns when the listener closes. A stream that cannot be opened takes its
// local connection down with a line saying why; a session the gateway no
// longer knows ends the command instead, so it is reported through fatal
// rather than printed here.
func serveListener(ln net.Listener, l localListener, session *api.TunnelSession, fatal chan<- error) {
	for {
		local, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			remote, err := api.DialTunnel(context.Background(), session.GatewayURL, session.Token, l.Target.ID)
			if err != nil {
				local.Close()
				if strings.Contains(err.Error(), "tunnel session expired") {
					select {
					case fatal <- err:
					default:
					}
					return
				}
				fmt.Printf("⚠️  %s: %v\n", l.Target.Name, err)
				return
			}
			api.PipeTunnel(local, remote)
		}()
	}
}

// waitForStop blocks until the user interrupts the command, or a stream
// reports the session is gone — which it returns, since that ends the run.
func waitForStop(fatal <-chan error) error {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)
	select {
	case <-stop:
		return nil
	case err := <-fatal:
		return err
	}
}

// writeLocalEnv writes the rewritten environment through exactly the path
// `env pull` uses: the git-safety refusal first, then a file only its owner
// can read. No value is ever printed.
func writeLocalEnv(path, projectName, siteSlug string, env map[string]string, ls []localListener, force bool) error {
	tracked, ignored, inRepo := gitFileState(path)
	if err := envPullRefusal(tracked, ignored, inRepo, force, filepath.Base(path)); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(renderDotenv(localHeader(projectName, siteSlug, ls), env)), 0o600)
}

// localEnvPath resolves where the dotenv file goes: --out, relative to the
// working directory, or .env.local next to the app.
func localEnvPath(out, appDir string) string {
	if out == "" {
		return filepath.Join(appDir, ".env.local")
	}
	if filepath.IsAbs(out) {
		return out
	}
	cwd, _ := os.Getwd()
	return filepath.Join(cwd, out)
}

// displayPath prefers the path as the user would type it from here.
func displayPath(path string) string {
	cwd, err := os.Getwd()
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(cwd, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	return rel
}
