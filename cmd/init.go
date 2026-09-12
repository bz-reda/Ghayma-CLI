package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
)

type ProjectConfig struct {
	ProjectID     string `json:"project_id"`
	Name          string `json:"name"`
	Slug          string `json:"slug"`
	Framework     string `json:"framework"`
	SiteID        string `json:"site_id,omitempty"`
	SiteName      string `json:"site_name,omitempty"`
	SiteSlug      string `json:"site_slug,omitempty"`
	RootDirectory string `json:"root_directory,omitempty"`
	// Build config: optional overrides recorded from init; the server
	// still auto-detects when these are empty.
	BuildCommand    string `json:"build_command,omitempty"`
	InstallCommand  string `json:"install_command,omitempty"`
	StartCommand    string `json:"start_command,omitempty"`
	OutputDirectory string `json:"output_directory,omitempty"`
	Port            int    `json:"port,omitempty"`
}

// detectFramework inspects the project files to pick a framework label for
// .ghayma.json. package.json is checked BEFORE index.html so a Node/Next app
// that ships a static index.html isn't mis-detected as static. "auto" lets the
// platform decide (it auto-routes static / Node / Next.js / Python / Go / …).
func detectFramework(dir string) string {
	has := func(f string) bool { _, err := os.Stat(filepath.Join(dir, f)); return err == nil }
	switch {
	case has("package.json"):
		if data, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil && strings.Contains(string(data), `"next"`) {
			return "nextjs"
		}
		return "node"
	case has("go.mod"):
		return "go"
	case has("requirements.txt") || has("pyproject.toml"):
		return "python"
	case has("composer.json"):
		return "php"
	case has("Gemfile"):
		return "ruby"
	case has("Cargo.toml"):
		return "rust"
	case has("index.html"):
		return "static"
	default:
		return "auto"
	}
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a new project in the current directory",
	Long: `Initialize a new project in the current directory.

init asks how the project will be used: deploy an app or site from this
directory, or no site at all — databases, storage and auth only, which is what
a mobile app or an external backend needs. A site can always be added later
with 'ghayma site create <name>'.

For scripts and CI: --no-site answers "no site" without asking, --site <name>
answers "deploy a site" with that name, and --domain <host> skips the domain
question.`,
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if !cfg.LoggedIn() {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		// Flag sanity first: a contradictory combination must be refused
		// before anything reaches the API — and with a non-zero exit, since
		// these flags exist for scripts and CI.
		if err := validateInitSiteFlags(initNoSite, initSite, initDomain); err != nil {
			fmt.Printf("❌ %v\n", err)
			exitFn(1)
			return
		}

		// Check if already initialized
		if _, err := findProjectConfig("."); err == nil {
			fmt.Println("⚠️  Project already initialized. Delete the project config to re-initialize.")
			return
		}

		// If CWD is a monorepo root, nudge the user to init from their app
		// subdir instead — or ask for the app subdir and write the config
		// inside it so the deploy flow sends the correct root_directory.
		appSubdir, ok := detectMonorepoAppSubdir()
		if !ok {
			return
		}
		configDir := "."
		if appSubdir != "" {
			if existing, err := findProjectConfig(appSubdir); err == nil {
				fmt.Printf("⚠️  %s already exists. Delete it to re-initialize.\n", existing)
				return
			}
			configDir = appSubdir
		}

		client := api.NewClient(cfg)

		// Offer to attach to an EXISTING project instead of always creating a
		// new one — the only way to add a second monorepo app (e.g. apps/admin)
		// as its own site under a project that already exists. A failed list
		// (old/self-hosted server) silently falls through to the create path.
		if projects, err := client.ListProjects(); err == nil && len(projects) > 0 {
			idx, err := promptProjectChoiceFn(projects)
			if err != nil {
				fmt.Println("❌ Cancelled.")
				return
			}
			if idx != createNewIdx {
				if err := attachToExistingProject(client, &projects[idx], configDir, initNoSite); err != nil {
					if errors.Is(err, errAttachCancelled) {
						fmt.Println("❌ Cancelled.")
					} else {
						fmt.Printf("❌ %v\n", err)
					}
				}
				return
			}
		}

		// --- create a NEW project ---

		// Project name
		name, err := promptProjectNameFn()
		if err != nil {
			fmt.Println("❌ Cancelled.")
			return
		}

		// Framework — auto-detected from the project files (no longer a
		// hardcoded nextjs picker). "auto" defers to the platform. Only a site
		// build uses it, so the detection is reported (and sent) below, once
		// the project is known to deploy something.
		framework := detectFramework(".")

		// Resolve the billing account for the new project. The API
		// requires billing_account_id for billable plans (init creates
		// with the default "hobby" plan, which is billable). Auto-select
		// when there's exactly one eligible account, prompt when there
		// are several, honor --billing-account for non-interactive use.
		billingAccountFlag, _ := cmd.Flags().GetString("billing-account")
		billingAccountID, err := resolveBillingAccount(client, billingAccountFlag)
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		// Resolve the plan for the new project. Historically init sent an
		// empty plan, so the server silently defaulted every project to
		// hobby (2026-07-09: a project landed on hobby without anyone
		// choosing). Now init asks — or honors --plan — and sends the slug.
		planFlag, _ := cmd.Flags().GetString("plan")
		plan, err := resolvePlan(client, planFlag)
		if err != nil {
			// A genuine cancel must never fall through to a create on the
			// default plan — abort cleanly. Other errors (invalid --plan)
			// print their own message.
			if errors.Is(err, errPlanSelectionCancelled) {
				fmt.Println("❌ Cancelled.")
			} else {
				fmt.Printf("❌ %v\n", err)
			}
			return
		}

		// One explicit question, asked once, before any site prompt: a
		// database/storage/auth-only project never deploys from this directory,
		// so it gets no site, no domain and nothing to build.
		mode, err := resolveInitSiteMode(initNoSite, initSite)
		if err != nil {
			fmt.Println("❌ Cancelled.")
			return
		}
		if mode == siteModeNoSite {
			framework = "auto"
		} else {
			fmt.Printf("🔎 Detected framework: %s\n", framework)
		}

		project, err := client.CreateProject(name, framework, billingAccountID, plan)
		if err != nil {
			fmt.Printf("❌ Failed to create project: %v\n", err)
			return
		}

		if mode == siteModeNoSite {
			finishSiteLessInit(project, framework, configDir)
			return
		}

		// Site name — default "main" for single-site projects. --site answers
		// it for scripts and CI ("main" allowed).
		siteName := strings.TrimSpace(initSite)
		if siteName == "" {
			sitePrompt := promptui.Prompt{
				Label:   "Site name (e.g. frontend, admin — leave empty for 'main')",
				Default: "",
			}
			siteName, err = sitePrompt.Run()
			if err != nil {
				fmt.Println("❌ Cancelled.")
				return
			}
			siteName = strings.TrimSpace(siteName)
		}
		if siteName == "" {
			siteName = "main"
		}

		// The main site is materialized lazily by the backend on first deploy
		// (2026-07-23) — a fresh project has no site yet. If one already exists
		// (re-init, or the user created it), record its id; otherwise leave the
		// fields empty and let deploy create main + its default domain on the fly.
		// Any other name creates the site now via the API.
		var siteID, siteSlug string
		if siteName == "main" {
			sites, err := client.ListSites(project.ID)
			if err == nil {
				for _, s := range sites {
					if s.Slug == "main" {
						siteID = s.ID
						siteSlug = s.Slug
						break
					}
				}
			}
		} else {
			site, err := client.CreateSite(project.ID, siteName)
			if err != nil {
				fmt.Printf("⚠️  Failed to create site '%s': %v\n", siteName, err)
				fmt.Println("   You can add sites later with: ghayma site add <name>")
			} else {
				siteID = site.ID
				siteSlug = site.Slug
				fmt.Printf("📌 Site '%s' created (slug: %s)\n", site.Name, site.Slug)
			}
		}

		// Domain. --domain answers it; so does a non-interactive shell, where
		// there is nobody to ask and no safe default but "skip".
		domain := strings.TrimSpace(initDomain)
		if domain == "" && stdinIsTerminalFn() {
			domainPrompt := promptui.Prompt{
				Label:   "Domain (e.g., mysite.com, leave empty to skip)",
				Default: "",
			}
			domain, err = domainPrompt.Run()
			if err != nil {
				fmt.Println("❌ Cancelled.")
				return
			}
			domain = strings.TrimSpace(domain)
		}

		if domain != "" {
			if err := client.AddDomain(project.ID, siteID, domain); err != nil {
				fmt.Printf("⚠️  Failed to add domain: %v\n", err)
			} else {
				fmt.Printf("🌐 Domain %s added\n", domain)
			}
		}

		// Save project config — in the app subdir for monorepos, in CWD otherwise.
		configPath, err := writeInitConfig(configDir, ProjectConfig{
			ProjectID: project.ID,
			Name:      project.Name,
			Slug:      project.Slug,
			Framework: framework,
			SiteID:    siteID,
			SiteName:  siteName,
			SiteSlug:  siteSlug,
		})
		if err != nil {
			fmt.Printf("❌ Failed to write config: %v\n", err)
			return
		}

		fmt.Printf("✅ Project '%s' created (slug: %s)\n", project.Name, project.Slug)
		if siteName != "main" {
			fmt.Printf("   Site: %s (slug: %s)\n", siteName, siteSlug)
		}
		fmt.Printf("📁 Config saved to %s\n", configPath)
		fmt.Println("\nNext: run 'ghayma deploy --prod' to deploy")
	},
}

