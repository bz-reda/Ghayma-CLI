package cmd

import (
	"fmt"
	"sort"
	"strings"

	"paas-cli/internal/api"

	"github.com/manifoldco/promptui"
)

// Pure auth-app points-pricing helpers for the CLI. authCostPreview is the ONE
// place the auth point formula lives client-side; it mirrors the backend
// paas-api/internal/points/pricing.go (AuthCost) EXACTLY so a preview can never
// disagree with the server's admission charge. Every number comes from the
// catalog at runtime — zero hardcoded pricing.

// authCostPreview computes an auth app's points footprint from the catalog:
//
//	cost = bracket.PointsCost + (twofa ? rates.TwoFAPoints : 0) + (sms ? bracket.SMSPoints : 0)
//
// The SMS add-on is priced from the SELECTED bracket (not a flat rate). Returns
// an error when the catalog is nil or the bracket slug is unknown.
func authCostPreview(cat *api.MarketplaceCatalog, bracketSlug string, twofa, sms bool) (int64, error) {
	if cat == nil {
		return 0, fmt.Errorf("no marketplace catalog")
	}
	b, ok := findAuthTier(cat, bracketSlug)
	if !ok {
		return 0, fmt.Errorf("unknown user bracket %q", bracketSlug)
	}
	cost := int64(b.PointsCost)
	if twofa {
		cost += cat.Rates.TwoFAPoints
	}
	if sms {
		cost += int64(b.SMSPoints)
	}
	return cost, nil
}

// findAuthTier looks a bracket slug up in the catalog.
func findAuthTier(cat *api.MarketplaceCatalog, slug string) (api.CatalogAuthTier, bool) {
	for _, t := range cat.AuthTiers {
		if t.Slug == slug {
			return t, true
		}
	}
	return api.CatalogAuthTier{}, false
}

// sortedAuthTiers returns the brackets ordered by Position so the picker lists
// smallest→largest regardless of server ordering.
func sortedAuthTiers(cat *api.MarketplaceCatalog) []api.CatalogAuthTier {
	out := append([]api.CatalogAuthTier(nil), cat.AuthTiers...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Position < out[j].Position })
	return out
}

// defaultAuthTier returns the lowest-Position bracket — the server's default
// when --users is omitted. Derived from the catalog rather than a hardcoded
// "1k".
func defaultAuthTier(cat *api.MarketplaceCatalog) (api.CatalogAuthTier, bool) {
	if cat == nil || len(cat.AuthTiers) == 0 {
		return api.CatalogAuthTier{}, false
	}
	best := cat.AuthTiers[0]
	for _, t := range cat.AuthTiers[1:] {
		if t.Position < best.Position {
			best = t
		}
	}
	return best, true
}

// authBracketSlugs returns the catalog's bracket slugs ordered by Position, for
// the "valid brackets" hint in validateAuthBracket.
func authBracketSlugs(cat *api.MarketplaceCatalog) []string {
	tiers := sortedAuthTiers(cat)
	slugs := make([]string, len(tiers))
	for i, t := range tiers {
		slugs[i] = t.Slug
	}
	return slugs
}

// validateAuthBracket guards the --users flag→slug value against the catalog.
// A blank value (server default) and a known slug pass; an unknown slug errors,
// listing the valid brackets. Only called when the catalog is present.
func validateAuthBracket(cat *api.MarketplaceCatalog, slug string) error {
	if slug == "" {
		return nil
	}
	if _, ok := findAuthTier(cat, slug); ok {
		return nil
	}
	return fmt.Errorf("unknown bracket %q; choose one of: %s", slug, strings.Join(authBracketSlugs(cat), ", "))
}

// bracketOrDefault names a bracket for user-facing copy, rendering a blank
// slug (server default) as "default".
func bracketOrDefault(slug string) string {
	if slug == "" {
		return "default"
	}
	return slug
}

// enabledFeatureList joins the features being turned on into a human phrase
// ("2FA", "SMS", or "2FA + SMS"). Returns "" when neither is set.
func enabledFeatureList(twofa, sms bool) string {
	var parts []string
	if twofa {
		parts = append(parts, "2FA")
	}
	if sms {
		parts = append(parts, "SMS")
	}
	return strings.Join(parts, " + ")
}

// authBracketLabel renders an interactive bracket choice, e.g.
// "10k — up to 10000 users · 3 pts".
func authBracketLabel(t api.CatalogAuthTier) string {
	return fmt.Sprintf("%s — up to %d users · %d pts", t.Slug, t.MaxUsers, t.PointsCost)
}

// authSMSLabel renders the SMS add-on price line from the selected bracket,
// e.g. "+3 pts, includes 500 msgs/mo".
func authSMSLabel(t api.CatalogAuthTier) string {
	return fmt.Sprintf("+%d pts, includes %d msgs/mo", t.SMSPoints, t.SMSIncludedMonthly)
}

// The interactive pickers are indirected through function variables so tests
// can substitute the promptui I/O and exercise the cancel/abort control flow.
var (
	promptAuthBracketFn = promptAuthBracket
	promptAuth2FAFn     = promptAuth2FA
	promptAuthSMSFn     = promptAuthSMS
)

// promptAuthSelections renders the catalog-driven bracket/2FA/SMS pickers for an
// interactive create. A promptui cancel (Ctrl-C) from any picker returns a
// non-nil error so the caller aborts WITHOUT creating an auth app — an empty
// brackets catalog is NOT a cancel and still degrades to the server default.
func promptAuthSelections(cat *api.MarketplaceCatalog) (string, bool, bool, error) {
	bracket, err := promptAuthBracketFn(cat)
	if err != nil {
		return "", false, false, err
	}
	twofa, err := promptAuth2FAFn(cat)
	if err != nil {
		return "", false, false, err
	}
	sms, err := promptAuthSMSFn(cat, bracket)
	if err != nil {
		return "", false, false, err
	}
	return bracket, twofa, sms, nil
}

func promptAuthBracket(cat *api.MarketplaceCatalog) (string, error) {
	tiers := sortedAuthTiers(cat)
	if len(tiers) == 0 {
		return "", nil
	}
	labels := make([]string, len(tiers))
	for i, t := range tiers {
		labels[i] = authBracketLabel(t)
	}
	sel := promptui.Select{Label: "Select a user-capacity bracket", Items: labels, Size: 10}
	idx, _, err := sel.Run()
	if err != nil {
		return "", err
	}
	return tiers[idx].Slug, nil
}

// promptAuth2FA offers a No/Yes choice (a Select, not a y/n confirm, so a "No"
// answer is a valid selection and can't be confused with a Ctrl-C cancel).
func promptAuth2FA(cat *api.MarketplaceCatalog) (bool, error) {
	items := []string{"No", fmt.Sprintf("Yes — +%d pts", cat.Rates.TwoFAPoints)}
	sel := promptui.Select{Label: "Enable two-factor auth (2FA)?", Items: items}
	idx, _, err := sel.Run()
	if err != nil {
		return false, err
	}
	return idx == 1, nil
}

// promptAuthSMS offers a No/Yes choice; the Yes line carries the SELECTED
// bracket's SMS price and included allowance.
func promptAuthSMS(cat *api.MarketplaceCatalog, bracketSlug string) (bool, error) {
	yes := "Yes"
	if t, ok := findAuthTier(cat, bracketSlug); ok {
		yes = "Yes — " + authSMSLabel(t)
	}
	items := []string{"No", yes}
	sel := promptui.Select{Label: "Enable SMS?", Items: items}
	idx, _, err := sel.Run()
	if err != nil {
		return false, err
	}
	return idx == 1, nil
}
