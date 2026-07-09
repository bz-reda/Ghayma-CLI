package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"paas-cli/internal/api"

	"github.com/manifoldco/promptui"
)

// Pure object-storage points-pricing helpers for the CLI. storageCostPreview is
// the ONE place the storage point formula lives client-side; it mirrors the
// backend paas-api/internal/points/pricing.go so a preview can never disagree
// with the server's admission charge. Every number comes from the catalog at
// runtime — zero hardcoded pricing.

// storageCostPreview prices a bucket's quota in points from a whole-GB figure:
// ceilDiv(quotaGB, obj_block_gb) * obj_block_points. This equals the backend's
// StorageCostBytes at whole-GB boundaries (the CLI sends size_mb = quotaGB*1024,
// so N GB is exactly N GiB and the byte ceiling reduces to the GB ceiling). It
// is the indicative marginal cost for THIS bucket's quota; the backend charges
// on the project-total quota (a 409 is rendered, never pre-blocked). A nil
// catalog prices to 0 defensively — the caller only previews when the catalog
// is present.
func storageCostPreview(cat *api.MarketplaceCatalog, quotaGB int) int64 {
	if cat == nil {
		return 0
	}
	return dbCeilDiv(int64(quotaGB), cat.Rates.ObjBlockGB) * cat.Rates.ObjBlockPoints
}

// formatReserveLineFor builds the pre-submit "This <noun> will reserve N pts"
// line, appending " · M remaining after" only when a non-PAYG summary was
// fetchable. Shared by storage + auth create; a nil summary (fetch failed)
// degrades to the reserve-only line and never blocks. (db create has its own
// copy in db_points.go pinned to the "database" wording.)
func formatReserveLineFor(noun string, cost int64, summary *api.ProjectPointsSummary) string {
	line := fmt.Sprintf("This %s will reserve %d pts", noun, cost)
	if summary != nil && !summary.PAYG {
		line += fmt.Sprintf(" · %d remaining after", summary.Remaining-cost)
	}
	return line
}

// promptStorageQuotaFn is indirected so tests can substitute the promptui I/O
// and exercise the cancel/abort control flow.
var promptStorageQuotaFn = promptStorageQuota

// promptStorageQuota asks for a bucket quota in GB, stepped by the catalog's
// obj_block_gb. A blank answer leaves it 0 → the server applies the plan's
// per-bucket default. A promptui cancel (Ctrl-C) returns a non-nil error so the
// caller aborts WITHOUT creating a bucket at a default quota.
func promptStorageQuota(cat *api.MarketplaceCatalog) (int, error) {
	block := int(cat.Rates.ObjBlockGB)
	if block <= 0 {
		block = 1
	}
	prompt := promptui.Prompt{
		Label: fmt.Sprintf("Storage quota in GB — steps of %d (blank for plan default)", block),
		Validate: func(s string) error {
			s = strings.TrimSpace(s)
			if s == "" {
				return nil
			}
			n, err := strconv.Atoi(s)
			if err != nil || n <= 0 {
				return fmt.Errorf("enter a positive whole number of GB")
			}
			if n%block != 0 {
				return fmt.Errorf("quota must be a multiple of %d GB", block)
			}
			return nil
		},
	}
	res, err := prompt.Run()
	if err != nil {
		return 0, err
	}
	res = strings.TrimSpace(res)
	if res == "" {
		return 0, nil
	}
	n, _ := strconv.Atoi(res)
	return n, nil
}