// init's site flags. Bound to package vars (rather than read back off the
// cobra flag set) so the tests that drive the real command tree can reset them
// between runs like every other command's flags.
var (
	initNoSite bool
	initSite   string
	initDomain string
)

// initSiteMode is what a new project is for: an app or site this directory
// deploys, or backend resources only (databases, storage, auth).
type initSiteMode int

const (
	siteModeDeploy initSiteMode = iota
	siteModeNoSite
)

// siteModeLabels are the two answers to "How will you use this project?", in
// the order the picker offers them.
var siteModeLabels = []string{
	"Deploy an app or site from this directory",
	"No site — only databases, storage and auth (mobile app, external backend)",
}

// promptSiteModeFn is indirected so tests can drive the question without a TTY.
var promptSiteModeFn = promptSiteMode

// promptSiteMode asks the one question that decides the rest of init. A cancel
// surfaces the promptui error so init aborts instead of assuming a site.
func promptSiteMode() (initSiteMode, error) {
	sel := promptui.Select{
		Label: "How will you use this project?",
		Items: siteModeLabels,
	}
	idx, _, err := sel.Run()
	if err != nil {
		return siteModeDeploy, err
	}
	if idx == 1 {
		return siteModeNoSite, nil
	}
	return siteModeDeploy, nil
}

