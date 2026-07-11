package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

var envCmd = &cobra.Command{
	Use:   "env",
	Short: "Manage environment variables",
}

// localConfig reads the project config and returns projectID + siteID.
func localConfig() (projectID, siteID, name string, err error) {
	data, readErr := readProjectConfig(".")
	if readErr != nil {
		return "", "", "", fmt.Errorf("no project config found — run 'ghayma init' first")
	}
	var cfg struct {
		ProjectID string `json:"project_id"`
		Name      string `json:"name"`
		SiteID    string `json:"site_id"`
	}
	json.Unmarshal(data, &cfg)
	return cfg.ProjectID, cfg.SiteID, cfg.Name, nil
}

// getEnvVarsSnapshot fetches env vars with build-time markers.
func getEnvVarsSnapshot(client *api.Client, projectID, siteID string) (*api.EnvVarsSnapshot, error) {
	if siteID != "" {
		return client.GetEnvVarsSnapshotBySite(projectID, siteID)
	}
	return client.GetEnvVarsSnapshot(projectID)
}

// setEnvVarsWithBuildTime saves env vars with the build-time key list.
func setEnvVarsWithBuildTime(client *api.Client, projectID, siteID string, vars map[string]string, buildTimeKeys []string, force bool) error {
	if siteID != "" {
		return client.SetEnvVarsBySiteWithBuildTime(projectID, siteID, vars, buildTimeKeys, force)
	}
	return client.SetEnvVarsWithBuildTime(projectID, vars, buildTimeKeys, force)
}

// sortedKeys returns slice keys sorted; used for deterministic output.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var (
	envSetBuildTime bool
	envSetForce     bool
)

var envSetCmd = &cobra.Command{
	Use:   "set [KEY=VALUE...]",
	Short: "Set environment variables",
	Long: `Set one or more environment variables.

By default, variables are runtime-only — they are injected into the running
container but NOT available during the Docker build. For frameworks that
inline env vars into the client bundle at build time (Next.js NEXT_PUBLIC_*,
Vite VITE_*, CRA REACT_APP_*, Expo EXPO_PUBLIC_*), pass --build-time.

Build-time values are embedded in image layers and visible to anyone with
registry pull access. The server refuses to mark likely-secret keys
(*_SECRET, *_TOKEN, *_PASSWORD, PRIVATE_*, CREDENTIAL*) as build-time;
pass --force to override.`,
	Args: requireAtLeastOneArg("KEY=VALUE", ""),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		projectID, siteID, _, err := localConfig()
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		client := api.NewClient(cfg)

		snap, err := getEnvVarsSnapshot(client, projectID, siteID)
		if err != nil {
			snap = &api.EnvVarsSnapshot{Values: make(map[string]string)}
		}

		// Merge new values; update the build-time set atomically.
		btSet := make(map[string]bool, len(snap.BuildTimeKeys))
		for _, k := range snap.BuildTimeKeys {
			btSet[k] = true
		}

		var changed []string
		for _, arg := range args {
			parts := strings.SplitN(arg, "=", 2)
			if len(parts) != 2 {
				fmt.Printf("❌ Invalid format: %s (use KEY=VALUE)\n", arg)
				return
			}
			snap.Values[parts[0]] = parts[1]
			changed = append(changed, parts[0])
			if envSetBuildTime {
				btSet[parts[0]] = true
			}
		}

		var btKeys []string
		for k := range btSet {
			btKeys = append(btKeys, k)
		}
		sort.Strings(btKeys)

		if err := setEnvVarsWithBuildTime(client, projectID, siteID, snap.Values, btKeys, envSetForce); err != nil {
			fmt.Printf("❌ Failed to set env vars: %v\n", err)
			return
		}

		fmt.Println("✅ Environment variables updated:")
		for _, k := range changed {
			badge := ""
			if btSet[k] {
				badge = "  [build-time]"
			}
			fmt.Printf("   %s=%s%s\n", k, snap.Values[k], badge)
		}
		if envSetBuildTime {
			fmt.Println("\n⚠️  Build-time variables are embedded in image layers and visible")
			fmt.Println("   to anyone with registry pull access. Do not use for secrets.")
		}
		fmt.Println("\n🔄 Redeploy to apply: ghayma deploy --prod")
	},
}

