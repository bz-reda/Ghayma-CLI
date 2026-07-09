package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"paas-cli/internal/api"
)

// Pure DB points-pricing helpers for the CLI. dbCostPreview is the ONE place
// the database point formula lives client-side; it mirrors the backend
// paas-api/internal/points/pricing.go EXACTLY so a preview can never disagree
// with the server's admission charge. Every number comes from the catalog at
// runtime — zero hardcoded pricing.

// dbCeilDiv returns ceil(a/b); a non-positive divisor yields 0 (mirrors
// points.CeilDiv, defensive against a mis-set block-size rate).
func dbCeilDiv(a, b int64) int64 {
	if b <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

// dbDiskCost prices a DB disk per whole block, rounding up (mirrors
// points.DiskCost).
func dbDiskCost(diskGB, blockGB, blockPoints int64) int64 {
	return dbCeilDiv(diskGB, blockGB) * blockPoints
}

// dbBackupCost scales a DB's disk footprint by its backup multiplier (mirrors
// points.BackupCost). A 0 multiplier (weekly/free) costs nothing; otherwise the
// charge is at least one multiplier unit so a sub-block disk still pays.
func dbBackupCost(diskGB, backupBlockGB, multiplier int64) int64 {
	if multiplier == 0 {
		return 0
	}
	scaled := multiplier * dbCeilDiv(diskGB, backupBlockGB)
	if scaled < multiplier {
		return multiplier
	}
	return scaled
}

// dbCostPreview computes a database's points footprint from the catalog:
//
//	cost = tier.PointsCost + DiskCost(diskGB) + BackupCost(diskGB, backup)
//
// A blank backupSlug prices no backup component. Returns an error when the
// catalog is nil or a slug is not present.
func dbCostPreview(cat *api.MarketplaceCatalog, tierSlug string, diskGB int, backupSlug string) (int64, error) {
	if cat == nil {
		return 0, fmt.Errorf("no marketplace catalog")
	}
	tier, ok := findDBTier(cat, tierSlug)
	if !ok {
		return 0, fmt.Errorf("unknown database tier %q", tierSlug)
	}
	cost := int64(tier.PointsCost)
	cost += dbDiskCost(int64(diskGB), cat.Rates.DBBlockGB, cat.Rates.DBBlockPoints)
	if backupSlug != "" {
		bt, ok := findBackupTier(cat, backupSlug)
		if !ok {
			return 0, fmt.Errorf("unknown backup tier %q", backupSlug)
		}
		cost += dbBackupCost(int64(diskGB), cat.Rates.BackupBlockGB, int64(bt.Multiplier))
	}
	return cost, nil
}

// findDBTier / findBackupTier look a slug up in the catalog.
func findDBTier(cat *api.MarketplaceCatalog, slug string) (api.CatalogDBTier, bool) {
	for _, t := range cat.DBTiers {
		if t.Slug == slug {
			return t, true
		}
	}
	return api.CatalogDBTier{}, false
}

func findBackupTier(cat *api.MarketplaceCatalog, slug string) (api.CatalogBackupTier, bool) {
	for _, t := range cat.BackupTiers {
		if t.Slug == slug {
			return t, true
		}
	}
	return api.CatalogBackupTier{}, false
}

// dbTierSlugs lists the catalog's DB tier slugs ordered by Position, for the
// "available tiers" hint in validateDBTier.
func dbTierSlugs(cat *api.MarketplaceCatalog) []string {
	tiers := sortedDBTiers(cat)
	slugs := make([]string, len(tiers))
	for i, t := range tiers {
		slugs[i] = t.Slug
	}
	return slugs
}

// validateDBTier guards the --tier flag→slug value against the catalog (mirrors
// validateAuthBracket). A blank value (server default) and a known slug pass; an
// unknown slug errors, listing the available tiers. Only called when the catalog
// is present — a missing catalog fails soft and defers to the backend.
func validateDBTier(cat *api.MarketplaceCatalog, slug string) error {
	if slug == "" {
		return nil
	}
	if _, ok := findDBTier(cat, slug); ok {
		return nil
	}
	return fmt.Errorf("unknown tier %q; choose one of: %s", slug, strings.Join(dbTierSlugs(cat), ", "))
}

// defaultDBTier / defaultBackupTier return the lowest-Position row — the
// server's default is the smallest tier / the free weekly backup, both at
// Position 0. Derived from the catalog rather than a hardcoded "xs"/"weekly".
func defaultDBTier(cat *api.MarketplaceCatalog) (api.CatalogDBTier, bool) {
	if cat == nil || len(cat.DBTiers) == 0 {
		return api.CatalogDBTier{}, false
	}
	best := cat.DBTiers[0]
	for _, t := range cat.DBTiers[1:] {
		if t.Position < best.Position {
			best = t
		}
	}
	return best, true
}

func defaultBackupTier(cat *api.MarketplaceCatalog) (api.CatalogBackupTier, bool) {
	if cat == nil || len(cat.BackupTiers) == 0 {
		return api.CatalogBackupTier{}, false
	}
	best := cat.BackupTiers[0]
	for _, t := range cat.BackupTiers[1:] {
		if t.Position < best.Position {
			best = t
		}
	}
	return best, true
}

// dbTierLabel renders an interactive tier choice, e.g.
// "xs — 0.25 vCPU / 256 MB · 2 pts".
func dbTierLabel(t api.CatalogDBTier) string {
	return fmt.Sprintf("%s — %s vCPU / %d MB · %d pts", t.Slug, formatVCPU(t.CPULimitMilli), t.MemoryLimitMB, t.PointsCost)
}

// dbBackupLabel renders an interactive backup choice with the per-disk points
// delta, e.g. "daily — every 24h, keep 7 · +2 pts".
func dbBackupLabel(cat *api.MarketplaceCatalog, bt api.CatalogBackupTier, diskGB int) string {
	add := dbBackupCost(int64(diskGB), cat.Rates.BackupBlockGB, int64(bt.Multiplier))
	return fmt.Sprintf("%s — every %dh, keep %d · +%d pts", bt.Slug, bt.IntervalHours, bt.RetentionCount, add)
}

// formatVCPU converts a milli-CPU limit to a vCPU string with no trailing
// zeros (250 → "0.25", 1000 → "1", 2000 → "2").
func formatVCPU(milli int) string {
	return strconv.FormatFloat(float64(milli)/1000.0, 'g', -1, 64)
}

// formatReserveLine is the pre-submit cost line: "This database will reserve N
// pts", plus " · M remaining after" when a non-PAYG summary was fetchable. A
// nil summary (fetch failed) degrades to the reserve-only line — it never
// blocks the command.
func formatReserveLine(cost int64, summary *api.ProjectPointsSummary) string {
	line := fmt.Sprintf("This database will reserve %d pts", cost)
	if summary != nil && !summary.PAYG {
		line += fmt.Sprintf(" · %d remaining after", summary.Remaining-cost)
	}
	return line
}

// diskShrinkError returns a non-empty message when a resize target disk is
// below the current disk. Disk is GROW-ONLY (local-path PVCs can't shrink
// safely); the command aborts before hitting the API.
func diskShrinkError(currentGB, targetGB int) string {
	if targetGB < currentGB {
		return fmt.Sprintf("disk cannot shrink from %d GB to %d GB", currentGB, targetGB)
	}
	return ""
}
