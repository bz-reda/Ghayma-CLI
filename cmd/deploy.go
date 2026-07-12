package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

var deployProd bool

type projectConfig struct {
	ProjectID     string `json:"project_id"`
	Name          string `json:"name"`
	Slug          string `json:"slug"`
	SiteID        string `json:"site_id,omitempty"`
	SiteName      string `json:"site_name,omitempty"`
	SiteSlug      string `json:"site_slug,omitempty"`
	RootDirectory string `json:"root_directory,omitempty"` // app subdir when .espacetech.json lives at monorepo root
	Framework     string `json:"framework,omitempty"`      // recorded from init; server still auto-detects
	// DockerfilePath is an optional explicit override for the user's
	// Dockerfile, relative to the appDir (project root or rootDirectory
	// for monorepos). When empty the platform falls back to the
	// convention (literal `Dockerfile` at appDir). Only honored when
	// `custom_dockerfile_enabled` is set on the project from the
	// Customer Dashboard (Part 2 alpha feature flag).
	DockerfilePath string `json:"dockerfile_path,omitempty"`
	// Build config: optional overrides recorded from init; the server
	// still auto-detects when these are empty.
	BuildCommand    string `json:"build_command,omitempty"`
	InstallCommand  string `json:"install_command,omitempty"`
	StartCommand    string `json:"start_command,omitempty"`
	OutputDirectory string `json:"output_directory,omitempty"`
	Port            int    `json:"port,omitempty"`
}

type appChoice struct {
	Config projectConfig
	Dir    string // absolute path to app directory
	RelDir string // relative path from monorepo root
}

// findMonorepoRoot walks up from dir looking for turbo.json
func findMonorepoRoot(dir string) string {
	current := dir
	for {
		if _, err := os.Stat(filepath.Join(current, "turbo.json")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

// findInitializedApps scans a monorepo for apps that have a project config
// (.ghayma.json or the legacy .espacetech.json).
func findInitializedApps(root string) []appChoice {
	var apps []appChoice
	// Track which app dirs we've already recorded so an app holding both
	// configs (e.g. after a partial rename) is listed once, with the new
	// .ghayma.json winning over the legacy file.
	indexByDir := make(map[string]int)
	skipDirs := map[string]bool{
		"node_modules": true, ".git": true, ".next": true,
		".turbo": true, "dist": true, ".cache": true,
	}

	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() && skipDirs[info.Name()] {
			return filepath.SkipDir
		}
		// Limit search depth
		relPath, _ := filepath.Rel(root, path)
		if info.IsDir() && len(strings.Split(relPath, string(filepath.Separator))) > 4 {
			return filepath.SkipDir
		}
		if info.Name() == projectConfigName || info.Name() == legacyProjectConfigName {
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil
			}
			var cfg projectConfig
			json.Unmarshal(data, &cfg)

			appDir := filepath.Dir(path)
			choice := appChoice{Config: cfg, Dir: appDir, RelDir: monorepoRelDir(root, appDir)}

			if i, seen := indexByDir[appDir]; seen {
				// Already recorded from the legacy file; the new config wins.
				if info.Name() == projectConfigName {
					apps[i] = choice
				}
				return nil
			}
			indexByDir[appDir] = len(apps)
			apps = append(apps, choice)
		}
		return nil
	})
	return apps
}

// monorepoRelDir returns appDir relative to monorepoRoot as a forward-slash
// path — safe to send to the build server. filepath.Rel yields OS-native
// separators (backslashes on Windows), but the platform joins root_directory
// onto a POSIX source path, so a backslash produces
// `/tmp/sources/<id>/apps\admin` — a literal filename that doesn't exist on
// Linux (2026-07-12 Windows deploy incident; same class as the #7 tar-name fix).
func monorepoRelDir(monorepoRoot, appDir string) string {
	rel, err := filepath.Rel(monorepoRoot, appDir)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
}

var deployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Deploy the current project",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		cwd, _ := os.Getwd()
		var projCfg projectConfig
		var appDir string

		// Try to read the project config from CWD
		configPath, err := findProjectConfig(cwd)
		var data []byte
		if err == nil {
			data, err = os.ReadFile(configPath)
		}
		if err != nil {
			// No config in CWD - check if inside a monorepo
			monorepoRoot := findMonorepoRoot(cwd)
			if monorepoRoot == "" {
				fmt.Println("❌ No project config found.")
				fmt.Println("   • New project?       run 'ghayma init'")
				fmt.Println("   • Existing project?  run 'ghayma link'")
				return
			}

			apps := findInitializedApps(monorepoRoot)
			if len(apps) == 0 {
				fmt.Println("❌ No initialized apps found in this monorepo.")
				fmt.Println("   Navigate to your app directory and run 'ghayma init' first.")
				return
			}

			var selected appChoice
			if len(apps) == 1 {
				selected = apps[0]
				fmt.Printf("📦 Found app: %s (%s)\n", selected.Config.Name, selected.RelDir)
			} else {
				fmt.Println("📦 Multiple apps found in this monorepo:")
				for i, app := range apps {
					fmt.Printf("  %d) %s (%s)\n", i+1, app.Config.Name, app.RelDir)
				}
				fmt.Print("\nSelect app to deploy: ")
				reader := bufio.NewReader(os.Stdin)
				input, _ := reader.ReadString('\n')
				input = strings.TrimSpace(input)
				choice, parseErr := strconv.Atoi(input)
				if parseErr != nil || choice < 1 || choice > len(apps) {
					fmt.Println("❌ Invalid selection")
					return
				}
				selected = apps[choice-1]
			}

			projCfg = selected.Config
			appDir = selected.Dir
		} else {
			json.Unmarshal(data, &projCfg)
			appDir = cwd
		}

		// Determine source directory and root_directory
		sourceDir := appDir
		rootDirectory := ""

		if projCfg.RootDirectory != "" {
			// Config is at the monorepo root and explicitly points at an app subdir.
			rootDirectory = filepath.ToSlash(projCfg.RootDirectory)
			fmt.Printf("🚀 Deploying %s (monorepo: %s, from config)...\n", projCfg.Name, rootDirectory)
		} else if monorepoRoot := findMonorepoRoot(appDir); monorepoRoot != "" && monorepoRoot != appDir {
			// Config is at the app subdir; derive rootDirectory from filesystem layout.
			// ToSlash so Windows backslashes never leak to the POSIX build server.
			rootDirectory = monorepoRelDir(monorepoRoot, appDir)
			sourceDir = monorepoRoot
			fmt.Printf("🚀 Deploying %s (monorepo: %s)...\n", projCfg.Name, rootDirectory)
		} else {
			siteLabel := ""
			if projCfg.SiteName != "" && projCfg.SiteName != "main" {
				siteLabel = fmt.Sprintf(" [site: %s]", projCfg.SiteName)
			}
			fmt.Printf("🚀 Deploying %s%s...\n", projCfg.Name, siteLabel)
		}

		client := api.NewClient(cfg)

		rules := api.LoadIgnoreRules(sourceDir)
		printIgnoreRules(rules)

		// Part 2 PR-C: surface custom Dockerfile usage at deploy time
		// so the user can see which file the platform will build with.
		// The actual override-vs-convention decision lives server-side
		// (gated by projects.custom_dockerfile_enabled on the project);
		// this is purely the UX hint. Quiet for projects on the
		// auto-generated path — same output as before in that case.
		printCustomDockerfileHint(sourceDir, rootDirectory, projCfg.DockerfilePath)

		bc := api.DeployBuildConfig{
			Framework:       projCfg.Framework,
			BuildCommand:    projCfg.BuildCommand,
			InstallCommand:  projCfg.InstallCommand,
			StartCommand:    projCfg.StartCommand,
			OutputDirectory: projCfg.OutputDirectory,
			Port:            projCfg.Port,
		}
		resp, err := client.Deploy(projCfg.ProjectID, projCfg.SiteID, sourceDir, "CLI deploy", deployProd, rootDirectory, filepath.ToSlash(projCfg.DockerfilePath), bc, rules)
		if err != nil {
			fmt.Printf("❌ Deploy failed: %v\n", err)
			return
		}

		fmt.Printf("📦 Build queued (deployment: %s)\n", resp.DeploymentID)
		fmt.Println("⏳ Waiting for build...")

		for i := 0; i < 120; i++ {
			time.Sleep(3 * time.Second)

			deployment, err := client.GetDeployment(resp.DeploymentID)
			if err != nil {
				continue
			}

			switch deployment.Status {
			case "live":
				fmt.Println("\n✅ Deployed successfully!")
				if len(deployment.Domains) > 0 {
					fmt.Println("🌐 Your app is live at:")
					for _, d := range deployment.Domains {
						fmt.Printf("   https://%s\n", d)
					}
				} else {
					fmt.Println("🌐 Your app is live")
				}
				return
			case "failed":
				fmt.Println("\n❌ Deployment failed!")
				logs, _ := client.GetDeploymentLogs(resp.DeploymentID)
				if logs != "" {
					fmt.Println("\n📋 Build logs:")
					fmt.Println(logs)
				}
				return
			case "building":
				fmt.Print(".")
			case "deploying":
				fmt.Print("🔄")
			}
		}

		fmt.Println("\n⚠️  Deploy timed out. Check status with: ghayma status")
	},
}

