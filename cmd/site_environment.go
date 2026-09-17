package cmd

import (
	"errors"
	"fmt"
	"strings"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

// Environments (ENVIRONMENTS-DESIGN-2026-09-17 §§1, 3). A site carries a KIND
// — production | staging | development — and that kind, not a flag on the
// deploy, is what makes a deployment a production one. These two commands are
// the CLI's half of §1 and the `inherit_env` toggle of §3.
//
// They are `site environment` and `site inherit-env`, not `site set-env`:
// `ghayma env` is the env VARIABLES family, and a command called `site set-env`
// would read as "set this site's variables" to everyone who has ever typed
// `ghayma env set`. The two names also match their routes one-for-one.

// siteEnvKind backs `site create --env`.
var siteEnvKind string

// environmentAliases are the short forms people type. The canonical values are
// the only thing that reaches the wire.
var environmentAliases = map[string]string{
	"prod":        api.EnvironmentProduction,
	"production":  api.EnvironmentProduction,
	"stage":       api.EnvironmentStaging,
	"staging":     api.EnvironmentStaging,
	"dev":         api.EnvironmentDevelopment,
	"development": api.EnvironmentDevelopment,
}

// parseEnvironmentKind normalises what the user typed. "" stays "" — the
// server then applies its own default (development for a secondary site),
// which is the difference between "I didn't say" and "I said development".
func parseEnvironmentKind(arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", nil
	}
	if kind, ok := environmentAliases[strings.ToLower(arg)]; ok {
		return kind, nil
	}
	return "", fmt.Errorf("unknown environment %q — use %s, %s or %s",
		arg, api.EnvironmentProduction, api.EnvironmentStaging, api.EnvironmentDevelopment)
}

// parseOnOff reads the inherit-env argument.
func parseOnOff(arg string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "on", "true", "yes", "enable", "enabled":
		return true, nil
	case "off", "false", "no", "disable", "disabled":
		return false, nil
	}
	return false, fmt.Errorf("expected 'on' or 'off', got %q", arg)
}

// createdEnvironmentLine describes what the new site was born as. A backend
// that predates the field answers no environment, and the line is then omitted
// rather than guessed.
func createdEnvironmentLine(site *api.Site) string {
	if site == nil || site.Environment == "" {
		return ""
	}
	line := fmt.Sprintf("   Environment: %s", site.Environment)
	if site.InheritEnv {
		line += " · inherits the default site's variables (override any of them with 'ghayma env set')"
	}
	return line
}

// environmentFailure turns a refusal into the sentence that names the way out.
// The two 409s are the invariants of the model, not transient errors: the
// default site is always production (D2) and is the ladder's base (§3), so
// neither is retryable and both deserve a next step rather than a bare error.
func environmentFailure(err error, siteSlug string) string {
	var apiErr *api.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case api.CodeDefaultSiteEnvironmentLocked, api.CodeDefaultSiteLocked:
			return fmt.Sprintf("%s\n   '%s' is the site served at the project's bare URL. Create another site for this environment instead: ghayma site create <name> --env <kind>", apiErr.Message, siteSlug)
		case api.CodeDefaultSiteCannotInherit:
			return fmt.Sprintf("%s\n   '%s' is what the other sites inherit FROM; turn inheritance on for one of them instead.", apiErr.Message, siteSlug)
		}
		if apiErr.Status == 404 && apiErr.Code == "" {
			return "This platform does not serve site environments yet — the update that adds them is not deployed."
		}
	}
	return fmt.Sprintf("%v", err)
}

// resolveNamedSite maps a positional site name or slug onto a live site of the
// project the working directory is linked to. The site is positional and
// required on purpose: re-kinding a site changes what its deploys count as and
// who may touch them, so it is never inferred from the current directory.
func resolveNamedSite(name string) (*api.Client, string, *api.Site, error) {
	cfg := config.Load()
	if !cfg.LoggedIn() {
		return nil, "", nil, errors.New("Please login first: ghayma login")
	}
	projectID, _, _, err := localConfig()
	if err != nil {
		return nil, "", nil, err
	}
	client := api.NewClient(cfg)
	sites, err := client.ListSites(projectID)
	if err != nil {
		return nil, "", nil, fmt.Errorf("failed to list sites: %v", err)
	}
	site, err := liveSiteFor(sites, name)
	if err != nil {
		return nil, "", nil, err
	}
	return client, projectID, site, nil
}

