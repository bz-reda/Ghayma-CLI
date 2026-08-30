package cmd

import (
	"bufio"
	"encoding/json"
	"errors"
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

var (
	deployProd bool
	deploySite string
)

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
	// Crons is config-as-code for per-site cron jobs (cron-jobs spec §7). Kept
	// as raw JSON so the deploy forwards the array verbatim: key present
	// (including "[]") ⇒ authoritative site sync; key absent ⇒ deploy never
	// touches jobs. json.RawMessage is nil exactly when the key is absent.
	Crons json.RawMessage `json:"crons,omitempty"`
}

// cronsFormField returns the raw JSON of a project config's `crons` key for the
// deploy upload form, or "" when the key is absent. Empty (nil) ⇒ omit the form
// field so the backend never touches cron jobs; present ⇒ authoritative sync
// (cron-jobs spec §7). Pure so the presence/absence logic is unit-tested.
func cronsFormField(crons json.RawMessage) string {
	if len(crons) == 0 {
		return ""
	}
	return string(crons)
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
	Long: `Deploy the current project.

Inside an app directory — or anywhere below it — this deploys that directory's
site. At the root of a workspace linked with 'ghayma link' (whole project) it
asks which site to deploy, or takes --site <slug> — required when the shell is
not interactive.`,
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if !cfg.LoggedIn() {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		cwd, _ := os.Getwd()

		// One resolver decides which site this deploy targets: the per-app
		// config in CWD, the workspace manifest (--site, the only site, or a
		// question), or this directory's entry in an ancestor manifest.
		ctx, err := resolveSiteContext(cwd, deploySite, "deploy")
		if err != nil {
			if errors.Is(err, errAttachCancelled) {
				fmt.Println("❌ Cancelled")
				return
			}
			if !errors.Is(err, errNoProjectConfig) {
				fmt.Printf("❌ %v\n", err)
				return
			}

			// Nothing here or above describes a site — keep the pre-manifest
			// turbo scan-and-pick fallback so monorepos that only carry
			// per-app configs deploy from the root exactly as before.
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

			// Re-enter the resolver at the chosen app dir so the upload plan
			// is derived in exactly one place.
			ctx, err = resolveSiteContext(selected.Dir, deploySite, "deploy")
			if err != nil {
				fmt.Printf("❌ %v\n", err)
				return
			}
		}

		fmt.Println(deployHeadline(ctx))

		client := api.NewClient(cfg)

		rules := api.LoadIgnoreRules(ctx.SourceDir)
		printIgnoreRules(rules)

		// Surface which Dockerfile the build will use. The gate is the
		// project's "Use custom Dockerfile" toggle, so read it rather than
		// assume it is on; the lookup is best-effort and never fails the
		// deploy — an error just leaves the setting unknown (2026-08-17).
		var customDockerfile *bool
		if project, projErr := client.GetProject(ctx.ProjectID); projErr == nil {
			customDockerfile = &project.CustomDockerfileEnabled
		}
		for _, line := range dockerfileHint(appDirOf(ctx.SourceDir, ctx.RootDirectory), ctx.Site.DockerfilePath, customDockerfile) {
			fmt.Println(line)
		}

		bc := api.DeployBuildConfig{
			Framework:       ctx.Site.Framework,
			BuildCommand:    ctx.Site.BuildCommand,
			InstallCommand:  ctx.Site.InstallCommand,
			StartCommand:    ctx.Site.StartCommand,
			OutputDirectory: ctx.Site.OutputDirectory,
			Port:            ctx.Site.Port,
			Crons:           cronsFormField(ctx.Site.Crons),
		}
		resp, err := client.Deploy(ctx.ProjectID, ctx.Site.SiteID, ctx.SourceDir, "CLI deploy", deployProd, ctx.RootDirectory, filepath.ToSlash(ctx.Site.DockerfilePath), bc, rules)
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
	deployCmd.Flags().StringVar(&deploySite, "site", "", "Site to deploy (slug); at a workspace root with several sites this replaces the picker")
	rootCmd.AddCommand(deployCmd)
}

// deployHeadline is the line printed just before the upload starts. The three
// per-app forms are exactly what deploy printed before the manifest existed.
// The manifest form additionally names the tree being uploaded: "app" and
// "workspace" send different tarballs, and this line is the only place the
// user can see which one is on the wire (2026-08-16).
func deployHeadline(ctx *SiteContext) string {
	if ctx.FromManifest {
		return fmt.Sprintf("🚀 Deploying %s [site: %s] (%s)...", ctx.ProjectName, siteLabel(ctx.Site), uploadDescription(ctx))
	}
	if ctx.RootFromConfig {
		return fmt.Sprintf("🚀 Deploying %s (monorepo: %s, from config)...", ctx.ProjectName, ctx.RootDirectory)
	}
	if ctx.RootDirectory != "" {
		return fmt.Sprintf("🚀 Deploying %s (monorepo: %s)...", ctx.ProjectName, ctx.RootDirectory)
	}
	site := ""
	if ctx.Site.SiteName != "" && ctx.Site.SiteName != "main" {
		site = fmt.Sprintf(" [site: %s]", ctx.Site.SiteName)
	}
	return fmt.Sprintf("🚀 Deploying %s%s...", ctx.ProjectName, site)
}

// uploadDescription describes a manifest deploy's upload mode: a root_directory
// on the context means the whole workspace is uploaded and one app built inside
// it; no root_directory means only that app directory is uploaded.
func uploadDescription(ctx *SiteContext) string {
	if ctx.RootDirectory != "" {
		return fmt.Sprintf("uploading the workspace, building %s", ctx.RootDirectory)
	}
	dir := ctx.Site.RootDirectory
	if dir == "" {
		dir = "."
	}
	return fmt.Sprintf("uploading %s only", dir)
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
	// The build cannot run without these, so the tarball keeps them even when
	// a pattern above matches (Docker's own .dockerignore semantics).
	fmt.Println("   (Dockerfiles, .ghayma.json and ignore files are always uploaded)")
}

// appDirOf resolves the directory the build actually runs in: the upload root,
// or the root_directory inside it for a monorepo/workspace upload.
func appDirOf(sourceDir, rootDirectory string) string {
	if rootDirectory == "" {
		return sourceDir
	}
	return filepath.Join(sourceDir, filepath.FromSlash(rootDirectory))
}

// dockerfileHint returns the lines describing which Dockerfile the build will
// use. flag is the project's custom_dockerfile_enabled toggle; nil means the
// lookup failed and the setting is unknown.
//
// The wording has to track the flag because the flag is the whole gate: with it
// off the server always generates its own Dockerfile, however many Dockerfiles
// the upload carries. The old message claimed "Using your Dockerfile" from the
// file's presence alone, which read as a platform bug when the server was
// correctly ignoring it (2026-08-17).
//
// A dockerfile_path that is set but missing on disk is a warning, not an abort:
// the server resolves it (relative to the app directory) and is the authority
// on whether the build fails.
func dockerfileHint(appDir, explicitPath string, flag *bool) []string {
	relPath := explicitPath
	if relPath == "" {
		relPath = "Dockerfile"
	}

	if _, err := os.Stat(filepath.Join(appDir, filepath.FromSlash(relPath))); err != nil {
		if explicitPath == "" {
			return nil
		}
		// %s, not %q: a Windows-style path must be shown as written, not
		// with its separators escaped.
		return []string{fmt.Sprintf("⚠️  dockerfile_path \"%s\" not found under %s — the path is relative to the app directory; the build will fail if \"Use custom Dockerfile\" is enabled.", explicitPath, appDir)}
	}

	switch {
	case flag != nil && *flag:
		if explicitPath != "" {
			return []string{fmt.Sprintf("🐳 Using your Dockerfile at %s (dockerfile_path).", explicitPath)}
		}
		return []string{"🐳 Using your Dockerfile (custom Dockerfile enabled for this project)."}
	case flag != nil:
		return []string{fmt.Sprintf("ℹ️  Dockerfile found (%s) but \"Use custom Dockerfile\" is OFF for this project — the platform will generate one. Enable it in the Dashboard → project → Settings → Advanced.", relPath)}
	default:
		return []string{fmt.Sprintf("ℹ️  Dockerfile found (%s) — it is used only when \"Use custom Dockerfile\" is enabled for this project (could not verify the setting).", relPath)}
	}
}
