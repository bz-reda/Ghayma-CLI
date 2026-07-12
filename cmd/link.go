package cmd

import (
	"errors"
	"fmt"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
)

var linkCmd = &cobra.Command{
	Use:   "link [project-slug]",
	Short: "Link the current directory to an existing project",
	Long: `Link the current directory to a project that already exists on Ghayma.

Use this instead of 'init' when you clone a repository on a new machine:
init creates a brand-new project, while link connects to one you already own.
In a monorepo, run it from your app's subdirectory (or from the root and pick
the subdir) — link can attach to an existing site or create a new one.`,
	Args: cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		// In a monorepo root, write the config into the chosen app subdir so
		// deploy uploads the whole workspace and builds the right target.
		appSubdir := detectMonorepoAppSubdir()
		configDir := "."
		if appSubdir != "" {
			configDir = appSubdir
		}

		if _, err := findProjectConfig(configDir); err == nil {
			fmt.Println("⚠️  This directory is already linked. Delete the project config to re-link.")
			return
		}

		client := api.NewClient(cfg)

		projects, err := client.ListProjects()
		if err != nil {
			fmt.Printf("❌ Failed to list projects: %v\n", err)
			return
		}
		if len(projects) == 0 {
			fmt.Println("❌ You don't have any projects yet. Create one with: ghayma init")
			return
		}

		var project *api.Project

		if len(args) == 1 {
			target := args[0]
			for i, p := range projects {
				if p.Slug == target || p.Name == target || p.ID == target {
					project = &projects[i]
					break
				}
			}
			if project == nil {
				fmt.Printf("❌ No project found matching '%s'\n", target)
				fmt.Println("   Run 'ghayma link' without arguments to pick from a list.")
				return
			}
		} else {
			labels := make([]string, len(projects))
			for i, p := range projects {
				labels[i] = fmt.Sprintf("%s  (slug: %s, framework: %s)", p.Name, p.Slug, p.Framework)
			}
			sel := promptui.Select{
				Label: "Select a project to link",
				Items: labels,
				Size:  10,
			}
			idx, _, err := sel.Run()
			if err != nil {
				fmt.Println("❌ Cancelled")
				return
			}
			project = &projects[idx]
		}

		// Resolve or create the site under the chosen project, then write the
		// config into configDir. Shared with init's use-existing branch.
		if err := attachToExistingProject(client, project, configDir); err != nil {
			if errors.Is(err, errAttachCancelled) {
				fmt.Println("❌ Cancelled")
			} else {
				fmt.Printf("❌ %v\n", err)
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(linkCmd)
}