// resolveInitSiteMode decides whether the new project deploys a site. The flags
// answer for scripts and CI; otherwise the question is asked once, before any
// site prompt. Neither flag on a non-interactive shell keeps today's behaviour:
// the prompt fails and init aborts rather than guessing.
func resolveInitSiteMode(noSite bool, siteFlag string) (initSiteMode, error) {
	if noSite {
		return siteModeNoSite, nil
	}
	if strings.TrimSpace(siteFlag) != "" {
		return siteModeDeploy, nil
	}
	return promptSiteModeFn()
}

// validateInitSiteFlags rejects the contradictory combination before anything
// is created: --no-site means this directory deploys nothing, so a site name or
// a domain to attach to it cannot also be meant.
func validateInitSiteFlags(noSite bool, siteFlag, domainFlag string) error {
	if !noSite {
		return nil
	}
	var conflicting []string
	if strings.TrimSpace(siteFlag) != "" {
		conflicting = append(conflicting, "--site")
	}
	if strings.TrimSpace(domainFlag) != "" {
		conflicting = append(conflicting, "--domain")
	}
	if len(conflicting) == 0 {
		return nil
	}
	return fmt.Errorf("--no-site cannot be combined with %s", strings.Join(conflicting, " or "))
}

// finishSiteLessInit writes the site-less config and says what such a project
// is for. No site prompt, no domain prompt, no CreateSite/AddDomain call: the
// config carries project fields only, and `ghayma site create` adds a site if
// the project ever grows one.
func finishSiteLessInit(project *api.Project, framework, configDir string) {
	configPath, err := writeInitConfig(configDir, ProjectConfig{
		ProjectID: project.ID,
		Name:      project.Name,
		Slug:      project.Slug,
		Framework: framework,
	})
	if err != nil {
		fmt.Printf("❌ Failed to write config: %v\n", err)
		return
	}
	fmt.Printf("✅ Project '%s' created (slug: %s) — no site\n", project.Name, project.Slug)
	fmt.Printf("📁 Config saved to %s\n", configPath)
	printNoSiteNextSteps()
}

