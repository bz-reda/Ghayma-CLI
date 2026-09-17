package cmd

import (
	"errors"
	"fmt"
	"strings"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

// Promotion (ENVIRONMENTS-DESIGN-2026-09-17 §4). "It works on dev, ship
// exactly that to prod" — without rebuilding, because a rebuild is not the
// tested artifact. What travels is one image: the source deployment's digest,
// mounted into the target's repository, re-admitted and rolled out there with
// the TARGET's own variables, connections, domains and tier.
//
// From here on it is an ordinary deployment, so it is polled, narrated and
// reported by the same waitForDeployment every deploy uses.

var (
	promoteFrom       string
	promoteDeployment string
	promoteSite       string
	promoteMessage    string
)

var promoteCmd = &cobra.Command{
	Use:   "promote --from <site>",
	Short: "Ship the image a site already runs onto another site",
	Long: `Promote the exact image one site is running onto another site.

Nothing is rebuilt: the source deployment's image is the artifact you tested,
and that is what is deployed. The target keeps its own environment variables,
connections, credentials, domains and tier — a promotion is an ordinary deploy
of a known-good image.

--from names the site to promote FROM (its current live deployment, or the one
named by --deployment). The target is the project's DEFAULT site — the
production one — unless --site names another; the target is always printed
before anything is sent.

Examples:
  ghayma promote --from dev
  ghayma promote --from staging --site main
  ghayma promote --from dev --deployment 6f1c… `,
	Args: cobra.NoArgs,
	Run:  runPromote,
}

// resolvePromoteTarget decides which site a promotion lands on, and returns the
// line that says so when the choice was not spelled out.
//
// The default is the project's DEFAULT site rather than the directory's linked
// site: promotion runs from a development context ("promote what I just tested
// to production"), where the linked site is the SOURCE — defaulting to it would
// make the common invocation a self-promotion the server refuses. The default
// site is the production environment by definition (design D2), so it is the
// only target the direction of the verb can mean. It is never silent: the
// notice names the site and its kind before the request goes out.
func resolvePromoteTarget(sites []api.Site, siteFlag string) (*api.Site, string, error) {
	if strings.TrimSpace(siteFlag) != "" {
		site, err := liveSiteFor(sites, siteFlag)
		return site, "", err
	}
	if len(sites) == 0 {
		return nil, "", errNoSite
	}
	for i := range sites {
		if sites[i].IsDefault {
			return &sites[i], defaultTargetNotice(sites[i]), nil
		}
	}
	// No site claims to be the default (an older server omits the field), so
	// there is nothing to infer from: ask rather than pick.
	return nil, "", fmt.Errorf("name the site to promote INTO with --site <slug> (available: %s)", strings.Join(siteSlugs(sites), ", "))
}

// defaultTargetNotice names the defaulted target, with its kind when the
// platform carries one.
func defaultTargetNotice(site api.Site) string {
	kind := ""
	if site.Environment != "" {
		kind = fmt.Sprintf(" (%s)", site.Environment)
	}
	return fmt.Sprintf("ℹ️  Target: the project's default site '%s'%s — pass --site <slug> to promote somewhere else.", site.Slug, kind)
}

// promoteHeadline is the line printed once the promotion is accepted: what came
// from where. The digest is the whole point — it is the proof that the tested
// artifact, not a rebuild of it, is what lands.
func promoteHeadline(p *api.Promotion, from, target string) string {
	source := p.SourceSite
	if source == "" {
		source = from
	}
	image := p.SourceImageDigest
	if image == "" {
		image = p.SourceImageRef
	}
	if image == "" {
		return fmt.Sprintf("🚀 Promoting %s → %s", source, target)
	}
	return fmt.Sprintf("🚀 Promoting %s → %s (image %s)", source, target, shortDigest(image))
}

// shortDigest trims a sha256 digest to the twelve hex characters everything
// else in the ecosystem shows, and leaves anything else alone.
func shortDigest(ref string) string {
	if hex := strings.TrimPrefix(ref, api.DigestPrefix); hex != ref && len(hex) > 12 {
		return api.DigestPrefix + hex[:12]
	}
	return ref
}

// promoteFailure renders the server's refusal. Every one of them is written for
// the customer, so the message is printed as it came; the 409 additionally gets
// the CLI's own way out, because the server's sentence names the API field
// rather than the flag.
func promoteFailure(err error, from string) string {
	var apiErr *api.APIError
	if errors.As(err, &apiErr) && apiErr.Message != "" {
		if apiErr.Status == 409 {
			return fmt.Sprintf("%s\n   Or promote a specific one: ghayma promote --from %s --deployment <id>", apiErr.Message, from)
		}
		return apiErr.Message
	}
	return fmt.Sprintf("Promotion failed: %v", err)
}

func runPromote(cmd *cobra.Command, args []string) {
	cfg := config.Load()
	if !cfg.LoggedIn() {
		failf("Please login first: ghayma login")
		return
	}
	from := strings.TrimSpace(promoteFrom)
	if from == "" {
		failf("name the site to promote FROM: ghayma promote --from <slug>")
		return
	}

	projectID, _, _, err := localConfig()
	if err != nil {
		failf("%v", err)
		return
	}

	client := api.NewClient(cfg)
	sites, err := client.ListSites(projectID)
	if err != nil {
		failf("Failed to list sites: %v", err)
		return
	}
	target, notice, err := resolvePromoteTarget(sites, promoteSite)
	if err != nil {
		reportSiteError(err)
		exitFn(1)
		return
	}
	if notice != "" {
		fmt.Println(notice)
	}

	p, err := client.PromoteSite(projectID, target.ID, from, strings.TrimSpace(promoteDeployment), promoteMessage)
	if err != nil {
		failf("%s", promoteFailure(err, from))
		return
	}

	fmt.Println(promoteHeadline(p, from, target.Slug))
	fmt.Printf("📦 Promotion queued (deployment: %s)\n", p.ID)
	fmt.Println("⏳ Checking the image and rolling it out...")
	waitForDeployment(client, p.ID)
}

func init() {
	promoteCmd.Flags().StringVar(&promoteFrom, "from", "", "Site to promote FROM, by name or slug (required)")
	promoteCmd.Flags().StringVar(&promoteDeployment, "deployment", "", "Deployment of that site to promote (default: its current live one)")
	promoteCmd.Flags().StringVar(&promoteSite, "site", "", "Site to promote INTO (default: the project's default site)")
	promoteCmd.Flags().StringVar(&promoteMessage, "message", "", "Message to label the resulting deployment with")
	rootCmd.AddCommand(promoteCmd)
}
