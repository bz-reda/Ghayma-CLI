package cmd

import (
	"testing"
	"time"

	"paas-cli/internal/api"
)

// cronFixture is two sites, each with a "nightly" job plus a unique one — so
// name resolution has both a unique case and a cross-site collision.
func cronFixture() []api.CronJob {
	return []api.CronJob{
		{ID: "c1", SiteID: "s-main", Name: "nightly", Schedule: "0 3 * * *"},
		{ID: "c2", SiteID: "s-main", Name: "hourly-main", Schedule: "0 * * * *"},
		{ID: "c3", SiteID: "s-admin", Name: "nightly", Schedule: "0 4 * * *"},
	}
}

// A name on exactly one site resolves to that single job.
func TestFindCronsByName_Unique(t *testing.T) {
	got := findCronsByName(cronFixture(), "hourly-main")
	if len(got) != 1 || got[0].ID != "c2" {
		t.Fatalf("findCronsByName(hourly-main) = %+v; want single c2", got)
	}
}

// A name shared across sites returns every match — the caller then requires
// --site to disambiguate.
func TestFindCronsByName_AmbiguousAcrossSites(t *testing.T) {
	got := findCronsByName(cronFixture(), "nightly")
	if len(got) != 2 {
		t.Fatalf("findCronsByName(nightly) = %d matches; want 2 (one per site)", len(got))
	}
}

// An unknown name matches nothing (surfaced as a not-found error).
func TestFindCronsByName_Missing(t *testing.T) {
	if got := findCronsByName(cronFixture(), "nope"); len(got) != 0 {
		t.Fatalf("findCronsByName(nope) = %+v; want none", got)
	}
}

// relLabel renders future durations with "in " and past durations with " ago",
// choosing the coarsest of just-now/m/h/d.
func TestRelLabel(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{-30 * time.Second, "just now"},
		{5 * time.Minute, "in 5m"},
		{-2 * time.Hour, "2h ago"},
		{-3 * 24 * time.Hour, "3d ago"},
		{26 * time.Hour, "in 1d"},
	}
	for _, c := range cases {
		if got := relLabel(c.d); got != c.want {
			t.Errorf("relLabel(%v) = %q; want %q", c.d, got, c.want)
		}
	}
}

// relTime renders a nil timestamp as an em dash (never/unscheduled).
func TestRelTimeNil(t *testing.T) {
	if got := relTime(nil); got != "—" {
		t.Errorf("relTime(nil) = %q; want em dash", got)
	}
}

func TestHumanizeMS(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		{0, "—"},
		{-1, "—"},
		{340, "340ms"},
		{1000, "1.0s"},
		{1234, "1.2s"},
	}
	for _, c := range cases {
		if got := humanizeMS(c.ms); got != c.want {
			t.Errorf("humanizeMS(%d) = %q; want %q", c.ms, got, c.want)
		}
	}
}

// truncateOneLine flattens newlines and clips long errors to one ellipsized
// line without splitting a multi-byte rune.
func TestTruncateOneLine(t *testing.T) {
	if got := truncateOneLine("line one\nline two", 40); got != "line one line two" {
		t.Errorf("newline flatten = %q", got)
	}
	if got := truncateOneLine("abcdefghij", 5); got != "abcd…" {
		t.Errorf("clip = %q; want abcd…", got)
	}
	// A multi-byte rune at the clip boundary must not be split into invalid UTF-8.
	if got := truncateOneLine("ééééé", 3); got != "éé…" {
		t.Errorf("rune-safe clip = %q; want éé…", got)
	}
}

// lastRunLabel is "never" before the first run, else "<status> <relative>".
func TestLastRunLabel(t *testing.T) {
	if got := lastRunLabel(nil); got != "never" {
		t.Errorf("lastRunLabel(nil) = %q; want never", got)
	}
	when := time.Now().Add(-5 * time.Minute)
	if got := lastRunLabel(&api.CronRun{Status: "success", StartedAt: &when}); got != "success 5m ago" {
		t.Errorf("lastRunLabel = %q; want 'success 5m ago'", got)
	}
}

// cronPath shows an http job's "METHOD /path" (defaulting GET) and falls back to
// the job type when there is no path.
func TestCronPath(t *testing.T) {
	if got := cronPath(api.CronJob{Path: "/api/cron", Method: "POST"}); got != "POST /api/cron" {
		t.Errorf("cronPath = %q", got)
	}
	if got := cronPath(api.CronJob{Path: "/api/cron"}); got != "GET /api/cron" {
		t.Errorf("cronPath default method = %q", got)
	}
	if got := cronPath(api.CronJob{Type: "container"}); got != "container" {
		t.Errorf("cronPath no-path = %q", got)
	}
}
