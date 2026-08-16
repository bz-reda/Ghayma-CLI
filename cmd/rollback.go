package cmd

import (
	"encoding/json"
	"fmt"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

var rollbackCmd = &cobra.Command{
	Use:   "rollback",
	Short: "Rollback to a previous deployment",
	Run: func(cmd *cobra.Command, args []string) {
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
			Name      string `json:"name"`
		}
		json.Unmarshal(data, &projectCfg)

		client := api.NewClient(cfg)

		// List recent deployments
		deployments, err := client.ListDeployments(projectCfg.ProjectID)
		if err != nil {
			fmt.Printf("❌ Failed to list deployments: %v\n", err)
			return
		}

		// Filter only successful deployments with images
		var candidates []api.DeploymentInfo
		for _, d := range deployments {
			if d.Status == "live" && d.ImageTag != "" {
				candidates = append(candidates, d)
			}
		}

		if len(candidates) < 2 {
			fmt.Println("❌ No previous deployments to rollback to.")
			return
		}

		// Show options (skip the current/latest one)
		fmt.Printf("📋 Recent deployments for %s:\n\n", projectCfg.Name)
		for i, d := range candidates[1:] {
			fmt.Printf("   [%d] %s — %s (%s)\n", i+1, d.CreatedAt, d.ImageTag, d.CommitMessage)
		}

		fmt.Print("\nSelect deployment to rollback to (number): ")
		var choice int
		fmt.Scan(&choice)

		if choice < 1 || choice > len(candidates)-1 {
			fmt.Println("❌ Invalid selection")
			return
		}

		target := candidates[choice]
		fmt.Printf("🔄 Rolling back to %s...\n", target.ImageTag)

		result, err := client.Rollback(target.ID)
		if err != nil {
			fmt.Printf("❌ Rollback failed: %v\n", err)
			return
		}

		fmt.Println("✅ Rollback successful!")
		if len(result.Domains) > 0 {
			fmt.Println("🌐 Your app is live at:")
			for _, d := range result.Domains {
				fmt.Printf("   https://%s\n", d)
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(rollbackCmd)
}