// writeInitConfig writes a brand-new project config into configDir and returns
// the path it landed on.
func writeInitConfig(configDir string, cfg ProjectConfig) (string, error) {
	path := projectConfigWritePath(configDir)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, data, 0644)
}

// maxAppSubdirAttempts caps the re-prompt loop below.
const maxAppSubdirAttempts = 3

// promptAppSubdirFn / promptProjectNameFn are indirected so tests can drive
// the create-new path without a terminal.
var (
	promptAppSubdirFn   = promptAppSubdir
	promptProjectNameFn = promptProjectName
)

// promptProjectName asks for the new project's name. A cancel surfaces the
// error so init aborts before anything is created.
func promptProjectName() (string, error) {
	prompt := promptui.Prompt{Label: "Project name"}
	return prompt.Run()
}

// detectMonorepoAppSubdir asks for the app subdirectory to initialise inside
// when CWD is a workspace root. The bool reports whether the command may carry
// on: false means the user cancelled or ran out of tries and was told what to
// do next.
func detectMonorepoAppSubdir() (string, bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", true
	}
	return appSubdirForWorkspace(cwd)
}

// appSubdirForWorkspace is detectMonorepoAppSubdir with the directory passed
// in. It returns "" (with true) when cwd is not a workspace root, or when the
// user wants the config right here.
//
// It shares isWorkspaceRoot with the rest of the CLI instead of keeping its own
// file check: statting pnpm-workspace.yaml made a plain create-next-app project
// (pnpm 10 writes that file for settings alone) announce a monorepo and demand
// an app subdirectory that does not exist (2026-09-03).
func appSubdirForWorkspace(cwd string) (string, bool) {
	if !isWorkspaceRoot(cwd) {
		return "", true
	}

	fmt.Printf("📦 Workspace root detected (%s).\n", workspaceMarker(cwd))
	fmt.Println("   The app config should live in your app's subdirectory so the deploy")
	fmt.Println("   uploads the whole workspace and builds the right target.")

	def := defaultAppSubdir(cwd)
	for attempt := 0; attempt < maxAppSubdirAttempts; attempt++ {
		answer, err := promptAppSubdirFn(def)
		if err != nil {
			fmt.Println("❌ Cancelled.")
			return "", false
		}
		answer = strings.TrimSpace(strings.Trim(answer, "/"))
		if answer == "" {
			return "", true // initialise here, at the workspace root
		}
		if err := validateAppSubdir(cwd, answer); err != nil {
			fmt.Printf("⚠️  %v\n", err)
			continue
		}
		return answer, true
	}

	fmt.Println("❌ No usable app subdirectory given.")
	fmt.Println("   Run 'ghayma init' from inside the app's own directory, or answer with")
	fmt.Println("   an empty line to initialise here at the workspace root.")
	return "", false
}

