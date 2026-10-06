package cmd

import (
	"errors"
	"strings"
	"testing"

	"paas-cli/internal/api"

	"github.com/manifoldco/promptui"
)

// fixtureCatalog is a representative marketplace catalog. Numbers are test
// inputs, not production pricing — the CLI hardcodes none, every value here is
// read at runtime. Rates: db block 10 GB / 5 pts, backup block 10 GB.
func fixtureCatalog() *api.MarketplaceCatalog {
	return &api.MarketplaceCatalog{
		DBTiers: []api.CatalogDBTier{
			{Slug: "xs", CPULimitMilli: 250, MemoryLimitMB: 256, PointsCost: 2, Position: 0},
			{Slug: "s", CPULimitMilli: 500, MemoryLimitMB: 1024, PointsCost: 6, Position: 1},
			{Slug: "m", CPULimitMilli: 1000, MemoryLimitMB: 2048, PointsCost: 12, Position: 2},
		},
		BackupTiers: []api.CatalogBackupTier{
			{Slug: "weekly", IntervalHours: 168, RetentionCount: 4, Multiplier: 0, Position: 0},
			{Slug: "daily", IntervalHours: 24, RetentionCount: 7, Multiplier: 1, Position: 1},
			{Slug: "sixhourly", IntervalHours: 6, RetentionCount: 8, Multiplier: 3, Position: 2},
		},
		Rates: api.CatalogRates{DBBlockGB: 10, DBBlockPoints: 5, BackupBlockGB: 10},
	}
}

// The pure DB pricing primitives must mirror the backend pricing.go EXACTLY.
// Vectors are copied verbatim from paas-api/internal/points/pricing_test.go so
// a divergence between CLI preview and server admission is caught here.
func TestDBPricingPrimitives_BackendVectors(t *testing.T) {
	cases := []struct {
		name string
		got  int64
		want int64
	}{
		{"ceildiv zero divisor", dbCeilDiv(5, 0), 0},
		{"disk exact block", dbDiskCost(10, 1, 1), 10},
		{"disk rounds up", dbDiskCost(11, 10, 1), 2},
		{"backup weekly free", dbBackupCost(50, 10, 0), 0},
		{"backup daily 2GB min", dbBackupCost(2, 10, 1), 1},
		{"backup 6h 50GB", dbBackupCost(50, 10, 3), 15},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, c.got, c.want)
		}
	}
}

// dbCostPreview composes tier + disk + backup from the catalog. It is the ONE
// place the DB point formula lives in the CLI.
func TestDBCostPreview_Composition(t *testing.T) {
	cat := fixtureCatalog()

	// s (6) + DiskCost(20,10,5)=10 + BackupCost(20,10,daily=1)=2 = 18
	got, err := dbCostPreview(cat, "s", 20, "daily")
	if err != nil {
		t.Fatalf("dbCostPreview: %v", err)
	}
	if got != 18 {
		t.Errorf("s/20GB/daily = %d; want 18", got)
	}

	// xs (2) + DiskCost(0)=0 + BackupCost(weekly mult 0)=0 = 2
	got, err = dbCostPreview(cat, "xs", 0, "weekly")
	if err != nil {
		t.Fatalf("dbCostPreview xs: %v", err)
	}
	if got != 2 {
		t.Errorf("xs/0/weekly = %d; want 2", got)
	}

	// A blank backup slug prices no backup component (tier + disk only).
	got, err = dbCostPreview(cat, "m", 10, "")
	if err != nil {
		t.Fatalf("dbCostPreview m: %v", err)
	}
	if got != 12+5 {
		t.Errorf("m/10/none = %d; want %d", got, 12+5)
	}
}

func TestDBCostPreview_UnknownSlugs(t *testing.T) {
	cat := fixtureCatalog()
	if _, err := dbCostPreview(cat, "nope", 10, "weekly"); err == nil {
		t.Error("unknown tier must error")
	}
	if _, err := dbCostPreview(cat, "s", 10, "nope"); err == nil {
		t.Error("unknown backup tier must error")
	}
	if _, err := dbCostPreview(nil, "s", 10, "weekly"); err == nil {
		t.Error("nil catalog must error")
	}
}

// validateDBTier guards the --tier flag value against the catalog (mirrors
// validateAuthBracket): a blank value (server default) and a known slug pass; an
// unknown slug errors, naming the available tiers. Only reached when the catalog
// is present — a missing catalog fails soft and skips validation.
func TestValidateDBTier(t *testing.T) {
	cat := fixtureCatalog()

	if err := validateDBTier(cat, "", "postgres"); err != nil {
		t.Errorf("blank tier (server default) must pass, got %v", err)
	}
	if err := validateDBTier(cat, "s", "postgres"); err != nil {
		t.Errorf("known tier s must pass, got %v", err)
	}

	err := validateDBTier(cat, "xl", "postgres")
	if err == nil {
		t.Fatal("unknown tier must error")
	}
	// The error must name the bad slug and list the available tiers.
	for _, want := range []string{"xl", "xs", "s", "m"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q (bad slug + available tiers)", err.Error(), want)
		}
	}
}