func init() {
	deployCmd.Flags().BoolVarP(&deployProd, "prod", "p", false, "Deploy to production")
	rootCmd.AddCommand(deployCmd)
}

func printIgnoreRules(rules *api.IgnoreRules) {
	fmt.Printf("📋 Baseline ignore: %s, .env*.local\n", strings.Join(api.BaselineIgnoreDirs, ", "))
	if rules == nil || rules.Source == "" {
		fmt.Println("   (no .ghaymaignore, .espacetechignore, or .dockerignore found)")
		return
	}
	if len(rules.Patterns) == 0 {
		fmt.Printf("   %s is present but empty\n", rules.Source)
		return
	}
	fmt.Printf("   Applying %s:\n", rules.Source)
	for _, p := range rules.Patterns {
		fmt.Printf("     • %s\n", p)
	}
}

// printCustomDockerfileHint prints a one-line "Using your Dockerfile..."
// message if the upload appears to carry one. Inspection order:
//
//   1. Explicit dockerfile_path in .espacetech.json wins — if it exists
//      on disk, name it. (If it's set but missing, the server will
//      fail-fast with a helpful error during DetermineBuildStrategy;
//      we don't pre-empt that here.)
//   2. Otherwise look for a literal `Dockerfile` at the appDir
//      (sourceDir + rootDirectory). If present, mention it.
//
// We always show the message when one of the above applies so the
// user can confirm the platform sees what they intended. The actual
// gate (projects.custom_dockerfile_enabled) lives server-side; if
// the flag is off, the platform silently ignores the file and uses
// auto-generation, which the build logs make obvious.
func printCustomDockerfileHint(sourceDir, rootDirectory, explicitPath string) {
	appDir := sourceDir
	if rootDirectory != "" {
		appDir = filepath.Join(sourceDir, rootDirectory)
	}

	if explicitPath != "" {
		absPath := filepath.Join(appDir, explicitPath)
		if _, err := os.Stat(absPath); err == nil {
			fmt.Printf("🐳 Using your Dockerfile at %s (from .espacetech.json dockerfile_path)\n", explicitPath)
		}
		// If explicitPath is set but missing on disk, the server's
		// DetermineBuildStrategy will return a clear error — no need
		// to pre-empt with a duplicate warning here.
		return
	}

	conventionPath := filepath.Join(appDir, "Dockerfile")
	if _, err := os.Stat(conventionPath); err == nil {
		fmt.Println("🐳 Using your Dockerfile (custom Dockerfile feature; must be enabled per-project in the dashboard)")
	}
}
