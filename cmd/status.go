package cmd

import (
	"fmt"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "List your projects",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if !cfg.LoggedIn() {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		projects, err := client.ListProjects()
		if err != nil {
			fmt.Printf("❌ Failed to list projects: %v\n", err)
			return
		}

		if len(projects) == 0 {
			fmt.Println("No projects yet. Run 'ghayma init' to create one.")
			return
		}

		fmt.Println("📋 Your projects:")
		fmt.Println()
		for _, p := range projects {
			fmt.Printf("  %-20s %-15s %s\n", p.Name, p.Framework, p.Slug)
		}
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
