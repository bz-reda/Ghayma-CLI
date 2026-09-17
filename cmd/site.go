package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
)

var siteCmd = &cobra.Command{
	Use:   "site",
	Short: "Manage sites within a project",
}

var siteListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all sites in the current project",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if !cfg.LoggedIn() {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		projectID, _, _, err := localConfig()
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		client := api.NewClient(cfg)
		sites, err := client.ListSites(projectID)
		if err != nil {
			fmt.Printf("❌ Failed to list sites: %v\n", err)
			return
		}

		if len(sites) == 0 {
			fmt.Println("No sites found.")
			return
		}

		data, _ := readProjectConfigUp(".")
		var localCfg struct {
			SiteID string `json:"site_id"`
		}
		json.Unmarshal(data, &localCfg)

		fmt.Println("📌 Sites:")
		for _, s := range sites {
			fmt.Println(siteListLine(s, s.ID == localCfg.SiteID))
		}
		fmt.Println("\nTo switch active site: ghayma site use <slug>")
	},
}

// siteListLine renders one row of `site list`. The environment is part of a
// site's identity since Environments (§1) — it decides whether a deploy is a
// production one — so it sits next to the slug; a site resolving its variables
// through the ladder says so, because that is why its env list shows rows it
// does not own. A server that predates the field sends no environment, and the
// row then reads exactly as it always did rather than claiming a kind.
func siteListLine(s api.Site, active bool) string {
	marker := "  "
	if active {
		marker = "▶ "
	}
	env := ""
	if s.Environment != "" {
		env = fmt.Sprintf("env: %s, ", s.Environment)
	}
	inherits := ""
	if s.InheritEnv {
		inherits = "  [inherits env]"
	}
	return fmt.Sprintf("  %s%s  (slug: %s, %sstatus: %s, id: %s)%s", marker, s.Name, s.Slug, env, s.Status, s.ID, inherits)
}

func runSiteCreate(cmd *cobra.Command, args []string) {
	cfg := config.Load()
	if !cfg.LoggedIn() {
		fmt.Println("❌ Please login first: ghayma login")
		return
	}

	projectID, _, _, err := localConfig()
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		return
	}

	var siteName string
	if len(args) > 0 {
		siteName = args[0]
	} else {
		prompt := promptui.Prompt{Label: "Site name (e.g. admin, api, frontend)"}
		siteName, _ = prompt.Run()
	}
	if siteName == "" {
		fmt.Println("❌ Site name is required")
		return
	}

	// --env is checked before the request so a typo costs no round-trip and
	// reads as the CLI's own message rather than a server binding error.
	environment, err := parseEnvironmentKind(siteEnvKind)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		return
	}

	client := api.NewClient(cfg)
	site, err := client.CreateSite(projectID, siteName, environment)
	if err != nil {
		fmt.Printf("❌ Failed to create site: %v\n", err)
		return
	}

	fmt.Printf("✅ Site '%s' created (slug: %s, id: %s)\n", site.Name, site.Slug, site.ID)
	if line := createdEnvironmentLine(site); line != "" {
		fmt.Println(line)
	}

	// In a linked workspace the new site is unreachable until the manifest says
	// which directory builds it, so offer that mapping here. `site use` is not
	// the answer there — a manifest has no single active site.
	inWorkspace, added := mapNewSiteInManifest(site)
	switch {
	case added:
		fmt.Printf("\nTo deploy to this site:\n")
		fmt.Printf("  ghayma deploy --site %s\n", site.Slug)
	case inWorkspace:
		// mapNewSiteInManifest already said how to map it later.
	default:
		fmt.Printf("\nTo deploy to this site, switch to it first:\n")
		fmt.Printf("  ghayma site use %s\n", site.Slug)
		fmt.Printf("  ghayma deploy\n")
	}
}

var siteCreateCmd = &cobra.Command{
	Use:   "create [name]",
	Short: "Create a new site in the current project",
	Long: `Create a new site (app) in the current project.

A site IS an environment: it has its own namespace, variables, connections and
credentials, its own domain and its own deployment history. --env names which
kind it is — a deploy counts as production exactly when the site it targets is
a production site. New sites are development sites unless you say otherwise;
the project's default site is always production and cannot be re-kinded.

A new non-production site also starts with env var inheritance ON: it resolves
the default site's variables as its base and overrides them key by key, so
standing one up is not a hand-copy of production's list. Credentials are never
inherited — a development site gets its own database through its own
connection. Turn it off with 'ghayma site inherit-env <site> off'.

Examples:
  ghayma site create staging --env staging
  ghayma site create admin --env production`,
	Args: cobra.MaximumNArgs(1),
	Run:  runSiteCreate,
}