// validateAppSubdir checks an answer before anything is written into it: the
// answer must be a real directory that holds a package.json. The Next.js router
// folder named "app" is the trap this closes — it was accepted, so the config
// landed inside the router source instead of an app (2026-09-03).
func validateAppSubdir(cwd, answer string) error {
	dir := filepath.Join(cwd, filepath.FromSlash(answer))
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("%s does not exist here", answer)
	}
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
		return fmt.Errorf("%s has no package.json — pick the directory that holds your app's package.json (a Next.js %q router folder is not an app)", answer, "app")
	}
	return nil
}

// defaultAppSubdir suggests an answer the user can just accept: a real app
// directory of this workspace when there is one, else the usual layout.
func defaultAppSubdir(cwd string) string {
	if dirs := workspaceAppDirs(cwd); len(dirs) > 0 {
		return dirs[0]
	}
	return "apps/web"
}

func promptAppSubdir(def string) (string, error) {
	prompt := promptui.Prompt{
		Label:   "App subdirectory (e.g. apps/web; leave empty to init at root)",
		Default: def,
	}
	return prompt.Run()
}

// resolveBillingAccount decides which billing account backs the new
// project. With --billing-account it uses that id as-is (non-interactive
// / CI path — no list call, no prompt). Otherwise it lists the caller's
// accounts, filters to eligible ones (active + owner/admin), auto-selects
// a single one, or prompts to choose among several. Returns an actionable
// error when the user has no eligible account.
func resolveBillingAccount(client *api.Client, flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}

	accounts, err := client.ListBillingAccounts()
	if err != nil {
		return "", fmt.Errorf("could not list billing accounts: %w", err)
	}
	eligible := api.EligibleBillingAccounts(accounts)

	switch len(eligible) {
	case 0:
		return "", fmt.Errorf("no eligible billing account found.\n" +
			"   A billing account is required to create a project.\n" +
			"   Create one at dashboard.ghayma.dev/settings/billing, then re-run 'ghayma init'\n" +
			"   (or pass --billing-account <id> if you already have one).")
	case 1:
		a := eligible[0]
		fmt.Printf("💳 Using billing account: %s%s\n", a.Name, personalSuffix(a))
		return a.ID, nil
	default:
		labels := make([]string, len(eligible))
		for i, a := range eligible {
			labels[i] = fmt.Sprintf("%s%s  (role: %s)", a.Name, personalSuffix(a), a.Role)
		}
		sel := promptui.Select{
			Label: "Select a billing account for this project",
			Items: labels,
			Size:  10,
		}
		idx, _, err := sel.Run()
		if err != nil {
			return "", fmt.Errorf("cancelled")
		}
		return eligible[idx].ID, nil
	}
}

func personalSuffix(a api.BillingAccount) string {
	if a.IsPersonal {
		return " (personal)"
	}
	return ""
}

// errPlanSelectionCancelled marks an interactive plan picker cancel (Ctrl-C)
// so init aborts cleanly instead of falling through to a create on the
// server's default plan — the exact silent-default bug Task 6 fixes.
var errPlanSelectionCancelled = errors.New("plan selection cancelled")

// promptPlanSelectFn is indirected so tests can substitute the promptui I/O
// and exercise the cancel/abort control flow without a TTY.
var promptPlanSelectFn = promptPlanSelect

