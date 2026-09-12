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

// rollbackUnavailableMsg is shown when previous deployments exist but none of
// them can be restored — they fell out of the API's rollback window.
const rollbackUnavailableMsg = "No deployment is available for rollback (only the last 10 successful deployments of a site can be restored)."

// rollbackOptions returns the deployments the user may roll back to, newest
// first and without the one currently serving. Rows the API marks
// rollback_available=false are outside the rollback window; a row that lacks
// the field comes from an older API and stays listed. hasPrevious is false
// when there was never anything to roll back to.
func rollbackOptions(deployments []api.DeploymentInfo) (options []api.DeploymentInfo, hasPrevious bool) {
	var live []api.DeploymentInfo
	for _, d := range deployments {
		if d.Status == "live" && d.ImageTag != "" {
			live = append(live, d)
		}
	}
	if len(live) < 2 {
		return nil, false
	}
	for _, d := range live[1:] {
		if d.RollbackAvailable == nil || *d.RollbackAvailable {
			options = append(options, d)
		}
	}
	return options, true
}

// rollbackErrorText prefers the API's own message: the rollback endpoint
// answers 409 with a sentence written for the customer.
func rollbackErrorText(err error) string {
	var apiErr *api.APIError
	if errors.As(err, &apiErr) && apiErr.Message != "" {
		return apiErr.Message
	}
	return fmt.Sprintf("Rollback failed: %v", err)
}

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

		// Rollback restores a previous deployment of a site; there are none.
		if localConfigIsSiteLess() {
			failNoSite()
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

		options, hasPrevious := rollbackOptions(deployments)
		if !hasPrevious {
			fmt.Println("❌ No previous deployments to rollback to.")
			return
		}
		if len(options) == 0 {
			fmt.Println("❌ " + rollbackUnavailableMsg)
			os.Exit(1)
		}

		fmt.Printf("📋 Recent deployments for %s:\n\n", projectCfg.Name)
		for i, d := range options {
			fmt.Printf("   [%d] %s — %s (%s)\n", i+1, d.CreatedAt, d.ImageTag, d.CommitMessage)
		}

		fmt.Print("\nSelect deployment to rollback to (number): ")
		var choice int
		fmt.Scan(&choice)

		if choice < 1 || choice > len(options) {
			fmt.Println("❌ Invalid selection")
			return
		}

		target := options[choice-1]
		fmt.Printf("🔄 Rolling back to %s...\n", target.ImageTag)

		result, err := client.Rollback(target.ID)
		if err != nil {
			fmt.Printf("❌ %s\n", rollbackErrorText(err))
			os.Exit(1)
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
