package cmd

import (
	"errors"
	"fmt"
	"os"

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
the subdir) — link can attach to an existing site or create a new one.

At a workspace root — turbo.json, a pnpm-workspace.yaml with a packages: list,
or a package.json with "workspaces" — you can instead link the WHOLE project:
one manifest written to ./.ghayma.json listing every site and the directory
that builds it, so 'ghayma deploy' from the root can ask which site to deploy.`,
	Args: cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if !cfg.LoggedIn() {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		// Fetch first: an expired session or an empty account should be
		// reported before the user is walked through the monorepo prompt.
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

		// At a workspace root the user can link the WHOLE project — every
		// site in one manifest — instead of walking the app subdirectories one
		// run at a time. Choosing the per-app subdirectory continues below,
		// unchanged (2026-08-16).
		cwd, _ := os.Getwd()
		if findWorkspaceRoot(cwd) == cwd {
			handled, err := linkWorkspaceRoot(client, projects, args, cwd)
			if err != nil {
				if errors.Is(err, errAttachCancelled) {
					fmt.Println("❌ Cancelled")
				} else {
					fmt.Printf("❌ %v\n", err)
				}
				return
			}
			if handled {
				return
			}
		}

		// In a monorepo root, write the config into the chosen app subdir so
		// deploy uploads the whole workspace and builds the right target.
		appSubdir, ok := detectMonorepoAppSubdir()
		if !ok {
			return
		}
		configDir := "."
		if appSubdir != "" {
			configDir = appSubdir
		}

		if _, err := findProjectConfig(configDir); err == nil {
			fmt.Println("⚠️  This directory is already linked. Delete the project config to re-link.")
			return
		}

		project, err := selectLinkProject(projects, args)
		if err != nil {
			if errors.Is(err, errAttachCancelled) {
				fmt.Println("❌ Cancelled")
			} else {
				fmt.Printf("❌ %v\n", err)
			}
			return
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

// selectLinkProject resolves the project to link: the argument when given
// (slug, name or id), otherwise the interactive picker. Shared by the per-app
// and whole-workspace flows so both accept the same argument.
func selectLinkProject(projects []api.Project, args []string) (*api.Project, error) {
	if len(args) == 1 {
		target := args[0]
		for i, p := range projects {
			if p.Slug == target || p.Name == target || p.ID == target {
				return &projects[i], nil
			}
		}
		return nil, fmt.Errorf("no project found matching '%s'\n   Run 'ghayma link' without arguments to pick from a list.", target)
	}

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
		return nil, errAttachCancelled
	}
	return &projects[idx], nil
}

func init() {
	rootCmd.AddCommand(linkCmd)
}