// siteAddCmd is the deprecated alias; hidden from help output, prints a
// deprecation warning the first time per week it's invoked.
var siteAddCmd = &cobra.Command{
	Use:    "add [name]",
	Short:  "(deprecated) alias for 'site create'",
	Hidden: true,
	Args:   cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		maybeWarnDeprecated("site add", "site create", "a future release")
		runSiteCreate(cmd, args)
	},
}

var siteUseCmd = &cobra.Command{
	Use:   "use <slug>",
	Short: "Switch the active site for the project",
	Args:  requireOneArg("slug", "site list"),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if !cfg.LoggedIn() {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		slug := args[0]

		// The exact-directory read is deliberate: `site use` pins the site of
		// THIS directory's config and writes that same file back.
		configPath, err := findProjectConfig(".")
		if err != nil {
			// No file here — but a workspace manifest above may already map
			// this directory to a site, in which case the manifest is the
			// thing to edit, not a config to init.
			if cwd, wdErr := os.Getwd(); wdErr == nil {
				if ctx, resolveErr := resolveSiteContext(cwd, "", "switch"); resolveErr == nil && ctx.FromManifest {
					fmt.Printf("❌ This directory builds site %q per %s — the manifest decides which site a directory deploys; edit its root_directory entries to change that.\n", siteLabel(ctx.Site), ctx.ConfigPath)
					return
				}
			}
			fmt.Println("❌ No project config found. Run 'ghayma init' first.")
			return
		}
		data, err := os.ReadFile(configPath)
		if err != nil {
			fmt.Printf("❌ Could not read %s: %v\n", configPath, err)
			return
		}
		// A manifest has no single active site, and round-tripping it through a
		// per-app struct would drop `sites` and destroy every other site.
		if err := manifestHasNoActiveSite(data); err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		// perAppConfig, not ProjectConfig: the write-back below rewrites the
		// whole file, so anything the narrower struct doesn't carry
		// (dockerfile_path, crons, …) would be silently deleted (2026-08-16).
		var appCfg perAppConfig
		if err := json.Unmarshal(data, &appCfg); err != nil {
			fmt.Printf("❌ %s is not valid JSON: %v\n", configPath, err)
			return
		}

		client := api.NewClient(cfg)
		sites, err := client.ListSites(appCfg.ProjectID)
		if err != nil {
			fmt.Printf("❌ Failed to list sites: %v\n", err)
			return
		}

		// Nothing to switch to yet — say that, rather than "site not found",
		// which reads as a typo.
		if len(sites) == 0 {
			failNoSite()
			return
		}

		var matched *api.Site
		for i, s := range sites {
			if s.Slug == slug || s.Name == slug {
				matched = &sites[i]
				break
			}
		}

		if matched == nil {
			fmt.Printf("❌ Site '%s' not found in this project\n", slug)
			fmt.Println("   Run 'ghayma site list' to see available sites")
			return
		}

		appCfg.SiteID = matched.ID
		appCfg.SiteName = matched.Name
		appCfg.SiteSlug = matched.Slug

		// Update-in-place: write back to the same file we read, so a legacy
		// .espacetech.json project stays on .espacetech.json instead of silently
		// migrating to .ghayma.json (which would strand teammates on the old CLI).
		if err := writeProjectConfigUpdate(".", appCfg); err != nil {
			fmt.Printf("❌ Failed to update project config: %v\n", err)
			return
		}

		fmt.Printf("✅ Active site switched to '%s' (slug: %s)\n", matched.Name, matched.Slug)
		fmt.Println("   Run 'ghayma deploy' to deploy to this site")
	},
}

var (
	siteScaleSite     string
	siteScaleTier     string
	siteScaleReplicas int
)

