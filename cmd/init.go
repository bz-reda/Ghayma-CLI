package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
)

type ProjectConfig struct {
	ProjectID     string `json:"project_id"`
	Name          string `json:"name"`
	Slug          string `json:"slug"`
	Framework     string `json:"framework"`
	SiteID        string `json:"site_id,omitempty"`
	SiteName      string `json:"site_name,omitempty"`
	SiteSlug      string `json:"site_slug,omitempty"`
	RootDirectory string `json:"root_directory,omitempty"`
	// Build config: optional overrides recorded from init; the server
	// still auto-detects when these are empty.
	BuildCommand    string `json:"build_command,omitempty"`
	InstallCommand  string `json:"install_command,omitempty"`
	StartCommand    string `json:"start_command,omitempty"`
	OutputDirectory string `json:"output_directory,omitempty"`
	Port            int    `json:"port,omitempty"`
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a new project in the current directory",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		// Check if already initialized
		if _, err := findProjectConfig("."); err == nil {
			fmt.Println("⚠️  Project already initialized. Delete the project config to re-initialize.")
			return
		}

		// If CWD is a monorepo root, nudge the user to init from their app
		// subdir instead — or ask for the app subdir and write the config
		// inside it so the deploy flow sends the correct root_directory.
		appSubdir := detectMonorepoAppSubdir()
		if appSubdir != "" {
			if existing, err := findProjectConfig(appSubdir); err == nil {
				fmt.Printf("⚠️  %s already exists. Delete it to re-initialize.\n", existing)
				return
			}
		}

		// Project name
		namePrompt := promptui.Prompt{Label: "Project name"}
		name, _ := namePrompt.Run()

		// Framework
		frameworkSelect := promptui.Select{
			Label: "Framework",
			Items: []string{"nextjs"},
		}
		_, framework, _ := frameworkSelect.Run()

		client := api.NewClient(cfg)

		// Resolve the billing account for the new project. The API
		// requires billing_account_id for billable plans (init creates
		// with the default "hobby" plan, which is billable). Auto-select
		// when there's exactly one eligible account, prompt when there
		// are several, honor --billing-account for non-interactive use.
		billingAccountFlag, _ := cmd.Flags().GetString("billing-account")
		billingAccountID, err := resolveBillingAccount(client, billingAccountFlag)
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		project, err := client.CreateProject(name, framework, billingAccountID)
		if err != nil {
			fmt.Printf("❌ Failed to create project: %v\n", err)
			return
		}

		// Site name — default "main" for single-site projects
		sitePrompt := promptui.Prompt{
			Label:   "Site name (e.g. frontend, admin — leave empty for 'main')",
			Default: "",
		}
		siteName, _ := sitePrompt.Run()
		if siteName == "" {
			siteName = "main"
		}

		// The "main" site is auto-created by the backend on project creation.
		// For any other name, create a new site via the API.
		var siteID, siteSlug string
		if siteName == "main" {
			// Fetch the auto-created main site to get its ID
			sites, err := client.ListSites(project.ID)
			if err == nil {
				for _, s := range sites {
					if s.Slug == "main" {
						siteID = s.ID
						siteSlug = s.Slug
						break
					}
				}
			}
		} else {
			site, err := client.CreateSite(project.ID, siteName)
			if err != nil {
				fmt.Printf("⚠️  Failed to create site '%s': %v\n", siteName, err)
				fmt.Println("   You can add sites later with: ghayma site add <name>")
			} else {
				siteID = site.ID
				siteSlug = site.Slug
				fmt.Printf("📌 Site '%s' created (slug: %s)\n", site.Name, site.Slug)
			}
		}

		// Domain
		domainPrompt := promptui.Prompt{
			Label:   "Domain (e.g., mysite.com, leave empty to skip)",
			Default: "",
		}
		domain, _ := domainPrompt.Run()

		if domain != "" {
			if err := client.AddDomain(project.ID, siteID, domain); err != nil {
				fmt.Printf("⚠️  Failed to add domain: %v\n", err)
			} else {
				fmt.Printf("🌐 Domain %s added\n", domain)
			}
		}

		// Save project config — in the app subdir for monorepos, in CWD otherwise.
		projectCfg := ProjectConfig{
			ProjectID: project.ID,
			Name:      project.Name,
			Slug:      project.Slug,
			Framework: project.Framework,
			SiteID:    siteID,
			SiteName:  siteName,
			SiteSlug:  siteSlug,
		}
		configPath := projectConfigWritePath(".")
		if appSubdir != "" {
			configPath = projectConfigWritePath(appSubdir)
		}
		data, _ := json.MarshalIndent(projectCfg, "", "  ")
		if err := os.WriteFile(configPath, data, 0644); err != nil {
			fmt.Printf("❌ Failed to write config: %v\n", err)
			return
		}

		fmt.Printf("✅ Project '%s' created (slug: %s)\n", project.Name, project.Slug)
		if siteName != "main" {
			fmt.Printf("   Site: %s (slug: %s)\n", siteName, siteSlug)
		}
		fmt.Printf("📁 Config saved to %s\n", configPath)
		fmt.Println("\nNext: run 'ghayma deploy --prod' to deploy")
	},
}

