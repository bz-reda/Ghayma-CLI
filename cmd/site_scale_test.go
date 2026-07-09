package cmd

import (
	"strings"
	"testing"

	"paas-cli/internal/api"
)

// appFixtureCatalog is a representative catalog with app tiers. The numbers are
// test inputs, not production pricing — the CLI hardcodes none, every value here
// is read at runtime. Tier "b" costs 6 pts so appCostPreview(b, 2) == 12 mirrors
// the backend pricing vector AppCost(6, 2) = 12.
func appFixtureCatalog() *api.MarketplaceCatalog {
	return &api.MarketplaceCatalog{
		AppTiers: []api.CatalogAppTier{
			{Slug: "a", CPULimitMilli: 250, MemoryLimitMB: 256, PointsCost: 3, Position: 0},
			{Slug: "b", CPULimitMilli: 500, MemoryLimitMB: 512, PointsCost: 6, Position: 1},
			{Slug: "c", CPULimitMilli: 1000, MemoryLimitMB: 1024, PointsCost: 12, Position: 2},
		},
	}
}

// appCostPreview must mirror the backend pricing.go AppCost EXACTLY:
// AppCost(tierPoints, replicas) = tierPoints * replicas. Vector copied verbatim
// from paas-api/internal/points/pricing_test.go so a divergence between the CLI
// preview and the server's admission charge is caught here.
func TestAppCostPreview_BackendVector(t *testing.T) {
	cat := appFixtureCatalog()

	// b (6) × 2 replicas = 12 — the backend AppCost(6, 2) vector.
	got, err := appCostPreview(cat, "b", 2)
	if err != nil {
		t.Fatalf("appCostPreview: %v", err)
	}
	if got != 12 {
		t.Errorf("appCostPreview(b, 2) = %d; want 12 (AppCost(6, 2))", got)
	}

	// One replica charges the tier's own cost.
	if got, _ := appCostPreview(cat, "c", 1); got != 12 {
		t.Errorf("appCostPreview(c, 1) = %d; want 12", got)
	}
	if got, _ := appCostPreview(cat, "a", 3); got != 9 {
		t.Errorf("appCostPreview(a, 3) = %d; want 9", got)
	}
}

func TestAppCostPreview_Errors(t *testing.T) {
	cat := appFixtureCatalog()
	if _, err := appCostPreview(cat, "nope", 1); err == nil {
		t.Error("unknown app tier must error")
	}
	if _, err := appCostPreview(nil, "b", 1); err == nil {
		t.Error("nil catalog must error")
	}
}

// resolveScaleValues applies the flag→request mapping: an unset --tier keeps the
// site's current tier, an unset --replicas keeps its current count. Because
// app_tier_slug is required on the wire and replicas defaults to the current
// value, the command always sends a complete request.
func TestResolveScaleValues_CurrentDefaulting(t *testing.T) {
	site := &api.Site{AppTierSlug: "b", Replicas: 3}

	// Neither flag set → both current values survive.
	if tier, rep := resolveScaleValues(site, "", 0, false); tier != "b" || rep != 3 {
		t.Errorf("no flags → %q/%d; want b/3 (current values)", tier, rep)
	}
	// Only --tier set → new tier, current replicas.
	if tier, rep := resolveScaleValues(site, "c", 0, false); tier != "c" || rep != 3 {
		t.Errorf("--tier c → %q/%d; want c/3", tier, rep)
	}
	// Only --replicas set → current tier, new replicas.
	if tier, rep := resolveScaleValues(site, "", 5, true); tier != "b" || rep != 5 {
		t.Errorf("--replicas 5 → %q/%d; want b/5", tier, rep)
	}
	// Both set → both new.
	if tier, rep := resolveScaleValues(site, "d", 2, true); tier != "d" || rep != 2 {
		t.Errorf("--tier d --replicas 2 → %q/%d; want d/2", tier, rep)
	}
	// An EXPLICIT --replicas 0 (replicasSet true) must pass through as 0 so the
	// downstream validateReplicas rejects it — never silently kept as current.
	if _, rep := resolveScaleValues(site, "", 0, true); rep != 0 {
		t.Errorf("explicit --replicas 0 → %d; want 0 (so it is rejected, not defaulted)", rep)
	}
}

