package cmd

import (
	"fmt"
	"strings"
	"time"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

// cronSiteFlag scopes runs/trigger name resolution to one site (name or slug).
var cronSiteFlag string

var cronCmd = &cobra.Command{
	Use:   "cron",
	Short: "Manage scheduled jobs (cron)",
	Long: `View and test per-site scheduled jobs.

Cron jobs are defined per site and managed as config-as-code: add a "crons"
array to .ghayma.json and they sync on 'ghayma deploy'. These commands read and
test the jobs the platform is running.

  ghayma cron list                 # all jobs for the current project
  ghayma cron runs <name>          # recent run history for a job
  ghayma cron trigger <name>       # fire a job now (respects overlap rule)`,
}

var cronListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the project's cron jobs",
	Run:   runCronList,
}

var cronRunsCmd = &cobra.Command{
	Use:   "runs <name>",
	Short: "Show recent run history for a cron job",
	Args:  requireOneArg("name", "cron list"),
	Run:   runCronRuns,
}

var cronTriggerCmd = &cobra.Command{
	Use:   "trigger <name>",
	Short: "Fire a cron job now (for testing)",
	Args:  requireOneArg("name", "cron list"),
	Run:   runCronTrigger,
}

func runCronList(cmd *cobra.Command, args []string) {
	cfg := config.Load()
	if cfg.Token == "" {
		fmt.Println("❌ Please login first: ghayma login")
		return
	}

	projectID, configSiteID, name, err := localConfig()
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		return
	}

	client := api.NewClient(cfg)
	scopeID, err := resolveCronSiteScope(client, projectID, cronSiteFlag, configSiteID)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		return
	}

	crons, err := client.ListCrons(projectID, scopeID)
	if err != nil {
		fmt.Printf("❌ Failed to list cron jobs: %v\n", err)
		return
	}

	if len(crons) == 0 {
		fmt.Printf("No cron jobs for %s.\n", name)
		fmt.Println("   Add a \"crons\" array to .ghayma.json and run 'ghayma deploy'.")
		return
	}

	fmt.Printf("⏰ Cron jobs for %s:\n\n", name)
	printCronTable(crons)
}

func runCronRuns(cmd *cobra.Command, args []string) {
	cfg := config.Load()
	if cfg.Token == "" {
		fmt.Println("❌ Please login first: ghayma login")
		return
	}

	client, projectID, job, err := resolveCronJob(cfg, args[0])
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		return
	}

	runs, err := client.ListCronRuns(projectID, job.ID)
	if err != nil {
		fmt.Printf("❌ Failed to load run history: %v\n", err)
		return
	}

	if len(runs) == 0 {
		fmt.Printf("No runs yet for '%s'. Trigger one with: ghayma cron trigger %s\n", job.Name, job.Name)
		return
	}

	fmt.Printf("⏰ Recent runs for '%s':\n\n", job.Name)
	printCronRunsTable(runs)
}

func runCronTrigger(cmd *cobra.Command, args []string) {
	cfg := config.Load()
	if cfg.Token == "" {
		fmt.Println("❌ Please login first: ghayma login")
		return
	}

	client, projectID, job, err := resolveCronJob(cfg, args[0])
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		return
	}

	if err := client.TriggerCron(projectID, job.ID); err != nil {
		fmt.Printf("❌ Failed to trigger '%s': %v\n", job.Name, err)
		return
	}

	fmt.Printf("✅ '%s' queued — check `ghayma cron runs %s`\n", job.Name, job.Name)
}

// resolveCronJob loads the project config, resolves the site scope, fetches the
// jobs, and picks the one named `name`. Names are unique per site, so a
// project-wide lookup can find the same name on multiple sites; that's the only
// ambiguous case and it asks for --site. Shared by `runs` and `trigger`.
func resolveCronJob(cfg *config.Config, name string) (*api.Client, string, *api.CronJob, error) {
	projectID, configSiteID, _, err := localConfig()
	if err != nil {
		return nil, "", nil, err
	}

	client := api.NewClient(cfg)
	scopeID, err := resolveCronSiteScope(client, projectID, cronSiteFlag, configSiteID)
	if err != nil {
		return nil, "", nil, err
	}

	crons, err := client.ListCrons(projectID, scopeID)
	if err != nil {
		return nil, "", nil, fmt.Errorf("failed to list cron jobs: %v", err)
	}

	matches := findCronsByName(crons, name)
	switch len(matches) {
	case 1:
		return client, projectID, &matches[0], nil
	case 0:
		return nil, "", nil, fmt.Errorf("cron job '%s' not found — run 'ghayma cron list'", name)
	default:
		// Only reachable project-wide (per-site names are unique). Name the
		// sites so the user can disambiguate with --site.
		return nil, "", nil, fmt.Errorf("multiple jobs named '%s' across sites (%s) — pass --site <name>",
			name, strings.Join(cronMatchSites(client, projectID, matches), ", "))
	}
}

// resolveCronSiteScope maps the --site flag (name or slug) to a site id for the
// list query, falling back to the project config's site_id. It returns "" for a
// project-wide view (no flag and no config site) rather than forcing a choice —
// name resolution disambiguates when it actually has to. A --site that matches
// no site errors.
func resolveCronSiteScope(client *api.Client, projectID, siteFlag, configSiteID string) (string, error) {
	if siteFlag == "" {
		return configSiteID, nil
	}
	sites, err := client.ListSites(projectID)
	if err != nil {
		return "", err
	}
	for _, s := range sites {
		if s.Slug == siteFlag || s.Name == siteFlag {
			return s.ID, nil
		}
	}
	return "", fmt.Errorf("site %q not found in this project — run 'ghayma site list'", siteFlag)
}

