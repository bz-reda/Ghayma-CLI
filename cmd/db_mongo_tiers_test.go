package cmd

import (
	"strings"
	"testing"

	"paas-cli/internal/api"
)

// MongoDB is gated per DB tier by the catalog's mongo_enabled: the create
// picker, the --tier guard and the reserve preview must all read that flag, or
// the CLI offers/prices a tier the server rejects with a 400.

// mongoGatedCatalog mirrors the live shape: the two smallest tiers cannot run
// MongoDB, the rest can. Numbers are test inputs, not production pricing.
func mongoGatedCatalog() *api.MarketplaceCatalog {
	cat := fixtureCatalog()
	cat.DBTiers = []api.CatalogDBTier{
		{Slug: "xs", CPULimitMilli: 250, MemoryLimitMB: 256, PointsCost: 2, Position: 0, MongoEnabled: boolPtr(false)},
		{Slug: "s", CPULimitMilli: 500, MemoryLimitMB: 1024, PointsCost: 6, Position: 1, MongoEnabled: boolPtr(false)},
		{Slug: "m", CPULimitMilli: 1000, MemoryLimitMB: 2048, PointsCost: 12, Position: 2, MongoEnabled: boolPtr(true)},
		{Slug: "l", CPULimitMilli: 2000, MemoryLimitMB: 4096, PointsCost: 20, Position: 3, MongoEnabled: boolPtr(true)},
	}
	return cat
}

// noMongoCatalog has the flag present but false everywhere — a misconfigured
// catalog the CLI must not turn into an empty picker.
func noMongoCatalog() *api.MarketplaceCatalog {
	cat := mongoGatedCatalog()
	for i := range cat.DBTiers {
		cat.DBTiers[i].MongoEnabled = boolPtr(false)
	}
	return cat
}

func tierSlugsOf(tiers []api.CatalogDBTier) []string {
	out := make([]string, len(tiers))
	for i, t := range tiers {
		out[i] = t.Slug
	}
	return out
}

func TestDBTiersForEngine(t *testing.T) {
	cases := []struct {
		name   string
		cat    *api.MarketplaceCatalog
		dbType string
		want   []string
	}{
		{"postgres sees the whole ladder", mongoGatedCatalog(), "postgres", []string{"xs", "s", "m", "l"}},
		{"mongodb sees only mongo-enabled tiers", mongoGatedCatalog(), dbTypeMongoDB, []string{"m", "l"}},
		// An older backend omits mongo_enabled entirely (nil) — behave as today.
		{"absent flag means allowed", fixtureCatalog(), dbTypeMongoDB, []string{"xs", "s", "m"}},
		// Fail-soft: never hand the picker an empty list.
		{"no enabled tier degrades to the full ladder", noMongoCatalog(), dbTypeMongoDB, []string{"xs", "s", "m", "l"}},
		{"nil catalog", nil, dbTypeMongoDB, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := tierSlugsOf(dbTiersForEngine(c.cat, c.dbType))
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("dbTiersForEngine = %v; want %v", got, c.want)
			}
		})
	}
}

// An explicit --tier that the engine cannot run fails CLI-side with the same
// wording the server uses, instead of a round-trip 400.
func TestValidateDBTier_MongoGate(t *testing.T) {
	gated := mongoGatedCatalog()

	cases := []struct {
		name     string
		cat      *api.MarketplaceCatalog
		slug     string
		dbType   string
		wantErr  bool
		contains []string
	}{
		{"mongo on a disabled tier is rejected", gated, "xs", dbTypeMongoDB, true,
			[]string{`MongoDB starts at the "m" tier`, `the "xs" tier is too small to run MongoDB reliably`, `choose "m" or larger`}},
		{"mongo on an enabled tier passes", gated, "m", dbTypeMongoDB, false, nil},
		{"postgres on the same tier passes", gated, "xs", "postgres", false, nil},
		{"blank tier defers to the server", gated, "", dbTypeMongoDB, false, nil},
		{"unknown slug still reports unknown", gated, "xxl", dbTypeMongoDB, true, []string{"unknown tier", "xxl"}},
		{"absent flag means allowed", fixtureCatalog(), "xs", dbTypeMongoDB, false, nil},
		{"no enabled tier degrades to the generic remedy", noMongoCatalog(), "xs", dbTypeMongoDB, true,
			[]string{"too small to run MongoDB reliably", "choose a larger tier"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateDBTier(c.cat, c.slug, c.dbType)
			if c.wantErr != (err != nil) {
				t.Fatalf("validateDBTier(%q, %q) error = %v; wantErr %v", c.slug, c.dbType, err, c.wantErr)
			}
			for _, want := range c.contains {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err.Error(), want)
				}
			}
		})
	}
}

// The reserve preview for a tier-less create must quote the tier the server
// would actually apply — for mongo that is the smallest mongo-enabled tier,
// not the ladder's floor.
func TestDefaultDBTierForEngine(t *testing.T) {
	cases := []struct {
		name   string
		cat    *api.MarketplaceCatalog
		dbType string
		want   string
	}{
		{"postgres defaults to the smallest tier", mongoGatedCatalog(), "postgres", "xs"},
		{"mongodb defaults to the smallest mongo-enabled tier", mongoGatedCatalog(), dbTypeMongoDB, "m"},
		{"absent flag defaults to the smallest tier", fixtureCatalog(), dbTypeMongoDB, "xs"},
		{"no enabled tier falls back to the smallest tier", noMongoCatalog(), dbTypeMongoDB, "xs"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tier, ok := defaultDBTierForEngine(c.cat, c.dbType)
			if !ok {
				t.Fatal("expected a default tier")
			}
			if tier.Slug != c.want {
				t.Errorf("defaultDBTierForEngine(%q) = %q; want %q", c.dbType, tier.Slug, c.want)
			}
		})
	}

	if _, ok := defaultDBTierForEngine(&api.MarketplaceCatalog{}, dbTypeMongoDB); ok {
		t.Error("an empty catalog has no default tier")
	}

	// The preview prices that default — mongo must not quote the xs cost.
	cat := mongoGatedCatalog()
	mongoDefault, _ := defaultDBTierForEngine(cat, dbTypeMongoDB)
	cost, err := dbCostPreview(cat, mongoDefault.Slug, 0, "")
	if err != nil {
		t.Fatalf("dbCostPreview: %v", err)
	}
	if cost != 12 {
		t.Errorf("mongo tier-less preview = %d pts; want 12 (the m tier)", cost)
	}
}

// The engine must reach the tier picker — that wiring is the whole bug.
func TestPromptDBSelections_PassesEngineToTierPicker(t *testing.T) {
	defer swapPickers()()

	gotType := ""
	promptDBTierFn = func(cat *api.MarketplaceCatalog, dbType string) (string, error) {
		gotType = dbType
		tiers := dbTiersForEngine(cat, dbType)
		return tiers[0].Slug, nil
	}
	promptDBDiskFn = func(*api.MarketplaceCatalog) (int, error) { return 0, nil }
	promptDBBackupFn = func(*api.MarketplaceCatalog, int) (string, error) { return "weekly", nil }

	tier, _, _, err := promptDBSelections(mongoGatedCatalog(), dbTypeMongoDB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotType != dbTypeMongoDB {
		t.Errorf("tier picker got engine %q; want %q", gotType, dbTypeMongoDB)
	}
	if tier != "m" {
		t.Errorf("preselected tier = %q; want the smallest mongo-capable tier m", tier)
	}
}