var envListCmd = &cobra.Command{
	Use:   "list",
	Short: "List environment variables",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		projectID, siteID, name, err := localConfig()
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		client := api.NewClient(cfg)
		snap, err := getEnvVarsSnapshot(client, projectID, siteID)
		if err != nil {
			fmt.Printf("❌ Failed to get env vars: %v\n", err)
			return
		}

		if len(snap.Values) == 0 {
			fmt.Println("No environment variables set.")
			return
		}

		fmt.Printf("🔧 Environment variables for %s:\n\n", name)
		for _, k := range sortedKeys(snap.Values) {
			badge := ""
			if snap.IsBuildTime(k) {
				badge = "  [build-time]"
			}
			fmt.Printf("   %s=%s%s\n", k, snap.Values[k], badge)
		}
	},
}

func runEnvDelete(cmd *cobra.Command, args []string) {
	cfg := config.Load()
	if cfg.Token == "" {
		fmt.Println("❌ Please login first: ghayma login")
		return
	}

	projectID, siteID, _, err := localConfig()
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		return
	}

	client := api.NewClient(cfg)
	snap, err := getEnvVarsSnapshot(client, projectID, siteID)
	if err != nil {
		fmt.Printf("❌ Failed to get env vars: %v\n", err)
		return
	}

	btSet := make(map[string]bool, len(snap.BuildTimeKeys))
	for _, k := range snap.BuildTimeKeys {
		btSet[k] = true
	}

	// Split requested keys into present vs missing so the one-line diff
	// reflects what will actually change on the server. Missing keys
	// are a no-op but worth noting in case the user made a typo.
	var willRemove, notFound []string
	for _, key := range args {
		if _, present := snap.Values[key]; present {
			willRemove = append(willRemove, key)
		} else {
			notFound = append(notFound, key)
		}
	}
	sort.Strings(willRemove)
	sort.Strings(notFound)

	if len(willRemove) == 0 {
		fmt.Printf("Nothing to remove — none of [%s] are currently set.\n", strings.Join(args, ", "))
		return
	}
	fmt.Printf("Removing %d env var(s): %s\n", len(willRemove), strings.Join(willRemove, ", "))
	if len(notFound) > 0 {
		fmt.Printf("Skipped %d unknown key(s): %s\n", len(notFound), strings.Join(notFound, ", "))
	}

	for _, key := range willRemove {
		delete(snap.Values, key)
		delete(btSet, key)
	}

	var btKeys []string
	for k := range btSet {
		btKeys = append(btKeys, k)
	}
	sort.Strings(btKeys)

	if err := setEnvVarsWithBuildTime(client, projectID, siteID, snap.Values, btKeys, false); err != nil {
		fmt.Printf("❌ Failed to update env vars: %v\n", err)
		return
	}

	fmt.Println("✅ Environment variables updated")
	fmt.Println("🔄 Redeploy to apply: ghayma deploy --prod")
}

var envDeleteCmd = &cobra.Command{
	Use:   "delete [KEY...]",
	Short: "Delete environment variables",
	Args:  requireAtLeastOneArg("KEY", "env list"),
	Run:   runEnvDelete,
}

// envRemoveCmd is the deprecated alias; hidden.
var envRemoveCmd = &cobra.Command{
	Use:    "remove [KEY...]",
	Short:  "(deprecated) alias for 'env delete'",
	Hidden: true,
	Args:   requireAtLeastOneArg("KEY", "env list"),
	Run: func(cmd *cobra.Command, args []string) {
		maybeWarnDeprecated("env remove", "env delete", "a future release")
		runEnvDelete(cmd, args)
	},
}

var (
	envImportBuildTime    bool
	envImportDryRun       bool
	envImportSkipExisting bool
	envImportForce        bool
)