// findCronsByName returns every job whose name matches exactly (the server
// stores slugs). >1 match means the same name exists on multiple sites.
func findCronsByName(crons []api.CronJob, name string) []api.CronJob {
	var out []api.CronJob
	for _, c := range crons {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

// cronMatchSites maps the ambiguous matches' site ids back to slugs for the
// disambiguation hint, best-effort (falls back to the raw id if ListSites fails
// or a site is unknown).
func cronMatchSites(client *api.Client, projectID string, matches []api.CronJob) []string {
	slugByID := map[string]string{}
	if sites, err := client.ListSites(projectID); err == nil {
		for _, s := range sites {
			slugByID[s.ID] = s.Slug
		}
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if slug := slugByID[m.SiteID]; slug != "" {
			out = append(out, slug)
		} else {
			out = append(out, m.SiteID)
		}
	}
	return out
}

// printCronTable renders NAME / SCHEDULE (+tz) / PATH / ENABLED / NEXT RUN /
// LAST RUN, times shown relative to now.
func printCronTable(crons []api.CronJob) {
	fmt.Printf("   %-16s  %-26s  %-18s  %-8s  %-12s  %s\n", "NAME", "SCHEDULE", "PATH", "ENABLED", "NEXT RUN", "LAST RUN")
	for _, c := range crons {
		enabled := "yes"
		if !c.Enabled {
			enabled = "paused"
		}
		fmt.Printf("   %-16s  %-26s  %-18s  %-8s  %-12s  %s\n",
			c.Name,
			cronSchedule(c.Schedule, c.Timezone),
			cronPath(c),
			enabled,
			relTime(c.NextRunAt),
			lastRunLabel(c.LastRun),
		)
	}
}

// printCronRunsTable renders WHEN / STATUS / HTTP / DURATION / ERROR (error on
// one line).
func printCronRunsTable(runs []api.CronRun) {
	fmt.Printf("   %-14s  %-16s  %-6s  %-10s  %s\n", "WHEN", "STATUS", "HTTP", "DURATION", "ERROR")
	for _, r := range runs {
		http := "—"
		if r.HTTPStatus > 0 {
			http = fmt.Sprintf("%d", r.HTTPStatus)
		}
		fmt.Printf("   %-14s  %-16s  %-6s  %-10s  %s\n",
			relTime(r.StartedAt),
			r.Status,
			http,
			humanizeMS(r.DurationMS),
			truncateOneLine(r.Error, 60),
		)
	}
}

// cronSchedule renders the schedule cell as "<expr> (<tz>)", omitting the tz
// parens when the timezone is unset.
func cronSchedule(schedule, tz string) string {
	if tz == "" {
		return schedule
	}
	return fmt.Sprintf("%s (%s)", schedule, tz)
}

// cronPath renders an http job's target as "METHOD /path"; a container job (no
// path) shows its type instead.
func cronPath(c api.CronJob) string {
	if c.Path == "" {
		return c.Type
	}
	method := c.Method
	if method == "" {
		method = "GET"
	}
	return method + " " + c.Path
}

// lastRunLabel renders a job's last run as "<status> <when>", or "never" when
// the job has not run yet.
func lastRunLabel(r *api.CronRun) string {
	if r == nil {
		return "never"
	}
	return fmt.Sprintf("%s %s", r.Status, relTime(r.StartedAt))
}

// relTime renders a timestamp relative to now ("in 5m", "2h ago", "just now"),
// or "—" for a nil/never time.
func relTime(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return relLabel(time.Until(*t))
}

// relLabel turns a signed duration into a relative label: positive = future
// ("in 5m"), negative = past ("2h ago"). Kept pure so it is unit-tested without
// touching the clock.
func relLabel(d time.Duration) string {
	future := d >= 0
	if !future {
		d = -d
	}
	var mag string
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		mag = fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		mag = fmt.Sprintf("%dh", int(d.Hours()))
	default:
		mag = fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	if future {
		return "in " + mag
	}
	return mag + " ago"
}

// humanizeMS renders a millisecond duration as "340ms" / "1.2s", or "—" when
// unset (0).
func humanizeMS(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

// truncateOneLine collapses newlines to spaces and clips to max runes with an
// ellipsis, so an error never breaks the table across lines. Rune-based so a
// multi-byte character is never split.
func truncateOneLine(s string, max int) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	r := []rune(s)
	if max > 0 && len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}

func init() {
	cronRunsCmd.Flags().StringVar(&cronSiteFlag, "site", "", "Site name or slug to disambiguate when the job name exists on multiple sites")
	cronTriggerCmd.Flags().StringVar(&cronSiteFlag, "site", "", "Site name or slug to disambiguate when the job name exists on multiple sites")
	cronListCmd.Flags().StringVar(&cronSiteFlag, "site", "", "Only show jobs for this site (name or slug); defaults to the project config's site")

	cronCmd.AddCommand(cronListCmd)
	cronCmd.AddCommand(cronRunsCmd)
	cronCmd.AddCommand(cronTriggerCmd)
	rootCmd.AddCommand(cronCmd)
}
