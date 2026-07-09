package cmd

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/manifoldco/promptui"
)

// fixturePlans is a representative billing/plans response — the numbers are
// test inputs, not production pricing (the CLI hardcodes none).
func fixturePlans() []api.FixedPlan {
	return []api.FixedPlan{
		{Slug: "hobby", DisplayName: "Hobby", PriceDZDPerMonth: 2500, Points: 10, MaxAppTier: "b", MaxDBTier: "s"},
		{Slug: "pro", DisplayName: "Pro", PriceDZDPerMonth: 12000, Points: 40, MaxAppTier: "c", MaxDBTier: "m"},
	}
}

func planTestClient(url string) *api.Client {
	return api.NewClient(&config.Config{APIHost: url, Token: "test-token"})
}

// TestPlanSelectLabels pins the interactive label format from the brief:
// "<slug> — <price> DZD/mo · <points> pts", with the price thousand-separated
// and every value drawn from the response (nothing hardcoded).
func TestPlanSelectLabels(t *testing.T) {
	labels := planSelectLabels(fixturePlans())
	if len(labels) != 2 {
		t.Fatalf("got %d labels; want 2", len(labels))
	}
	if labels[0] != "hobby — 2,500 DZD/mo · 10 pts" {
		t.Errorf("labels[0] = %q; want %q", labels[0], "hobby — 2,500 DZD/mo · 10 pts")
	}
	if labels[1] != "pro — 12,000 DZD/mo · 40 pts" {
		t.Errorf("labels[1] = %q; want %q", labels[1], "pro — 12,000 DZD/mo · 40 pts")
	}
}

// TestFormatThousands pins the comma grouping the price label depends on.
func TestFormatThousands(t *testing.T) {
	cases := map[int64]string{
		0:       "0",
		500:     "500",
		2500:    "2,500",
		12000:   "12,000",
		1000000: "1,000,000",
	}
	for in, want := range cases {
		if got := formatThousands(in); got != want {
			t.Errorf("formatThousands(%d) = %q; want %q", in, got, want)
		}
	}
}

// TestValidatePlanFlag_Valid: a known slug passes through unchanged.
func TestValidatePlanFlag_Valid(t *testing.T) {
	got, err := validatePlanFlag(fixturePlans(), "pro")
	if err != nil {
		t.Fatalf("validatePlanFlag(pro): %v", err)
	}
	if got != "pro" {
		t.Errorf("got %q; want pro", got)
	}
}

// TestValidatePlanFlag_Invalid: an unknown slug errors and NAMES the available
// slugs so the user can correct the flag.
func TestValidatePlanFlag_Invalid(t *testing.T) {
	_, err := validatePlanFlag(fixturePlans(), "enterprise")
	if err == nil {
		t.Fatal("want error for unknown slug")
	}
	if !strings.Contains(err.Error(), "enterprise") {
		t.Errorf("error %q should name the bad slug", err.Error())
	}
	if !strings.Contains(err.Error(), "hobby") || !strings.Contains(err.Error(), "pro") {
		t.Errorf("error %q should list the available slugs hobby, pro", err.Error())
	}
}

// TestResolvePlan_FlagValid: with --plan set to a known slug and a live plans
// endpoint, resolvePlan returns that slug (non-interactive path).
func TestResolvePlan_FlagValid(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"fixed_plans":[{"slug":"hobby","price_dzd_per_month":2500,"points":10},{"slug":"pro","price_dzd_per_month":12000,"points":40}]}`))
	}))
	defer ts.Close()

	got, err := resolvePlan(planTestClient(ts.URL), "pro")
	if err != nil {
		t.Fatalf("resolvePlan: %v", err)
	}
	if got != "pro" {
		t.Errorf("got %q; want pro", got)
	}
}

// TestResolvePlan_FlagInvalid: with --plan set to an unknown slug, resolvePlan
// errors (init aborts) and names the available slugs.
func TestResolvePlan_FlagInvalid(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"fixed_plans":[{"slug":"hobby","price_dzd_per_month":2500,"points":10}]}`))
	}))
	defer ts.Close()

	_, err := resolvePlan(planTestClient(ts.URL), "enterprise")
	if err == nil {
		t.Fatal("want error for unknown --plan")
	}
	if errors.Is(err, errPlanSelectionCancelled) {
		t.Errorf("an invalid flag is not a cancel; got errPlanSelectionCancelled")
	}
	if !strings.Contains(err.Error(), "hobby") {
		t.Errorf("error %q should list available slugs", err.Error())
	}
}

