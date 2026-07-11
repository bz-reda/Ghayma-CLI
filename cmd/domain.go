package cmd

import (
	"encoding/json"
	"fmt"

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
	if cfg.Token == "" {
		fmt.Println("❌ Please login first: ghayma login")
		return
	}

	data, err := readProjectConfig(".")
	if err != nil {
		fmt.Println("❌ No project config found. Run 'ghayma init' first.")
		return
	}

	var projectCfg ProjectConfig
	json.Unmarshal(data, &projectCfg)

	client := api.NewClient(cfg)
	domain := args[0]

	err = client.AddDomain(projectCfg.ProjectID, projectCfg.SiteID, domain)
	if err != nil {
		fmt.Printf("❌ Failed to add domain: %v\n", err)
		return
	}

	fmt.Printf("✅ Domain '%s' added to %s\n", domain, projectCfg.Name)
	fmt.Println("\n📋 Next steps:")
	fmt.Printf("   1. Add an A record in your DNS: %s → 65.109.68.181\n", domain)
	fmt.Printf("   2. Redeploy: ghayma deploy --prod\n")
	fmt.Printf("   3. SSL will be provisioned automatically\n")
}

var domainCreateCmd = &cobra.Command{
	Use:   "create [domain]",
	Short: "Add a custom domain to the current project",
	Args:  requireOneArg("domain", ""),
	Run:   runDomainCreate,
}

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
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		data, err := readProjectConfig(".")
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
	if cfg.Token == "" {
		fmt.Println("❌ Please login first: ghayma login")
		return
	}

	data, err := readProjectConfig(".")
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
	domainDeleteCmd.Flags().BoolVarP(&domainDeleteYes, "yes", "y", false, "skip the confirmation prompt")
	domainRemoveCmd.Flags().BoolVarP(&domainDeleteYes, "yes", "y", false, "skip the confirmation prompt")

	domainCmd.AddCommand(domainCreateCmd)
	domainCmd.AddCommand(domainAddCmd) // hidden deprecated alias
	domainCmd.AddCommand(domainListCmd)
	domainCmd.AddCommand(domainDeleteCmd)
	domainCmd.AddCommand(domainRemoveCmd) // hidden deprecated alias
	rootCmd.AddCommand(domainCmd)
}