var siteScaleCmd = &cobra.Command{
	Use:   "scale",
	Short: "Change an app's compute tier and/or replica count (priced in points)",
	Long: `Change an app's compute tier and/or replica count.

Both flags are optional. An omitted --tier keeps the current tier; an omitted
--replicas keeps the current count. The current size is shown first, then the
new points footprint (tier × replicas) is previewed before the change is sent.

Examples:
  ghayma site scale --tier c            # bigger tier, same replicas
  ghayma site scale --replicas 3        # more replicas, same tier
  ghayma site scale --site api --tier b --replicas 2`,
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if !cfg.LoggedIn() {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		projectID, configSiteID, _, err := localConfig()
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		client := api.NewClient(cfg)
		sites, err := client.ListSites(projectID)
		if err != nil {
			fmt.Printf("❌ Failed to list sites: %v\n", err)
			return
		}

		// There is no app to scale on a site-less project.
		if len(sites) == 0 {
			failNoSite()
			return
		}

		site, err := resolveScaleTarget(sites, siteScaleSite, configSiteID)
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		// Show the current size first (the brief requires it).
		fmt.Printf("📌 %s (slug: %s)\n", site.Name, site.Slug)
		fmt.Printf("   Current: tier %s · %d replica(s)\n", displayTier(site.AppTierSlug), site.Replicas)

		if !cmd.Flags().Changed("tier") && !cmd.Flags().Changed("replicas") {
			fmt.Println("\nNothing to change. Pass --tier and/or --replicas.")
			return
		}

		// An app with no compute tier yet (never enveloped) can't be scaled
		// without choosing one: resolveScaleValues would send app_tier_slug:""
		// which the backend rejects with a raw binding:"required" 400. Fail
		// early with guidance instead.
		if msg := unenvelopedTierError(site.AppTierSlug, cmd.Flags().Changed("tier")); msg != "" {
			fmt.Printf("❌ %s\n", msg)
			return
		}

		tier, replicas := resolveScaleValues(site, siteScaleTier, siteScaleReplicas, cmd.Flags().Changed("replicas"))

		// Reject scale-to-zero client-side with the backend's own reason — never
		// send a sub-1 request.
		if msg := replicasBelowMinimum(replicas); msg != "" {
			fmt.Printf("❌ %s\n", msg)
			return
		}

		// Fail-soft preview. An older backend (pre-catalog) returns
		// ErrCatalogUnavailable — skip the points line and fall back to a bare
		// scale; never block the change.
		if cat, catErr := client.GetMarketplaceCatalog(); catErr == nil && cat != nil {
			if newCost, err := appCostPreview(cat, tier, replicas); err == nil {
				// A resize only spends the delta; fall back to the full new cost
				// when the current tier can't be priced (e.g. not in the catalog).
				delta := newCost
				if oldCost, oerr := appCostPreview(cat, site.AppTierSlug, site.Replicas); oerr == nil {
					delta = newCost - oldCost
				}
				summary, _ := client.GetProjectPoints(projectID)
				fmt.Println(formatScaleLine(tier, replicas, newCost, delta, summary))
			}
		}

		updated, err := client.SetAppTier(projectID, site.ID, tier, replicas)
		if err != nil {
			fmt.Printf("❌ Failed to scale: %s\n", formatMarketplaceError(err))
			return
		}

		fmt.Printf("✅ '%s' scaled to tier %s · %d replica(s)\n", updated.Name, displayTier(updated.AppTierSlug), updated.Replicas)
	},
}

func init() {
	siteCmd.AddCommand(siteListCmd)
	siteCmd.AddCommand(siteCreateCmd)
	siteCmd.AddCommand(siteAddCmd) // hidden deprecated alias
	siteCmd.AddCommand(siteUseCmd)
	siteCmd.AddCommand(siteScaleCmd)
	rootCmd.AddCommand(siteCmd)

	siteCreateCmd.Flags().StringVar(&siteEnvKind, "env", "", "Environment kind of the new site: development (default), staging or production")
	siteAddCmd.Flags().StringVar(&siteEnvKind, "env", "", "Environment kind of the new site: development (default), staging or production")

	siteScaleCmd.Flags().StringVar(&siteScaleSite, "site", "", "Site name or slug to scale (defaults to the project's active or only site)")
	siteScaleCmd.Flags().StringVar(&siteScaleTier, "tier", "", "New app compute tier (e.g. a, b, c, d). Keeps the current tier when omitted.")
	siteScaleCmd.Flags().IntVar(&siteScaleReplicas, "replicas", 0, "New replica count (must be >= 1). Keeps the current count when omitted.")
}
