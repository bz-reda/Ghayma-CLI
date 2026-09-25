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

// dbTypeMongoDB is the --type value for the engine gated per tier by the
// catalog's mongo_enabled.
const dbTypeMongoDB = "mongodb"

// dbTiersForEngine returns the catalog's DB tiers, ordered by Position, that can
// run the given engine. Every tier runs every engine except MongoDB, which the
// catalog gates per tier. Fail-soft: a catalog with no mongo-enabled tier at all
// degrades to the full ladder rather than an empty picker, leaving the server
// the final word.
func dbTiersForEngine(cat *api.MarketplaceCatalog, dbType string) []api.CatalogDBTier {
	tiers := sortedDBTiers(cat)
	if dbType != dbTypeMongoDB {
		return tiers
	}
	allowed := make([]api.CatalogDBTier, 0, len(tiers))
	for _, t := range tiers {
		if t.MongoAllowed() {
			allowed = append(allowed, t)
		}
	}
	if len(allowed) == 0 {
		return tiers
	}
	return allowed
}

// smallestMongoTier returns the lowest-Position tier that can run MongoDB.
func smallestMongoTier(cat *api.MarketplaceCatalog) (api.CatalogDBTier, bool) {
	for _, t := range sortedDBTiers(cat) {
		if t.MongoAllowed() {
			return t, true
		}
	}
	return api.CatalogDBTier{}, false
}

// mongoTierDisabledError mirrors the backend's MongoTierDisabledError wording
// (paas-api internal/databases/mongo_tier_gate.go) so the CLI-side rejection
// reads exactly like the 400 the server would have returned.
func mongoTierDisabledError(cat *api.MarketplaceCatalog, requested string) error {
	smallest, ok := smallestMongoTier(cat)
	if !ok {
		return fmt.Errorf("the %q tier is too small to run MongoDB reliably; choose a larger tier", requested)
	}
	return fmt.Errorf("MongoDB starts at the %q tier — the %q tier is too small to run MongoDB reliably; choose %q or larger",
		smallest.Slug, requested, smallest.Slug)
}

// validateDBTier guards the --tier flag→slug value against the catalog (mirrors
// validateAuthBracket). A blank value (server default) and a known slug pass; an
// unknown slug errors, listing the available tiers, and a known-but-mongo-
// disabled tier errors when the engine is mongodb. Only called when the catalog
// is present — a missing catalog fails soft and defers to the backend.
func validateDBTier(cat *api.MarketplaceCatalog, slug, dbType string) error {
	if slug == "" {
		return nil
	}
	tier, ok := findDBTier(cat, slug)
	if !ok {
		return fmt.Errorf("unknown tier %q; choose one of: %s", slug, strings.Join(dbTierSlugs(cat), ", "))
	}
	if dbType == dbTypeMongoDB && !tier.MongoAllowed() {
		return mongoTierDisabledError(cat, slug)
	}
	return nil
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

// defaultDBTierForEngine is the tier the server applies when none is given: the
// smallest tier that can run the engine. MongoDB's floor sits above the
// ladder's, so the preview must not quote the smallest tier overall.
func defaultDBTierForEngine(cat *api.MarketplaceCatalog, dbType string) (api.CatalogDBTier, bool) {
	if dbType == dbTypeMongoDB {
		if t, ok := smallestMongoTier(cat); ok {
			return t, true
		}
	}
	return defaultDBTier(cat)
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
