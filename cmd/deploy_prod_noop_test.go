package cmd

import (
	"strings"
	"testing"

	"paas-cli/internal/api"
	"paas-cli/internal/config"
)

// --prod after Environments (design D6). The flag is KEPT and still sent — it
// is in every script and every muscle memory — but it decides nothing: a
// deployment is a production one exactly when the site it targets is a
// production site. So the contract these tests hold is "one notice, zero
// behavior change".

func TestProdFlagNotice(t *testing.T) {
	got := prodFlagNotice("main", "production")
	if got != "ℹ️  --prod no longer changes anything: whether a deploy is production now follows the target site's environment (main is production)." {
		t.Errorf("notice = %q", got)
	}
	if got := prodFlagNotice("dev", "development"); !strings.Contains(got, "(dev is development)") {
		t.Errorf("a non-production target must be named as such: %q", got)
	}
	// A platform that does not carry environments yet: say what changed, claim
	// no kind.
	for _, got := range []string{prodFlagNotice("main", ""), prodFlagNotice("", "production")} {
		if !strings.HasSuffix(got, "environment.") {
			t.Errorf("notice without a known kind = %q; want the sentence to stop before claiming one", got)
		}
	}
}

// TestDeployProd_WarnsAndChangesNothing: the notice appears AND the request is
// byte-for-byte the one --prod always produced.
func TestDeployProd_WarnsAndChangesNothing(t *testing.T) {
	ts, seen, body := imageDeployStub(t, twoSites, `{"id":"dep-1","status":"live"}`)
	cliHome(t, ts.URL)
	noPrompt(t)
	fastPolls(t)

	out := runCLI(t, linkedDir(t), "deploy", "--image", "v1", "--prod")

	if !strings.Contains(out, "--prod no longer changes anything") || !strings.Contains(out, "(main is production)") {
		t.Errorf("output = %q; want the deprecation notice naming the target's kind", out)
	}
	if (*body)["is_production"] != true {
		t.Errorf("body = %v; the flag is a no-op for the USER, not a silently dropped field", *body)
	}
	if !containsPath(*seen, "POST /api/v1/projects/p1/sites/s1/deployments/image") {
		t.Errorf("requests = %v; the deploy itself is unchanged", *seen)
	}
}

// TestDeployWithoutProd_SaysNothing: the notice is about a flag that was typed.
func TestDeployWithoutProd_SaysNothing(t *testing.T) {
	ts, _, _ := imageDeployStub(t, twoSites, `{"id":"dep-1","status":"live"}`)
	cliHome(t, ts.URL)
	noPrompt(t)
	fastPolls(t)

	out := runCLI(t, linkedDir(t), "deploy", "--image", "v1")

	if strings.Contains(out, "--prod") {
		t.Errorf("output = %q; nothing to say about a flag nobody used", out)
	}
}

// TestBestEffortSiteKind: the lookup exists to label a notice, so it must never
// be able to fail a deploy — an unreadable site list falls back to the linked
// site's name with no kind.
func TestBestEffortSiteKind(t *testing.T) {
	ctx := &SiteContext{ProjectID: "p1", Site: SiteEntry{SiteID: "s1", SiteSlug: "main"}}

	ts, _ := sitesStub(t, twoSites)
	cliHome(t, ts.URL)
	slug, environment := bestEffortSiteKind(api.NewClient(config.Load()), ctx, "")
	if slug != "main" || environment != "production" {
		t.Errorf("live list → %q/%q; want main/production", slug, environment)
	}

	broken, _ := sitesStub(t, "")
	cliHome(t, broken.URL)
	slug, environment = bestEffortSiteKind(api.NewClient(config.Load()), &SiteContext{ProjectID: "p1", Site: SiteEntry{SiteSlug: "admin"}}, "")
	if slug != "admin" || environment != "" {
		t.Errorf("unreadable list → %q/%q; want the linked name and no claimed kind", slug, environment)
	}
}

// TestDeployProdFlag_IsStillAccepted pins that the flag was not removed: taking
// it away would break every script that carries it, which is the whole reason
// D6 chose a warned no-op over an error.
func TestDeployProdFlag_IsStillAccepted(t *testing.T) {
	flag := deployCmd.Flags().Lookup("prod")
	if flag == nil {
		t.Fatal("--prod must stay on deploy")
	}
	if flag.Shorthand != "p" {
		t.Errorf("--prod shorthand = %q; want p", flag.Shorthand)
	}
	if !strings.Contains(flag.Usage, "no longer needed") {
		t.Errorf("--prod usage = %q; the help must say it does nothing", flag.Usage)
	}
}
