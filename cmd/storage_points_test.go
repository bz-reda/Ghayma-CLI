package cmd

import (
	"strings"
	"testing"

	"paas-cli/internal/api"

	"github.com/manifoldco/promptui"
)

// storageFixtureCatalog is a representative catalog for object-storage pricing.
// Numbers are test inputs, not production pricing — the CLI hardcodes none.
// Obj block: 10 GB / 1 pt (matches the backend byte vectors' block/points).
func storageFixtureCatalog() *api.MarketplaceCatalog {
	return &api.MarketplaceCatalog{
		Rates: api.CatalogRates{ObjBlockGB: 10, ObjBlockPoints: 1},
	}
}

// storageCostPreview must agree with the backend StorageCostBytes at whole-GB
// boundaries. Vectors mirror paas-api/internal/points/pricing_test.go:
// StorageCostBytes(10GiB,10,1)=1, 12GiB=2, 10GiB+1=2 (→ 11GB=2), 0=0.
func TestStorageCostPreview_BackendVectors(t *testing.T) {
	cat := storageFixtureCatalog()
	cases := []struct {
		quotaGB int
		want    int64
	}{
		{10, 1},
		{12, 2},
		{11, 2},
		{0, 0},
	}
	for _, c := range cases {
		if got := storageCostPreview(cat, c.quotaGB); got != c.want {
			t.Errorf("storageCostPreview(%dGB) = %d; want %d", c.quotaGB, got, c.want)
		}
	}
}

// ObjBlockPoints multiplies per block (not just 1-pt blocks).
func TestStorageCostPreview_PointsMultiply(t *testing.T) {
	cat := &api.MarketplaceCatalog{Rates: api.CatalogRates{ObjBlockGB: 10, ObjBlockPoints: 5}}
	if got := storageCostPreview(cat, 25); got != 15 { // ceil(25/10)=3 blocks * 5
		t.Errorf("25GB @ block10/5pts = %d; want 15", got)
	}
}

// A nil catalog prices to 0 defensively (the caller only previews when the
// catalog is present, but the helper must not panic).
func TestStorageCostPreview_NilCatalog(t *testing.T) {
	if got := storageCostPreview(nil, 10); got != 0 {
		t.Errorf("nil catalog = %d; want 0", got)
	}
}

// The shared reserve line names the resource and shows the remaining-after tail
// only when a non-PAYG summary was fetchable.
func TestFormatReserveLineFor(t *testing.T) {
	line := formatReserveLineFor("bucket", 3, &api.ProjectPointsSummary{Remaining: 10, PAYG: false})
	if !strings.Contains(line, "This bucket will reserve 3 pts") {
		t.Errorf("bucket reserve line %q missing cost/noun", line)
	}
	if !strings.Contains(line, "7 remaining after") {
		t.Errorf("bucket reserve line %q missing remaining-after (10-3=7)", line)
	}

	// nil summary → reserve-only (never blocks).
	if l := formatReserveLineFor("auth app", 5, nil); !strings.Contains(l, "This auth app will reserve 5 pts") || strings.Contains(l, "remaining after") {
		t.Errorf("nil-summary line = %q; want reserve-only for auth app", l)
	}

	// PAYG → no remaining-after tail.
	if l := formatReserveLineFor("bucket", 5, &api.ProjectPointsSummary{PAYG: true}); strings.Contains(l, "remaining after") {
		t.Errorf("PAYG line = %q; must not show remaining-after", l)
	}
}

// swapStoragePickers snapshots the promptui-backed picker var and returns a
// restore fn, keeping tests hermetic.
func swapStoragePickers() func() {
	qf := promptStorageQuotaFn
	return func() { promptStorageQuotaFn = qf }
}

// A promptui cancel (Ctrl-C) from the quota picker must propagate as a non-nil
// error so the caller aborts WITHOUT creating a bucket at a default quota.
func TestPromptStorageQuota_CancelPropagates(t *testing.T) {
	defer swapStoragePickers()()
	promptStorageQuotaFn = func(*api.MarketplaceCatalog) (int, error) {
		return 0, promptui.ErrInterrupt
	}
	if _, err := promptStorageQuotaFn(storageFixtureCatalog()); err == nil {
		t.Fatal("expected a cancel error; nil would fall through to a create")
	}
}