// detectMonorepoAppSubdir checks whether CWD is a monorepo root (turbo.json
// or pnpm-workspace.yaml). If so, it prompts the user for the app subdir to
// initialise inside (e.g. "apps/web") and returns it. Returns "" when CWD is
// not a monorepo root or the user chooses to init at the current directory.
func detectMonorepoAppSubdir() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	_, turboErr := os.Stat(filepath.Join(cwd, "turbo.json"))
	_, pnpmErr := os.Stat(filepath.Join(cwd, "pnpm-workspace.yaml"))
	if turboErr != nil && pnpmErr != nil {
		return ""
	}

	fmt.Println("📦 Monorepo root detected (turbo.json / pnpm-workspace.yaml).")
	fmt.Println("   The app config should live in your app's subdirectory so the deploy")
	fmt.Println("   uploads the whole workspace and builds the right target.")

	prompt := promptui.Prompt{
		Label:   "App subdirectory (e.g. apps/web; leave empty to init at root)",
		Default: "apps/web",
	}
	result, err := prompt.Run()
	if err != nil {
		return ""
	}
	result = strings.TrimSpace(strings.Trim(result, "/"))
	if result == "" {
		return ""
	}
	if _, err := os.Stat(filepath.Join(cwd, result)); err != nil {
		fmt.Printf("⚠️  %s does not exist in this repo — aborting.\n", result)
		os.Exit(1)
	}
	return result
}

// resolveBillingAccount decides which billing account backs the new
// project. With --billing-account it uses that id as-is (non-interactive
// / CI path — no list call, no prompt). Otherwise it lists the caller's
// accounts, filters to eligible ones (active + owner/admin), auto-selects
// a single one, or prompts to choose among several. Returns an actionable
// error when the user has no eligible account.
func resolveBillingAccount(client *api.Client, flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}

	accounts, err := client.ListBillingAccounts()
	if err != nil {
		return "", fmt.Errorf("could not list billing accounts: %w", err)
	}
	eligible := api.EligibleBillingAccounts(accounts)

	switch len(eligible) {
	case 0:
		return "", fmt.Errorf("no eligible billing account found.\n" +
			"   A billing account is required to create a project.\n" +
			"   Create one at dashboard.ghayma.dev/settings/billing, then re-run 'ghayma init'\n" +
			"   (or pass --billing-account <id> if you already have one).")
	case 1:
		a := eligible[0]
		fmt.Printf("💳 Using billing account: %s%s\n", a.Name, personalSuffix(a))
		return a.ID, nil
	default:
		labels := make([]string, len(eligible))
		for i, a := range eligible {
			labels[i] = fmt.Sprintf("%s%s  (role: %s)", a.Name, personalSuffix(a), a.Role)
		}
		sel := promptui.Select{
			Label: "Select a billing account for this project",
			Items: labels,
			Size:  10,
		}
		idx, _, err := sel.Run()
		if err != nil {
			return "", fmt.Errorf("cancelled")
		}
		return eligible[idx].ID, nil
	}
}

func personalSuffix(a api.BillingAccount) string {
	if a.IsPersonal {
		return " (personal)"
	}
	return ""
}

func init() {
	initCmd.Flags().String("billing-account", "", "Billing account ID for the new project (skips the interactive picker; for non-interactive/CI use)")
	rootCmd.AddCommand(initCmd)
}
