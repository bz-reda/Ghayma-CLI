package cmd

import (
	"encoding/json"
	"fmt"
	"strconv"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

var logLines int

var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "View app runtime logs",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if !cfg.LoggedIn() {
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
			Slug      string `json:"slug"`
			Name      string `json:"name"`
		}
		json.Unmarshal(data, &projectCfg)

		client := api.NewClient(cfg)

		fmt.Printf("📋 Logs for %s (last %d lines):\n\n", projectCfg.Name, logLines)

		logs, err := client.GetAppLogs(projectCfg.ProjectID, logLines)
		if err != nil {
			fmt.Printf("❌ Failed to get logs: %v\n", err)
			return
		}

		if logs == "" {
			fmt.Println("(no log entries — the app may be idle, sleeping, or not deployed yet)")
			return
		}
		fmt.Println(logs)
	},
}

func init() {
	logsCmd.Flags().IntVarP(&logLines, "lines", "n", 100, "Number of lines to show")
	_ = strconv.Itoa(0) // suppress unused import
	rootCmd.AddCommand(logsCmd)
}