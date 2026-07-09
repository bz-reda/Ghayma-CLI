package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

var pointsCmd = &cobra.Command{
	Use:   "points",
	Short: "Show this project's points meter and per-resource breakdown",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		projectID := ""
		data, err := readProjectConfig(".")
		if err == nil {
			var projectCfg ProjectConfig
			json.Unmarshal(data, &projectCfg)
			projectID = projectCfg.ProjectID
		}
		if projectID == "" {
			fmt.Println("❌ No project config found. Run 'ghayma init' first or run this command from a project directory.")
			return
		}

		client := api.NewClient(cfg)
		summary, err := client.GetProjectPoints(projectID)
		if err != nil {
			fmt.Printf("❌ Failed to fetch points: %v\n", err)
			return
		}

		fmt.Print(renderPointsSummary(summary))
	},
}

// renderPointsSummary formats a project's points meter and per-resource
// breakdown. It is pure — every number comes from the summary at runtime, so
// no pricing is hardcoded here. PAYG projects hide the meter and show a
// pay-as-you-go note; over-budget and not-yet-enforced states each add one
// note line. Breakdown rows with no tier detail are marked untiered.
func renderPointsSummary(s *api.ProjectPointsSummary) string {
	var b strings.Builder

	if s.PAYG {
		b.WriteString("Pay-as-you-go — usage billed per resource (no points budget)\n")
	} else {
		fmt.Fprintf(&b, "Points: %d/%d used · %d remaining\n", s.Used, s.Budget, s.Remaining)
		if s.OverBy > 0 {
			fmt.Fprintf(&b, "⚠️  over budget by %d\n", s.OverBy)
		}
		if !s.Enforced {
			b.WriteString("(enforcement not active yet)\n")
		}
	}

	if len(s.Breakdown) > 0 {
		b.WriteString("\n")
		w := tabwriter.NewWriter(&b, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "KIND\tNAME\tDETAIL\tPTS")
		for _, r := range s.Breakdown {
			detail := r.Detail
			if strings.TrimSpace(detail) == "" {
				detail = "untiered (pending next deploy)"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\n", r.Kind, r.Name, detail, r.Points)
		}
		w.Flush()
	}

	return b.String()
}

func init() {
	rootCmd.AddCommand(pointsCmd)
}