// The interactive tier line renders vCPU / memory / points from the catalog.
func TestDBTierLabel(t *testing.T) {
	label := dbTierLabel(api.CatalogDBTier{Slug: "xs", CPULimitMilli: 250, MemoryLimitMB: 256, PointsCost: 2})
	for _, want := range []string{"xs", "0.25 vCPU", "256 MB", "2 pts"} {
		if !strings.Contains(label, want) {
			t.Errorf("tier label %q missing %q", label, want)
		}
	}
	// A full vCPU renders without trailing zeros.
	if l := dbTierLabel(api.CatalogDBTier{Slug: "m", CPULimitMilli: 1000, MemoryLimitMB: 2048, PointsCost: 12}); !strings.Contains(l, "1 vCPU") {
		t.Errorf("1000 milli should render '1 vCPU', got %q", l)
	}
}

// The backup select shows the per-disk points delta computed from the formula.
func TestDBBackupLabel(t *testing.T) {
	cat := fixtureCatalog()
	daily, _ := findBackupTier(cat, "daily")
	if l := dbBackupLabel(cat, daily, 20); !strings.Contains(l, "daily") || !strings.Contains(l, "+2 pts") {
		t.Errorf("daily/20GB label = %q; want it to name daily and +2 pts", l)
	}
	weekly, _ := findBackupTier(cat, "weekly")
	if l := dbBackupLabel(cat, weekly, 20); !strings.Contains(l, "+0 pts") {
		t.Errorf("weekly label = %q; want +0 pts", l)
	}
}

// The reserve line always shows the cost; the "remaining after" tail only when
// a (non-PAYG) summary was fetchable.
func TestFormatReserveLine(t *testing.T) {
	line := formatReserveLine(6, &api.ProjectPointsSummary{Remaining: 18, PAYG: false})
	if !strings.Contains(line, "reserve 6 pts") {
		t.Errorf("reserve line %q missing cost", line)
	}
	if !strings.Contains(line, "12 remaining after") {
		t.Errorf("reserve line %q missing remaining-after (18-6=12)", line)
	}

	// Fetch failure → nil summary → reserve-only, never blocks.
	if line := formatReserveLine(6, nil); !strings.Contains(line, "reserve 6 pts") || strings.Contains(line, "remaining after") {
		t.Errorf("nil-summary line = %q; want reserve-only", line)
	}

	// PAYG has no budget → no remaining-after tail.
	if line := formatReserveLine(6, &api.ProjectPointsSummary{PAYG: true}); strings.Contains(line, "remaining after") {
		t.Errorf("PAYG line = %q; must not show remaining-after", line)
	}
}

// formatMarketplaceError renders each MarketplaceError Kind per the Global
// Constraints. Shared across db create + resize (and Tasks 4 & 5) so the copy
// never drifts.
func TestFormatMarketplaceError_Kinds(t *testing.T) {
	// insufficient → shortfall message + the two explicit paths. No PAYG:
	// the API refuses it until the PAYG relaunch (2026-09-30).
	insuf := formatMarketplaceError(&api.MarketplaceError{
		Kind:    "insufficient",
		Message: "creating this database would exceed your plan's points budget",
	})
	for _, want := range []string{"points budget", "Upgrade", "Free up"} {
		if !strings.Contains(insuf, want) {
			t.Errorf("insufficient render missing %q, got:\n%s", want, insuf)
		}
	}
	if l := strings.ToLower(insuf); strings.Contains(l, "payg") || strings.Contains(l, "pay-as-you-go") {
		t.Errorf("insufficient render must not suggest pay-as-you-go, got:\n%s", insuf)
	}

	// capacity → contact-support, NEVER an upsell.
	cap := formatMarketplaceError(&api.MarketplaceError{Kind: "capacity", Message: "platform capacity is temporarily exhausted"})
	if cap != "Ghayma is at capacity — contact support" {
		t.Errorf("capacity render = %q; want the exact contact-support line", cap)
	}
	if strings.Contains(strings.ToLower(cap), "upgrade") || strings.Contains(strings.ToLower(cap), "payg") {
		t.Errorf("capacity render must never upsell, got: %q", cap)
	}

	// maxtier → reuse the server Message (it already names the tiers) + an
	// upgrade-to-unlock nudge.
	mt := formatMarketplaceError(&api.MarketplaceError{
		Kind:    "maxtier",
		Message: `requested database tier "l" (Large) exceeds the plan's maximum "m" (Medium)`,
	})
	if !strings.Contains(mt, "exceeds the plan's maximum") {
		t.Errorf("maxtier render must carry the server message, got:\n%s", mt)
	}
	if !strings.Contains(strings.ToLower(mt), "upgrade") {
		t.Errorf("maxtier render should nudge an upgrade, got:\n%s", mt)
	}

	// A non-marketplace error renders its raw text.
	if got := formatMarketplaceError(errors.New("some raw failure")); got != "some raw failure" {
		t.Errorf("plain error render = %q; want raw text", got)
	}
}

