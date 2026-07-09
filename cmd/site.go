package cmd

import (
	"encoding/json"
	"fmt"

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
		if cfg.Token == "" {
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

		data, _ := readProjectConfig(".")
		var localCfg struct {
			SiteID string `json:"site_id"`
		}
		json.Unmarshal(data, &localCfg)

		fmt.Println("📌 Sites:")
		for _, s := range sites {
			marker := "  "
			if s.ID == localCfg.SiteID {
				marker = "▶ "
			}
			fmt.Printf("  %s%s  (slug: %s, status: %s, id: %s)\n", marker, s.Name, s.Slug, s.Status, s.ID)
		}
		fmt.Println("\nTo switch active site: ghayma site use <slug>")
	},
}

func runSiteCreate(cmd *cobra.Command, args []string) {
	cfg := config.Load()
	if cfg.Token == "" {
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

	client := api.NewClient(cfg)
	site, err := client.CreateSite(projectID, siteName)
	if err != nil {
		fmt.Printf("❌ Failed to create site: %v\n", err)
		return
	}

	fmt.Printf("✅ Site '%s' created (slug: %s, id: %s)\n", site.Name, site.Slug, site.ID)
	fmt.Printf("\nTo deploy to this site, switch to it first:\n")
	fmt.Printf("  ghayma site use %s\n", site.Slug)
	fmt.Printf("  ghayma deploy --prod\n")
}

var siteCreateCmd = &cobra.Command{
	Use:   "create [name]",
	Short: "Create a new site in the current project",
	Args:  cobra.MaximumNArgs(1),
	Run:   runSiteCreate,
}

// siteAddCmd is the deprecated alias; hidden from help output, prints a
// deprecation warning the first time per week it's invoked.
var siteAddCmd = &cobra.Command{
	Use:    "add [name]",
	Short:  "(deprecated) alias for 'site create'",
	Hidden: true,
	Args:   cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		maybeWarnDeprecated("site add", "site create", "v0.3.0")
		runSiteCreate(cmd, args)
	},
}

var siteUseCmd = &cobra.Command{
	Use:   "use <slug>",
	Short: "Switch the active site for the project",
	Args:  requireOneArg("slug", "site list"),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		slug := args[0]

		data, err := readProjectConfig(".")
		if err != nil {
			fmt.Println("❌ No project config found. Run 'ghayma init' first.")
			return
		}

		var projCfg ProjectConfig
		json.Unmarshal(data, &projCfg)

		client := api.NewClient(cfg)
		sites, err := client.ListSites(projCfg.ProjectID)
		if err != nil {
			fmt.Printf("❌ Failed to list sites: %v\n", err)
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

		projCfg.SiteID = matched.ID
		projCfg.SiteName = matched.Name
		projCfg.SiteSlug = matched.Slug

		// Update-in-place: write back to the same file we read, so a legacy
		// .espacetech.json project stays on .espacetech.json instead of silently
		// migrating to .ghayma.json (which would strand teammates on the old CLI).
		if err := writeProjectConfigUpdate(".", projCfg); err != nil {
			fmt.Printf("❌ Failed to update project config: %v\n", err)
			return
		}

		fmt.Printf("✅ Active site switched to '%s' (slug: %s)\n", matched.Name, matched.Slug)
		fmt.Println("   Run 'ghayma deploy --prod' to deploy to this site")
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
		if cfg.Token == "" {
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

	siteScaleCmd.Flags().StringVar(&siteScaleSite, "site", "", "Site name or slug to scale (defaults to the project's active or only site)")
	siteScaleCmd.Flags().StringVar(&siteScaleTier, "tier", "", "New app compute tier (e.g. a, b, c, d). Keeps the current tier when omitted.")
	siteScaleCmd.Flags().IntVar(&siteScaleReplicas, "replicas", 0, "New replica count (must be >= 1). Keeps the current count when omitted.")
}