var siteEnvironmentCmd = &cobra.Command{
	Use:   "environment <site> <production|staging|development>",
	Short: "Change which environment a site is",
	Long: `Change which environment a site is.

A deployment counts as production exactly when the site it targets is a
production site — this is what decides that, not a flag on the deploy. It also
decides how the CLI and the console warn about sharing a database across
environments.

The project's DEFAULT site is always production and cannot be re-kinded: it is
the site served at the project's bare URL.

Examples:
  ghayma site environment staging staging
  ghayma site environment preview development`,
	Args: argChecker("argument", "site list", 2, 2),
	Run: func(cmd *cobra.Command, args []string) {
		kind, err := parseEnvironmentKind(args[1])
		if err != nil {
			failf("%v", err)
			return
		}
		if kind == "" {
			failf("name an environment: production, staging or development")
			return
		}
		client, projectID, site, err := resolveNamedSite(args[0])
		if err != nil {
			reportSiteError(err)
			exitFn(1)
			return
		}
		if site.Environment == kind {
			fmt.Printf("ℹ️  '%s' is already a %s site — no change.\n", site.Slug, kind)
			return
		}

		updated, err := client.SetSiteEnvironment(projectID, site.ID, kind)
		if err != nil {
			failf("%s", environmentFailure(err, site.Slug))
			return
		}
		from := site.Environment
		if from == "" {
			from = "unknown"
		}
		fmt.Printf("✅ '%s' is now a %s site (was %s)\n", updated.Slug, updated.Environment, from)
		fmt.Println("   It applies from the next deploy of this site.")
	},
}

var siteInheritEnvCmd = &cobra.Command{
	Use:   "inherit-env <site> <on|off>",
	Short: "Turn env var inheritance on or off for a site",
	Long: `Turn env var inheritance on or off for a site.

With it ON the site resolves its environment as the project's DEFAULT site's
variables, overridden key by key by its own — so a development site is not a
hand-copy of production's list that goes stale in silence. Set a variable of
the same name on this site to override an inherited one.

Credentials are never inherited: database URLs, bucket keys, cron secrets, the
auth variables and the managed platform key are derived per site from that
site's own connections. A development site inheriting production's variables
still uses its own database.

The DEFAULT site cannot inherit — it is the base the others read from.

Examples:
  ghayma site inherit-env staging on
  ghayma site inherit-env preview off`,
	Args: argChecker("argument", "site list", 2, 2),
	Run: func(cmd *cobra.Command, args []string) {
		inherit, err := parseOnOff(args[1])
		if err != nil {
			failf("%v", err)
			return
		}
		client, projectID, site, err := resolveNamedSite(args[0])
		if err != nil {
			reportSiteError(err)
			exitFn(1)
			return
		}

		updated, err := client.SetSiteInheritEnv(projectID, site.ID, inherit)
		if err != nil {
			failf("%s", environmentFailure(err, site.Slug))
			return
		}
		if updated.InheritEnv {
			fmt.Printf("✅ '%s' now inherits the default site's variables, overridden by its own\n", updated.Slug)
			fmt.Println("   See what it resolves to: ghayma env list --site " + updated.Slug)
		} else {
			fmt.Printf("✅ '%s' no longer inherits variables — it resolves its own rows only\n", updated.Slug)
			fmt.Println("   Anything it was inheriting stops being injected on its next deploy; nothing was copied down.")
		}
	},
}

func init() {
	siteCmd.AddCommand(siteEnvironmentCmd)
	siteCmd.AddCommand(siteInheritEnvCmd)
}