// swapPickers snapshots the promptui-backed picker vars and returns a restore
// fn, keeping tests hermetic (the real pickers are put back afterwards).
func swapPickers() func() {
	tf, df, bf := promptDBTierFn, promptDBDiskFn, promptDBBackupFn
	return func() {
		promptDBTierFn, promptDBDiskFn, promptDBBackupFn = tf, df, bf
	}
}

// A promptui cancel (Ctrl-C = promptui.ErrInterrupt) from ANY interactive
// picker must propagate as a non-nil error so the caller aborts WITHOUT
// creating a database. This pins the fix for the swallow-error bug where a
// cancel returned ""/0 and fell through to a smallest-tier create. It also
// verifies short-circuiting: pickers after the cancelled one never run.
func TestPromptDBSelections_CancelAborts(t *testing.T) {
	for _, failStage := range []string{"tier", "disk", "backup"} {
		t.Run(failStage+" cancel", func(t *testing.T) {
			defer swapPickers()()

			diskCalled, backupCalled := false, false

			promptDBTierFn = func(*api.MarketplaceCatalog, string) (string, error) {
				if failStage == "tier" {
					return "", promptui.ErrInterrupt
				}
				return "s", nil
			}
			promptDBDiskFn = func(*api.MarketplaceCatalog) (int, error) {
				diskCalled = true
				if failStage == "disk" {
					return 0, promptui.ErrInterrupt
				}
				return 20, nil
			}
			promptDBBackupFn = func(*api.MarketplaceCatalog, int) (string, error) {
				backupCalled = true
				if failStage == "backup" {
					return "", promptui.ErrInterrupt
				}
				return "daily", nil
			}

			tier, disk, backup, err := promptDBSelections(fixtureCatalog(), "postgres")
			if err == nil {
				t.Fatal("expected a cancel error; nil would fall through to a create")
			}
			if tier != "" || disk != 0 || backup != "" {
				t.Errorf("cancel must return zero selections, got %q/%d/%q", tier, disk, backup)
			}
			switch failStage {
			case "tier":
				if diskCalled || backupCalled {
					t.Error("tier cancel must short-circuit before disk/backup pickers")
				}
			case "disk":
				if backupCalled {
					t.Error("disk cancel must short-circuit before backup picker")
				}
			}
		})
	}
}

// The happy path composes all three picker results in order.
func TestPromptDBSelections_AllSucceed(t *testing.T) {
	defer swapPickers()()
	promptDBTierFn = func(*api.MarketplaceCatalog, string) (string, error) { return "m", nil }
	promptDBDiskFn = func(*api.MarketplaceCatalog) (int, error) { return 30, nil }
	promptDBBackupFn = func(*api.MarketplaceCatalog, int) (string, error) { return "weekly", nil }

	tier, disk, backup, err := promptDBSelections(fixtureCatalog(), "postgres")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tier != "m" || disk != 30 || backup != "weekly" {
		t.Errorf("got %q/%d/%q; want m/30/weekly", tier, disk, backup)
	}
}

// dbCreated is the create's 201 for shop-db; connections is the
// `,"connections":{...}` tail, or "" for a server that predates it.
func dbCreated(connections string) string {
	return `{"database":{"id":"d1","name":"shop-db","type":"postgres","host":"shop-db.internal","port":5432,"status":"provisioning"}` + connections + `}`
}

