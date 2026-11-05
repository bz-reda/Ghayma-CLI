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
	deployProd      bool
	deploySite      string
	deployImage     string
	deployNoConnect bool
)

type projectConfig struct {
	ProjectID     string `json:"project_id"`
	Name          string `json:"name"`
	Slug          string `json:"slug"`
	SiteID        string `json:"site_id,omitempty"`
	SiteName      string `json:"site_name,omitempty"`
	SiteSlug      string `json:"site_slug,omitempty"`
	RootDirectory string `json:"root_directory,omitempty"` // app subdir when .ghayma.json lives at monorepo root
	Framework     string `json:"framework,omitempty"`      // recorded from init; server still auto-detects
	// DockerfilePath is an optional explicit override for the user's
	// Dockerfile, relative to the appDir (project root or rootDirectory
	// for monorepos). When empty the platform falls back to the
	// convention (literal `Dockerfile` at appDir). Only honored when the
	// site builds with its Dockerfile.
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

// deployWaitPolls × deployWaitInterval = 25 minutes: the server's queue plus its 20-minute build deadline.
// A var, not a const, only so a test that drives the whole command through the
// wait does not sit out a real three-second poll.
var (
	deployWaitPolls    = 500
	deployWaitInterval = 3 * time.Second
)

var deployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Deploy the current project",
	Long: `Deploy the current project.

Inside an app directory — or anywhere below it — this deploys that directory's
site. At the root of a workspace linked with 'ghayma link' (whole project) it
asks which site to deploy, or takes --site <slug> — required when the shell is
not interactive.

--image deploys an image you already pushed with 'ghayma docker push' instead
of uploading this directory: nothing is built, the image is checked and rolled
out as it is. Name a tag, or a sha256: digest for one exact image.

A database, bucket or auth app that no site uses yet is offered to the site
being deployed, one question each on a terminal, and connected before the
build starts (a database still starting connects once it accepts connections).
Without a terminal, or with --no-connect, nothing is connected
and the deploy prints the command that connects each one. The offer and
--no-connect apply to source deploys only, not to --image.

Examples:
  ghayma deploy
  ghayma deploy --site admin
  ghayma deploy --no-connect
  ghayma deploy --image v1
  ghayma deploy --image sha256:0a1b2c…`,
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if !cfg.LoggedIn() {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		cwd, _ := os.Getwd()
		client := api.NewClient(cfg)

		// One resolver decides which site this deploy targets: the per-app
		// config in CWD, the workspace manifest (--site, the only site, or a
		// question), or this directory's entry in an ancestor manifest.
		ctx, err := resolveSiteContextLive(client, cwd, deploySite, "deploy")
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
			ctx, err = resolveSiteContextLive(client, selected.Dir, deploySite, "deploy")
			if err != nil {
				fmt.Printf("❌ %v\n", err)
				return
			}
		}

		// An image deploy ships what was already pushed to the registry, so it
		// branches out before every line of the upload path below: no tarball,
		// no ignore rules, no Dockerfile and no build settings are involved.
		if strings.TrimSpace(deployImage) != "" {
			runImageDeploy(client, ctx, deployImage, deploySite, deployProd)
			return
		}

		builder, err := configBuilder(ctx)
		if err != nil {
			failf("%v", err)
			return
		}

		// A config naming no site leaves --site to the live list, as every
		// other site-scoped command does. The upload has to carry the site it
		// picked: without a site_id the server deploys to the default site.
		if ctx.NoSite && deploySite != "" {
			site, err := liveSiteOf(client, ctx, deploySite)
			if errors.Is(err, errNoSite) {
				failNoSite()
				return
			}
			if err != nil {
				failf("%v", err)
				return
			}
			ctx.Site.SiteID, ctx.Site.SiteSlug, ctx.Site.SiteName = site.ID, site.Slug, site.Name
			ctx.NoSite = false
		}

		// The live site list names the site the upload lands on (below). A
		// site-less project still deploys — the platform materializes `main`
		// on the first one — but say so, because nothing in the config or the
		// init flow ever mentioned a site. A config naming no site on a
		// project that already has one gets no such notice; when the list
		// cannot be read the notice still prints, as before.
		sites, sitesErr := client.ListSites(ctx.ProjectID)
		if ctx.NoSite && (sitesErr != nil || len(sites) == 0) {
			fmt.Println("ℹ️  This project has no site yet; deploying creates the site 'main'.")
		}

		// --prod is a no-op since Environments (design D6): the target site's
		// kind decides. Said here, before the upload, so the answer arrives
		// with the deploy rather than after it.
		if deployProd {
			slug, environment := bestEffortSiteKind(client, ctx, deploySite)
			fmt.Println(prodFlagNotice(slug, environment))
		}

		// Services no site uses are offered to the site the upload lands on,
		// with every answer in before the upload starts; the choice rides on
		// the upload. When the server has no site for it — several sites, none
		// default or main, or a linked site since deleted — it refuses the
		// upload, so nothing is offered.
		var chosen []api.Unconnected
		target, known := deployTargetSite(ctx, sites, sitesErr == nil)
		if known {
			if chosen, err = chooseDeployConnections(client, ctx.ProjectID, target, stdinIsTerminalFn(), deployNoConnect); err != nil {
				fmt.Println("❌ Cancelled")
				return
			}
		}

		// The headline stays open: the wait's progress dots extend it.
		fmt.Print(deployHeadline(ctx))

		rules := api.LoadIgnoreRules(ctx.SourceDir)
		bc := api.DeployBuildConfig{
			Framework:       ctx.Site.Framework,
			BuildCommand:    ctx.Site.BuildCommand,
			InstallCommand:  ctx.Site.InstallCommand,
			StartCommand:    ctx.Site.StartCommand,
			OutputDirectory: ctx.Site.OutputDirectory,
			Port:            ctx.Site.Port,
			Crons:           cronsFormField(ctx.Site.Crons),
			Builder:         builder,
		}
		resp, err := client.Deploy(ctx.ProjectID, ctx.Site.SiteID, ctx.SourceDir, "CLI deploy", deployProd, ctx.RootDirectory, filepath.ToSlash(ctx.Site.DockerfilePath), bc, rules, connectionItems(chosen))
		if err != nil {
			fmt.Printf("\n❌ Deploy failed: %v\n", err)
			return
		}

		if len(chosen) > 0 {
			fmt.Println()
			printDeployConnections(resp.Connections, chosen, deployedSiteSlug(sites, resp.SiteID, target))
		}
		waitForDeployment(client, resp.DeploymentID)
	},
}