// resolvePlan decides which plan slug to send to CreateProject.
//   - --plan set: validated against the fetched list; an unknown slug errors
//     (naming the available slugs) so init aborts.
//   - Interactive (no --plan): a picker over the fetched plans. A Ctrl-C
//     cancel returns errPlanSelectionCancelled so init aborts — it never
//     falls through to the default plan.
//   - Fetch failure (old/self-hosted server without the endpoint, or any
//     error) WITH --plan set: sends the slug through unvalidated — the server
//     validates/rejects it, so a scripted `init --plan pro` never silently
//     lands on the default plan. A note flags that it wasn't validated.
//   - Fetch failure WITHOUT --plan (interactive), or an empty plan list:
//     returns "" (server default) with a printed note. Never blocks init.
//
// Note the deliberate distinction: a fetch failure degrades to the server
// default, but a user cancel aborts — the two must not be conflated.
func resolvePlan(client *api.Client, planFlag string) (string, error) {
	plans, err := client.GetPlans()
	if err != nil {
		if planFlag != "" {
			// Don't drop an explicit flag on a catalog blip — send it as-is
			// and let the server validate it (silent-wrong-plan is the bug
			// Task 6 fixes; dropping to the default here would reintroduce it).
			fmt.Printf("ℹ️  Couldn't fetch plans to validate --plan %q; sending it as-is for the server to validate.\n", planFlag)
			return planFlag, nil
		}
		fmt.Println("ℹ️  Couldn't fetch plans; the server's default plan will apply.")
		return "", nil
	}

	if planFlag != "" {
		return validatePlanFlag(plans, planFlag)
	}

	if len(plans) == 0 {
		fmt.Println("ℹ️  No plans available to choose; the server's default plan will apply.")
		return "", nil
	}

	slug, err := promptPlanSelectFn(plans)
	if err != nil {
		return "", errPlanSelectionCancelled
	}
	return slug, nil
}

// validatePlanFlag guards the --plan value against the fetched plans. A known
// slug passes; an unknown one errors, listing the available slugs. Pure.
func validatePlanFlag(plans []api.FixedPlan, slug string) (string, error) {
	for _, p := range plans {
		if p.Slug == slug {
			return slug, nil
		}
	}
	return "", fmt.Errorf("unknown plan %q; choose one of: %s", slug, strings.Join(planSlugs(plans), ", "))
}

// planSlugs lists the plan slugs in response order, for the --plan error hint.
func planSlugs(plans []api.FixedPlan) []string {
	slugs := make([]string, len(plans))
	for i, p := range plans {
		slugs[i] = p.Slug
	}
	return slugs
}

// planSelectLabels renders one interactive line per fixed plan. Pure so the
// label formatting is unit-testable.
func planSelectLabels(plans []api.FixedPlan) []string {
	labels := make([]string, len(plans))
	for i, p := range plans {
		labels[i] = planSelectLabel(p)
	}
	return labels
}

// planSelectLabel formats a single plan choice, e.g.
// "hobby — 2,500 DZD/mo · 10 pts". Price and points come straight from the
// billing/plans response — nothing hardcoded.
func planSelectLabel(p api.FixedPlan) string {
	return fmt.Sprintf("%s — %s DZD/mo · %d pts", p.Slug, formatThousands(p.PriceDZDPerMonth), p.Points)
}

// formatThousands renders an integer with comma thousand-separators
// (2500 → "2,500"), matching the brief's price format.
func formatThousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteByte(s[i])
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// promptPlanSelect renders the catalog-driven plan picker and returns the
// chosen slug. A promptui cancel (Ctrl-C) surfaces the error so resolvePlan
// aborts init.
func promptPlanSelect(plans []api.FixedPlan) (string, error) {
	sel := promptui.Select{
		Label: "Select a plan for this project",
		Items: planSelectLabels(plans),
		Size:  10,
	}
	idx, _, err := sel.Run()
	if err != nil {
		return "", err
	}
	return plans[idx].Slug, nil
}

func init() {
	initCmd.Flags().String("billing-account", "", "Billing account ID for the new project (skips the interactive picker; for non-interactive/CI use)")
	initCmd.Flags().String("plan", "", "Plan slug for the new project (e.g. hobby, pro). Interactive picker when omitted; server default if plans can't be fetched.")
	initCmd.Flags().BoolVar(&initNoSite, "no-site", false, "Create the project without a site — databases, storage and auth only (skips the site and domain questions)")
	initCmd.Flags().StringVar(&initSite, "site", "", "Name of the site to create ('main' allowed); skips the site question")
	initCmd.Flags().StringVar(&initDomain, "domain", "", "Custom domain to attach to the new site; skips the domain question")
	rootCmd.AddCommand(initCmd)
}