func TestDBCreate_SiteFlagsSendTheChosenSites(t *testing.T) {
	forceStdin(t, true)
	noSiteQuestions(t)
	stub := createStub(t, shopSites, dbCreated(`,"connections":{"connected":["s2"],"pending":["s1"],"failed":[]}`))
	cliHome(t, stub.URL)

	out := runCLI(t, linkedDir(t), "db", "create", "shop-db", "--site", "main", "--site", "admin")
	if got := stub.sentSites(); got != `["s1","s2"]` {
		t.Errorf("connect_site_ids = %s; want [\"s1\",\"s2\"]", got)
	}
	for _, want := range []string{
		"Created postgres database 'shop-db'",
		"Connecting to 'main' once it is ready to accept connections",
		"Connected to 'admin'",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestDBCreate_SiteWithNoConnectRefusedBeforeAnyCall(t *testing.T) {
	forceStdin(t, true)
	noSiteQuestions(t)
	stub := createStub(t, shopSites, dbCreated(""))
	cliHome(t, stub.URL)

	out := runCLI(t, linkedDir(t), "db", "create", "shop-db", "--site", "main", "--no-connect")
	if !strings.Contains(out, "--site and --no-connect") || lastExitCode != 1 {
		t.Errorf("want the refusal and exit 1, got %d:\n%s", lastExitCode, out)
	}
	if calls := stub.seen(); len(calls) != 0 {
		t.Errorf("the refusal must come before any call, got %v", calls)
	}
}

func TestDBCreate_UnknownSiteNeverCreates(t *testing.T) {
	forceStdin(t, false)
	stub := createStub(t, shopSites, dbCreated(""))
	cliHome(t, stub.URL)

	out := runCLI(t, linkedDir(t), "db", "create", "shop-db", "--site", "nope")
	if !strings.Contains(out, `"nope"`) || !strings.Contains(out, "main, admin, legacy") || lastExitCode != 1 {
		t.Errorf("want the unknown site, the project's sites and exit 1, got %d:\n%s", lastExitCode, out)
	}
	if stub.created() {
		t.Errorf("an unknown site must stop the create, got %v", stub.seen())
	}
}

func TestDBCreate_TerminalAnswersAreSent(t *testing.T) {
	forceStdin(t, true)
	answerSiteQuestions(t, false, true, false)
	stub := createStub(t, shopSites, dbCreated(`,"connections":{"connected":[],"pending":["s2"],"failed":[]}`))
	cliHome(t, stub.URL)

	out := runCLI(t, linkedDir(t), "db", "create", "shop-db")
	if got := stub.sentSites(); got != `["s2"]` {
		t.Errorf("connect_site_ids = %s; want [\"s2\"]", got)
	}
	if !strings.Contains(out, "Connecting to 'admin' once it is ready to accept connections") {
		t.Errorf("missing the pending line:\n%s", out)
	}
}

func TestDBCreate_CancelledQuestionCreatesNothing(t *testing.T) {
	forceStdin(t, true)
	orig := promptConnectSiteFn
	t.Cleanup(func() { promptConnectSiteFn = orig })
	promptConnectSiteFn = func(string, bool) (bool, error) { return false, promptui.ErrInterrupt }
	stub := createStub(t, shopSites, dbCreated(""))
	cliHome(t, stub.URL)

	out := runCLI(t, linkedDir(t), "db", "create", "shop-db")
	if !strings.Contains(out, "Cancelled") {
		t.Errorf("want the cancel line:\n%s", out)
	}
	if stub.created() {
		t.Errorf("a cancelled question must not create, got %v", stub.seen())
	}
}

// An answer that accounts for none of the chosen sites says where to check.
func TestDBCreate_UnreadableResultSaysWhereToCheck(t *testing.T) {
	forceStdin(t, false)
	stub := createStub(t, shopSites, dbCreated(`,"connections":{}`))
	cliHome(t, stub.URL)

	out := runCLI(t, linkedDir(t), "db", "create", "shop-db", "--site", "main")
	if !strings.Contains(out, "couldn't read the connection result — check with: ghayma connections") {
		t.Errorf("missing the check line:\n%s", out)
	}
}

// olderServerWarning is what a create says when the server sent no
// connections: it predates the choice and may have connected on its own.
func olderServerWarning(kind string) string {
	return "⚠️  this server didn't report connections — it may have connected the " + kind + " to the project's main site; check with: ghayma connections\n"
}

// --no-connect against a server that predates the choice: that server may
// still have connected the database, so the create never implies otherwise.
func TestDBCreate_NoConnectOnAnOlderServerSaysItMayHaveConnected(t *testing.T) {
	forceStdin(t, true)
	noSiteQuestions(t)
	stub := createStub(t, shopSites, dbCreated(""))
	cliHome(t, stub.URL)

	out := runCLI(t, linkedDir(t), "db", "create", "shop-db", "--no-connect")
	if got := stub.sentSites(); got != `[]` {
		t.Errorf("connect_site_ids = %s; want []", got)
	}
	if !strings.Contains(out, olderServerWarning("database")) {
		t.Errorf("missing the warning:\n%s", out)
	}
}
