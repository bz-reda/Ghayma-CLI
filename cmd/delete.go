package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

var deleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete the current project and all its resources",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if !cfg.LoggedIn() {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		configPath, err := findProjectConfig(".")
		if err != nil {
			fmt.Println("❌ No project config found. Run 'ghayma init' first.")
			return
		}
		data, err := os.ReadFile(configPath)
		if err != nil {
			fmt.Println("❌ No project config found. Run 'ghayma init' first.")
			return
		}

		var projectCfg struct {
			ProjectID string `json:"project_id"`
			Name      string `json:"name"`
		}
		json.Unmarshal(data, &projectCfg)

		fmt.Printf("⚠️  This will permanently delete '%s' and all its deployments, domains, and resources.\n", projectCfg.Name)
		fmt.Printf("Type the project name to confirm: ")

		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		if input != projectCfg.Name {
			fmt.Println("❌ Name doesn't match. Deletion cancelled.")
			return
		}

		client := api.NewClient(cfg)
		err = client.DeleteProject(projectCfg.ProjectID)
		if err != nil {
			fmt.Printf("❌ Failed to delete: %v\n", err)
			return
		}

		os.Remove(configPath)
		fmt.Printf("✅ Project '%s' deleted successfully.\n", projectCfg.Name)
	},
}

func init() {
	rootCmd.AddCommand(deleteCmd)
}