var envImportCmd = &cobra.Command{
	Use:   "import <file>",
	Short: "Import environment variables from a dotenv file",
	Long: `Bulk import environment variables from a .env-style file.

Supports standard dotenv syntax: KEY=VALUE, quoted values ("...", '...'),
# comments, blank lines, and an optional leading 'export '.

By default, existing vars are overwritten with a printed diff. Use
--skip-existing to keep existing values untouched.`,
	Args: requireOneArg("file", ""),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		path := args[0]
		parsed, err := parseDotenv(path)
		if err != nil {
			fmt.Printf("❌ Failed to parse %s: %v\n", path, err)
			return
		}
		if len(parsed) == 0 {
			fmt.Printf("⚠️  No variables found in %s\n", path)
			return
		}

		projectID, siteID, name, err := localConfig()
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		client := api.NewClient(cfg)
		snap, err := getEnvVarsSnapshot(client, projectID, siteID)
		if err != nil {
			snap = &api.EnvVarsSnapshot{Values: make(map[string]string)}
		}

		btSet := make(map[string]bool, len(snap.BuildTimeKeys))
		for _, k := range snap.BuildTimeKeys {
			btSet[k] = true
		}

		var added, overwritten, skipped []string
		for _, kv := range parsed {
			existing, present := snap.Values[kv.Key]
			switch {
			case !present:
				added = append(added, kv.Key)
				snap.Values[kv.Key] = kv.Value
			case envImportSkipExisting:
				skipped = append(skipped, kv.Key)
			case existing == kv.Value:
				// no-op, identical value
			default:
				overwritten = append(overwritten, kv.Key)
				snap.Values[kv.Key] = kv.Value
			}
			if envImportBuildTime {
				btSet[kv.Key] = true
			}
		}

		sort.Strings(added)
		sort.Strings(overwritten)
		sort.Strings(skipped)

		fmt.Printf("📄 %s → %s (%d entries parsed)\n", path, name, len(parsed))
		if len(added) > 0 {
			fmt.Printf("\n  + Added (%d):\n", len(added))
			for _, k := range added {
				fmt.Printf("      %s\n", k)
			}
		}
		if len(overwritten) > 0 {
			fmt.Printf("\n  ~ Overwritten (%d):\n", len(overwritten))
			for _, k := range overwritten {
				fmt.Printf("      %s\n", k)
			}
		}
		if len(skipped) > 0 {
			fmt.Printf("\n  · Skipped — already set (%d):\n", len(skipped))
			for _, k := range skipped {
				fmt.Printf("      %s\n", k)
			}
		}
		if envImportBuildTime {
			fmt.Println("\n  All imported vars will be marked [build-time].")
			fmt.Println("  ⚠️  Build-time values are embedded in image layers.")
		}

		if envImportDryRun {
			fmt.Println("\n🔍 Dry run — no changes made.")
			return
		}

		var btKeys []string
		for k := range btSet {
			btKeys = append(btKeys, k)
		}
		sort.Strings(btKeys)

		if err := setEnvVarsWithBuildTime(client, projectID, siteID, snap.Values, btKeys, envImportForce); err != nil {
			fmt.Printf("\n❌ Failed to apply: %v\n", err)
			return
		}

		fmt.Println("\n✅ Import applied. Redeploy with: ghayma deploy --prod")
	},
}

// dotenvEntry is one parsed key/value pair.
type dotenvEntry struct {
	Key   string
	Value string
}

// parseDotenv reads a dotenv-format file. Supports:
//   - KEY=VALUE
//   - optional leading `export `
//   - single/double-quoted values (strip quotes; preserve inner chars)
//   - # comments, blank lines
//   - trailing inline comments after unquoted values are NOT stripped
//     (kept as-is to avoid ambiguity with values containing `#`)
func parseDotenv(path string) ([]dotenvEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []dotenvEntry
	sc := bufio.NewScanner(f)
	// Allow large values (up to 1 MB per line) — dotenv files sometimes
	// carry big values like PEM blocks, though those usually aren't
	// multiline in .env syntax.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		line = strings.TrimSpace(line)

		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		value := strings.TrimSpace(line[eq+1:])

		// Strip matching surrounding quotes — quoted values preserve internal
		// whitespace; unquoted values have already been trimmed above.
		if len(value) >= 2 {
			first, last := value[0], value[len(value)-1]
			if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
				value = value[1 : len(value)-1]
			}
		}

		if key == "" {
			continue
		}
		out = append(out, dotenvEntry{Key: key, Value: value})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func init() {
	envSetCmd.Flags().BoolVar(&envSetBuildTime, "build-time", false, "mark these keys as build-time (available to next build, etc.)")
	envSetCmd.Flags().BoolVar(&envSetForce, "force", false, "bypass secret-pattern rejection for --build-time keys")

	envImportCmd.Flags().BoolVar(&envImportBuildTime, "build-time", false, "mark all imported vars as build-time")
	envImportCmd.Flags().BoolVar(&envImportDryRun, "dry-run", false, "print what would change without applying")
	envImportCmd.Flags().BoolVar(&envImportSkipExisting, "skip-existing", false, "do not overwrite vars that are already set")
	envImportCmd.Flags().BoolVar(&envImportForce, "force", false, "bypass secret-pattern rejection for --build-time keys")

	envCmd.AddCommand(envSetCmd)
	envCmd.AddCommand(envListCmd)
	envCmd.AddCommand(envDeleteCmd)
	envCmd.AddCommand(envRemoveCmd) // hidden deprecated alias
	envCmd.AddCommand(envImportCmd)
	rootCmd.AddCommand(envCmd)
}