// resolveScaleTarget picks which site to scale: an explicit --site name/slug
// wins; else the project config's site_id; else a lone single site. Ambiguous or
// unmatched inputs error so we never scale the wrong app.
func TestResolveScaleTarget(t *testing.T) {
	sites := []api.Site{
		{ID: "s1", Name: "Admin", Slug: "admin"},
		{ID: "s2", Name: "API", Slug: "api"},
	}

	t.Run("--site matches by slug", func(t *testing.T) {
		got, err := resolveScaleTarget(sites, "api", "")
		if err != nil || got.ID != "s2" {
			t.Fatalf("got %+v, err %v; want s2", got, err)
		}
	})
	t.Run("--site matches by name", func(t *testing.T) {
		got, err := resolveScaleTarget(sites, "Admin", "")
		if err != nil || got.ID != "s1" {
			t.Fatalf("got %+v, err %v; want s1", got, err)
		}
	})
	t.Run("--site unmatched errors", func(t *testing.T) {
		if _, err := resolveScaleTarget(sites, "nope", ""); err == nil {
			t.Error("unmatched --site must error")
		}
	})
	t.Run("config site_id matches by id", func(t *testing.T) {
		got, err := resolveScaleTarget(sites, "", "s2")
		if err != nil || got.ID != "s2" {
			t.Fatalf("got %+v, err %v; want s2", got, err)
		}
	})
	t.Run("config site_id unmatched errors", func(t *testing.T) {
		if _, err := resolveScaleTarget(sites, "", "ghost"); err == nil {
			t.Error("unmatched config site_id must error")
		}
	})
	t.Run("single site fallback", func(t *testing.T) {
		got, err := resolveScaleTarget(sites[:1], "", "")
		if err != nil || got.ID != "s1" {
			t.Fatalf("got %+v, err %v; want the lone site", got, err)
		}
	})
	t.Run("ambiguous errors", func(t *testing.T) {
		if _, err := resolveScaleTarget(sites, "", ""); err == nil {
			t.Error("multiple sites with no selector must error")
		}
	})
	t.Run("no sites errors", func(t *testing.T) {
		if _, err := resolveScaleTarget(nil, "", ""); err == nil {
			t.Error("empty site list must error")
		}
	})
}

// replicasBelowMinimum mirrors the backend ErrReplicasBelowMinimum reason. The
// CLI rejects sub-1 client-side and never even sends the request (pause /
// scale-to-zero is a future feature).
func TestReplicasBelowMinimum(t *testing.T) {
	for _, r := range []int{0, -1, -5} {
		msg := replicasBelowMinimum(r)
		if !strings.Contains(msg, "replicas must be >= 1") {
			t.Errorf("replicasBelowMinimum(%d) = %q; want the backend >= 1 reason", r, msg)
		}
		if !strings.Contains(msg, "pause / scale-to-zero is not yet supported") {
			t.Errorf("replicasBelowMinimum(%d) = %q; must carry the backend's exact wording", r, msg)
		}
	}
	for _, r := range []int{1, 2, 10} {
		if msg := replicasBelowMinimum(r); msg != "" {
			t.Errorf("replicasBelowMinimum(%d) = %q; want empty (allowed)", r, msg)
		}
	}
}

// formatScaleLine shows the new total footprint (tier × replicas = N pts) and,
// when a non-PAYG summary was fetchable, the remaining-after computed from the
// DELTA (new − old) — a resize only spends the difference, so subtracting the
// full new cost would understate what is left.
func TestFormatScaleLine(t *testing.T) {
	summary := &api.ProjectPointsSummary{Remaining: 74, PAYG: false}

	// new 12, delta 6 → 74 - 6 = 68 remaining after.
	line := formatScaleLine("b", 2, 12, 6, summary)
	if !strings.Contains(line, "b × 2 = 12 pts") {
		t.Errorf("scale line %q missing the tier × replicas headline", line)
	}
	if !strings.Contains(line, "68 remaining after") {
		t.Errorf("scale line %q missing delta-based remaining-after (74-6=68)", line)
	}

	// Fetch failure → nil summary → headline only, never blocks.
	if l := formatScaleLine("b", 2, 12, 6, nil); !strings.Contains(l, "12 pts") || strings.Contains(l, "remaining after") {
		t.Errorf("nil-summary line = %q; want headline only", l)
	}

	// PAYG has no budget → no remaining-after tail.
	if l := formatScaleLine("b", 2, 12, 6, &api.ProjectPointsSummary{PAYG: true}); strings.Contains(l, "remaining after") {
		t.Errorf("PAYG line = %q; must not show remaining-after", l)
	}
}
