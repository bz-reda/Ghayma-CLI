package cmd

import (
	"fmt"

	"paas-cli/internal/api"
)

// Pure app points-pricing + resolution helpers for `site scale`. appCostPreview
// is the ONE place the app point formula lives client-side; it mirrors the
// backend paas-api/internal/points/pricing.go AppCost EXACTLY so a preview can
// never disagree with the server's admission charge. Every number comes from the
// catalog at runtime — zero hardcoded pricing.

// appCostPreview prices a running app: tier.PointsCost × replicas (mirrors
// points.AppCost). Returns an error when the catalog is nil or the tier slug is
// not present.
func appCostPreview(cat *api.MarketplaceCatalog, tierSlug string, replicas int) (int64, error) {
	if cat == nil {
		return 0, fmt.Errorf("no marketplace catalog")
	}
	tier, ok := findAppTier(cat, tierSlug)
	if !ok {
		return 0, fmt.Errorf("unknown app tier %q", tierSlug)
	}
	return int64(tier.PointsCost) * int64(replicas), nil
}

// findAppTier looks a slug up in the catalog's app tiers.
func findAppTier(cat *api.MarketplaceCatalog, slug string) (api.CatalogAppTier, bool) {
	for _, t := range cat.AppTiers {
		if t.Slug == slug {
			return t, true
		}
	}
	return api.CatalogAppTier{}, false
}

// resolveScaleTarget picks which site to scale: an explicit --site name/slug
// wins; otherwise the project config's site_id; otherwise a lone single site.
// Ambiguous or unmatched inputs return a clear error so we never scale the wrong
// app.
func resolveScaleTarget(sites []api.Site, siteFlag, configSiteID string) (*api.Site, error) {
	if len(sites) == 0 {
		return nil, fmt.Errorf("no sites in this project")
	}
	if siteFlag != "" {
		for i := range sites {
			if sites[i].Slug == siteFlag || sites[i].Name == siteFlag {
				return &sites[i], nil
			}
		}
		return nil, fmt.Errorf("site %q not found in this project — run 'ghayma site list'", siteFlag)
	}
	if configSiteID != "" {
		for i := range sites {
			if sites[i].ID == configSiteID {
				return &sites[i], nil
			}
		}
		return nil, fmt.Errorf("the project's active site_id is not in this project — pass --site <name>")
	}
	if len(sites) == 1 {
		return &sites[0], nil
	}
	return nil, fmt.Errorf("multiple sites in this project — pass --site <name> to choose one")
}

// resolveScaleValues applies the flag→request mapping: an unset --tier keeps the
// site's current tier; an unset --replicas keeps its current count. replicasSet
// comes from cmd.Flags().Changed so an explicit --replicas 0 is distinguishable
// from "unset" (0 passes through and is later rejected by replicasBelowMinimum).
func resolveScaleValues(site *api.Site, tierFlag string, replicasFlag int, replicasSet bool) (tier string, replicas int) {
	tier = site.AppTierSlug
	if tierFlag != "" {
		tier = tierFlag
	}
	replicas = site.Replicas
	if replicasSet {
		replicas = replicasFlag
	}
	return tier, replicas
}

// replicasBelowMinimum returns the backend's exact ErrReplicasBelowMinimum
// reason when replicas < 1, or "" when the count is allowed. Pause /
// scale-to-zero is not yet supported, so the CLI rejects sub-1 client-side and
// never sends the request (mirrors internal/sites points_admission.go).
func replicasBelowMinimum(replicas int) string {
	if replicas < 1 {
		return "replicas must be >= 1 (pause / scale-to-zero is not yet supported)"
	}
	return ""
}

// formatScaleLine is the pre-submit cost line: "<tier> × <replicas> = N pts",
// plus " · M remaining after" when a non-PAYG summary was fetchable. Because
// scale is a RESIZE (unlike the create previews), the remaining-after subtracts
// the DELTA (new − old), not the full new cost — an app that already spends its
// old cost only pays the difference. A nil summary (fetch failed) degrades to
// the headline only and never blocks.
func formatScaleLine(tierSlug string, replicas int, newCost, deltaCost int64, summary *api.ProjectPointsSummary) string {
	line := fmt.Sprintf("%s × %d = %d pts", tierSlug, replicas, newCost)
	if summary != nil && !summary.PAYG {
		line += fmt.Sprintf(" · %d remaining after", summary.Remaining-deltaCost)
	}
	return line
}

// displayTier renders a tier slug for display, guarding the (rare) case of an
// app whose tier has not been enveloped yet — an empty slug shows as "(unset)".
func displayTier(slug string) string {
	if slug == "" {
		return "(unset)"
	}
	return slug
}

// unenvelopedTierError guards `site scale` on an app that has no compute tier
// yet (currentTier == "") when the user did not pass --tier. Sending the
// resolved empty tier would fail the backend's app_tier_slug binding:"required"
// with a raw gin 400, so we reject early with an actionable message. When --tier
// is set (tierSet true) — for any current state — the guard passes and the scale
// proceeds unchanged. Returns "" when allowed.
func unenvelopedTierError(currentTier string, tierSet bool) string {
	if currentTier == "" && !tierSet {
		return "this app has no compute tier yet — specify --tier <tier> to set one"
	}
	return ""
}