// waitForDeployment is the wait every deploy shares, whatever it was created
// from: one dot per poll on the caller's open headline line, then where the app
// is live or why it failed.
//
// An image deploy uses it unchanged on purpose — it IS a normal deployment,
// and an image refused by admission is a FAILED one whose build logs carry the
// reason, which is exactly what the failure branch already prints.
func waitForDeployment(client *api.Client, deploymentID string) {
	// Same budget as the server: the build queue plus the 20-minute build deadline.
	for i := 0; i < deployWaitPolls; i++ {
		time.Sleep(deployWaitInterval)

		deployment, err := client.GetDeployment(deploymentID)
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
			logs, _ := client.GetDeploymentLogs(deploymentID)
			if logs != "" {
				fmt.Println("\n📋 Build logs:")
				fmt.Println(logs)
			}
			return
		case "queued", "building", "deploying":
			fmt.Print(".")
		}
	}

	fmt.Println("\n⚠️  Still not finished after 25 minutes; the build keeps running on the platform. Check with: ghayma status")
}

// noSiteForImageMessage is what a site-less project is told when it asks for an
// image deploy. The deploy route acts on an EXISTING site, and the two ways to
// get one differ: a source deploy materializes the canonical main site (its
// build settings, its bare <project>.ghayma.app domain), while 'site create'
// adds a site by name. Both are offered rather than one being run silently.
const noSiteForImageMessage = "ℹ️  This project has no site yet, and deploying an image needs one.\n" +
	"   Create it with: ghayma site create main\n" +
	"   (or run 'ghayma deploy' once to deploy from source, which creates the main site)"

