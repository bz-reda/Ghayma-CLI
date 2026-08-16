package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

var domainCmd = &cobra.Command{
	Use:   "domain",
	Short: "Manage project domains",
}

func runDomainCreate(cmd *cobra.Command, args []string) {
	cfg := config.Load()
	if !cfg.LoggedIn() {
		fmt.Println("❌ Please login first: ghayma login")
		return
	}

	// A domain is attached to ONE site, so this resolves the same way deploy
	// does: the app directory pins it, a workspace root asks or takes --site.
	cwd, _ := os.Getwd()
	ctx, err := resolveSiteContext(cwd, domainSite, "attach the domain to")
	if err != nil {
		switch {
		case errors.Is(err, errAttachCancelled):
			fmt.Println("❌ Cancelled")
		case errors.Is(err, errNoProjectConfig):
			fmt.Println("❌ No project config found. Run 'ghayma init' first.")
		default:
			fmt.Printf("❌ %v\n", err)
		}
		return
	}

	client := api.NewClient(cfg)
	domain := args[0]

	if err := client.AddDomain(ctx.ProjectID, ctx.Site.SiteID, domain); err != nil {
		fmt.Printf("❌ Failed to add domain: %v\n", err)
		return
	}

	fmt.Printf("✅ Domain '%s' added to %s\n", domain, ctx.ProjectName)
	fmt.Println("\n📋 Next steps:")
	fmt.Printf("   1. Add an A record in your DNS: %s → 65.109.68.181\n", domain)
	fmt.Printf("   2. Redeploy: ghayma deploy --prod\n")
	fmt.Printf("   3. SSL will be provisioned automatically\n")
}

var domainCreateCmd = &cobra.Command{
	Use:   "create [domain]",
	Short: "Add a custom domain to the current project",
	Long: `Add a custom domain.

The domain is attached to ONE site. Inside an app directory that site is the one
the directory is linked to. At the root of a workspace linked with 'ghayma link'
(whole project) this asks which site to attach it to, or takes --site <slug> —
required when the shell is not interactive.`,
	Args: requireOneArg("domain", ""),
	Run:  runDomainCreate,
}

// domainSite is the site to attach the domain to, for the workspace-root case.
var domainSite string

// domainAddCmd is the deprecated alias; hidden.
var domainAddCmd = &cobra.Command{
	Use:    "add [domain]",
	Short:  "(deprecated) alias for 'domain create'",
	Hidden: true,
	Args:   requireOneArg("domain", ""),
	Run: func(cmd *cobra.Command, args []string) {
		maybeWarnDeprecated("domain add", "domain create", "a future release")
		runDomainCreate(cmd, args)
	},
}

var domainListCmd = &cobra.Command{
	Use:   "list",
	Short: "List domains for the current project",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if !cfg.LoggedIn() {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		// Domains are project-scoped, so the config anywhere above works.
		data, err := readProjectConfigUp(".")
		if err != nil {
			fmt.Println("❌ No project config found. Run 'ghayma init' first.")
			return
		}

		var projectCfg ProjectConfig
		json.Unmarshal(data, &projectCfg)

		client := api.NewClient(cfg)
		domains, err := client.ListDomains(projectCfg.ProjectID)
		if err != nil {
			fmt.Printf("❌ Failed to list domains: %v\n", err)
			return
		}

		if len(domains) == 0 {
			fmt.Println("No domains configured.")
			return
		}

		fmt.Printf("🌐 Domains for %s:\n\n", projectCfg.Name)
		for _, d := range domains {
			fmt.Printf("   https://%s\n", d)
		}
	},
}

var domainDeleteYes bool

func runDomainDelete(cmd *cobra.Command, args []string) {
	cfg := config.Load()
	if !cfg.LoggedIn() {
		fmt.Println("❌ Please login first: ghayma login")
		return
	}

	data, err := readProjectConfigUp(".")
	if err != nil {
		fmt.Println("❌ No project config found. Run 'ghayma init' first.")
		return
	}

	var projectCfg struct {
		ProjectID string `json:"project_id"`
	}
	json.Unmarshal(data, &projectCfg)

	if !domainDeleteYes {
		fmt.Printf("⚠️  Remove '%s'? This breaks DNS routing for this hostname. (y/N): ", args[0])
		var confirm string
		fmt.Scanln(&confirm)
		if confirm != "y" && confirm != "Y" {
			fmt.Println("❌ Cancelled.")
			return
		}
	}

	client := api.NewClient(cfg)
	err = client.RemoveDomain(projectCfg.ProjectID, args[0])
	if err != nil {
		fmt.Printf("❌ Failed to remove domain: %v\n", err)
		return
	}

	fmt.Printf("✅ Domain '%s' removed. Redeploy to apply changes.\n", args[0])
}

var domainDeleteCmd = &cobra.Command{
	Use:   "delete [domain]",
	Short: "Remove a domain from the current project",
	Args:  requireOneArg("domain", "domain list"),
	Run:   runDomainDelete,
}

// domainRemoveCmd is the deprecated alias; hidden.
var domainRemoveCmd = &cobra.Command{
	Use:    "remove [domain]",
	Short:  "(deprecated) alias for 'domain delete'",
	Hidden: true,
	Args:   requireOneArg("domain", "domain list"),
	Run: func(cmd *cobra.Command, args []string) {
		maybeWarnDeprecated("domain remove", "domain delete", "a future release")
		runDomainDelete(cmd, args)
	},
}

func init() {
	const siteFlagUsage = "Site name or slug to attach the domain to (required at a workspace root when the shell is not interactive)"
	domainCreateCmd.Flags().StringVar(&domainSite, "site", "", siteFlagUsage)
	domainAddCmd.Flags().StringVar(&domainSite, "site", "", siteFlagUsage)

	domainDeleteCmd.Flags().BoolVarP(&domainDeleteYes, "yes", "y", false, "skip the confirmation prompt")
	domainRemoveCmd.Flags().BoolVarP(&domainDeleteYes, "yes", "y", false, "skip the confirmation prompt")

	domainCmd.AddCommand(domainCreateCmd)
	domainCmd.AddCommand(domainAddCmd) // hidden deprecated alias
	domainCmd.AddCommand(domainListCmd)
	domainCmd.AddCommand(domainDeleteCmd)
	domainCmd.AddCommand(domainRemoveCmd) // hidden deprecated alias
	rootCmd.AddCommand(domainCmd)
}