// TestResolvePlan_FetchFailedFallback: without --plan, a 404 (old/self-hosted
// server) degrades to the empty plan (server default) with NO error — init must
// never block. The explicit-flag case is covered by
// TestResolvePlan_FlagSurvivesFetchFailure below, which sends the slug through.
func TestResolvePlan_FetchFailedFallback(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"404 page not found"}`))
	}))
	defer ts.Close()

	got, err := resolvePlan(planTestClient(ts.URL), "")
	if err != nil {
		t.Fatalf("resolvePlan(no flag): want fail-soft nil error, got %v", err)
	}
	if got != "" {
		t.Errorf("resolvePlan(no flag) = %q; want empty (server default)", got)
	}
}

// TestResolvePlan_FlagSurvivesFetchFailure: with --plan set and a failing plans
// fetch, resolvePlan must NOT drop the flag to the server default (that would
// silently create a scripted `init --plan pro` on hobby). It sends the slug
// through unvalidated — the server then validates/rejects it — with NO error so
// init is never blocked.
func TestResolvePlan_FlagSurvivesFetchFailure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"404 page not found"}`))
	}))
	defer ts.Close()

	got, err := resolvePlan(planTestClient(ts.URL), "pro")
	if err != nil {
		t.Fatalf("resolvePlan(--plan pro, fetch fails): want nil error, got %v", err)
	}
	if got != "pro" {
		t.Errorf("resolvePlan(--plan pro, fetch fails) = %q; want %q (sent as-is)", got, "pro")
	}
}

// TestResolvePlan_InteractiveHappy: no --plan, the picker returns a slug, and
// resolvePlan passes it through.
func TestResolvePlan_InteractiveHappy(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"fixed_plans":[{"slug":"hobby","price_dzd_per_month":2500,"points":10},{"slug":"pro","price_dzd_per_month":12000,"points":40}]}`))
	}))
	defer ts.Close()

	orig := promptPlanSelectFn
	defer func() { promptPlanSelectFn = orig }()
	promptPlanSelectFn = func(plans []api.FixedPlan) (string, error) {
		return "pro", nil
	}

	got, err := resolvePlan(planTestClient(ts.URL), "")
	if err != nil {
		t.Fatalf("resolvePlan: %v", err)
	}
	if got != "pro" {
		t.Errorf("got %q; want pro", got)
	}
}

// TestResolvePlan_CancelAborts is the core cancel-discipline pin: a promptui
// cancel (Ctrl-C) from the interactive picker returns errPlanSelectionCancelled
// so init ABORTS — it must never fall through to a create on the default plan.
// This is DISTINCT from the fetch-failure fallback above.
func TestResolvePlan_CancelAborts(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"fixed_plans":[{"slug":"hobby","price_dzd_per_month":2500,"points":10}]}`))
	}))
	defer ts.Close()

	orig := promptPlanSelectFn
	defer func() { promptPlanSelectFn = orig }()
	promptPlanSelectFn = func(plans []api.FixedPlan) (string, error) {
		return "", promptui.ErrInterrupt
	}

	got, err := resolvePlan(planTestClient(ts.URL), "")
	if !errors.Is(err, errPlanSelectionCancelled) {
		t.Fatalf("err = %v; want errPlanSelectionCancelled", err)
	}
	if got != "" {
		t.Errorf("cancel must not yield a plan slug; got %q", got)
	}
}

// TestResolvePlan_EmptyCatalogFallback: a live endpoint that returns zero
// fixed plans (nothing to choose) degrades to the server default, not a cancel.
func TestResolvePlan_EmptyCatalogFallback(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"fixed_plans":[]}`))
	}))
	defer ts.Close()

	got, err := resolvePlan(planTestClient(ts.URL), "")
	if err != nil {
		t.Fatalf("resolvePlan: want nil error on empty catalog, got %v", err)
	}
	if got != "" {
		t.Errorf("got %q; want empty (server default)", got)
	}
}