// runImageDeploy deploys an image already in the project's registry
// repository. The site has to be a LIVE one: the endpoint is site-scoped and
// takes a site id, so the config's linked site is mapped onto the project's
// actual sites first.
func runImageDeploy(client *api.Client, ctx *SiteContext, image, siteFlag string, prod bool) {
	site, err := liveSiteOf(client, ctx, siteFlag)
	if err != nil {
		if errors.Is(err, errNoSite) {
			fmt.Println(noSiteForImageMessage)
			exitFn(1)
			return
		}
		reportSiteError(err)
		exitFn(1)
		return
	}
	// Same notice as the upload path — the site is already resolved here, so
	// it costs no extra request.
	if prod {
		fmt.Println(prodFlagNotice(site.Slug, site.Environment))
	}
	image = strings.TrimSpace(image)
	fmt.Printf("🚀 Deploying image %s to %s [site: %s]...", image, ctx.ProjectName, site.Slug)
	deployPushedImage(client, ctx.ProjectID, site.ID, image, prod)
}

// deployPushedImage creates the image deployment and waits on it, after the
// caller printed its headline without a newline. The commit message is left to
// the server, which labels the row "Deploy image <ref>" — one place decides
// how an image deployment is named.
func deployPushedImage(client *api.Client, projectID, siteID, image string, prod bool) {
	resp, err := client.DeployImage(projectID, siteID, image, "", prod)
	if err != nil {
		fmt.Println()
		failf("Deploy failed: %v", err)
		return
	}
	waitForDeployment(client, resp.ID)
}

// prodFlagNotice is what --prod prints since Environments (design D6). The flag
// is kept — scripts and muscle memory carry it everywhere — and it is still
// sent, but a deployment is a production one exactly when the site it targets
// is a production site, so the flag no longer decides anything. The notice
// names the target and its kind, which is the fact that replaced the flag;
// environment is "" on a platform that predates the field, and the sentence
// then stops before claiming one.
func prodFlagNotice(siteSlug, environment string) string {
	const lead = "ℹ️  --prod no longer changes anything: whether a deploy is production now follows the target site's environment"
	if siteSlug == "" || environment == "" {
		return lead + "."
	}
	return fmt.Sprintf("%s (%s is %s).", lead, siteSlug, environment)
}

// bestEffortSiteKind names the site a source deploy targets and its
// environment. Best-effort by design: it exists to label a notice, so a live
// list that cannot be read falls back to the linked site's name with no kind
// rather than failing a deploy that is otherwise fine.
func bestEffortSiteKind(client *api.Client, ctx *SiteContext, siteFlag string) (slug, environment string) {
	if site, err := liveSiteOf(client, ctx, siteFlag); err == nil {
		return site.Slug, site.Environment
	}
	if ctx.NoSite {
		return "", ""
	}
	return siteLabel(ctx.Site), ""
}

func init() {
	deployCmd.Flags().BoolVarP(&deployProd, "prod", "p", false, "(no longer needed) a deploy is production when the target site is a production site")
	deployCmd.Flags().StringVar(&deploySite, "site", "", "Site to deploy (slug, name or id); picks the project's site when this directory's config names none, and replaces the picker at a workspace root")
	deployCmd.Flags().StringVar(&deployImage, "image", "", "Deploy an image already pushed with 'ghayma docker push' (tag, or sha256: digest) instead of uploading this directory")
	deployCmd.Flags().BoolVar(&deployNoConnect, "no-connect", false, "Leave services no site uses unconnected, without asking")
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

// appDirOf resolves the directory the build actually runs in: the upload root,
// or the root_directory inside it for a monorepo/workspace upload.
func appDirOf(sourceDir, rootDirectory string) string {
	if rootDirectory == "" {
		return sourceDir
	}
	return filepath.Join(sourceDir, filepath.FromSlash(rootDirectory))
}
