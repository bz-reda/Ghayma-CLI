package cmd

import (
	"strings"
	"testing"

	"paas-cli/internal/api"

	"github.com/manifoldco/promptui"
)

// authFixtureCatalog is a representative catalog for auth-app pricing. Two
// brackets let both backend AuthCost vectors resolve from real catalog rows;
// TwoFAPoints is a flat rate. Numbers are test inputs, not production pricing.
func authFixtureCatalog() *api.MarketplaceCatalog {
	return &api.MarketplaceCatalog{
		AuthTiers: []api.CatalogAuthTier{
			{Slug: "1k", MaxUsers: 1000, PointsCost: 1, Position: 0},
			{Slug: "10k", MaxUsers: 10000, PointsCost: 3, Position: 1},
		},
		Rates: api.CatalogRates{TwoFAPoints: 1},
	}
}

// authCostPreview mirrors the backend AuthCost EXACTLY. Vectors from
// paas-api/internal/points/pricing_test.go with smsEnabled=false — the only
// shape the CLI can now produce: AuthCost(3,_,1,true,false)=4 and
// AuthCost(1,_,1,false,false)=1, resolved from the fixture brackets.
func TestAuthCostPreview_BackendVectors(t *testing.T) {
	cat := authFixtureCatalog()

	// 10k bracket (3) + 2FA (rates.TwoFAPoints=1) = 4
	got, err := authCostPreview(cat, "10k", true)
	if err != nil {
		t.Fatalf("authCostPreview 10k: %v", err)
	}
	if got != 4 {
		t.Errorf("10k/2fa = %d; want 4", got)
	}

	// 1k bracket (1), no 2FA = 1
	got, err = authCostPreview(cat, "1k", false)
	if err != nil {
		t.Fatalf("authCostPreview 1k: %v", err)
	}
	if got != 1 {
		t.Errorf("1k/no-features = %d; want 1", got)
	}
}

// 2FA is a FLAT rate: the same add-on costs the same on every bracket, so only
// the bracket's own points differ.
func TestAuthCostPreview_2FAIsFlatRate(t *testing.T) {
	cat := authFixtureCatalog()

	// 2FA on 1k: 1 + TwoFAPoints(1) = 2
	if got, _ := authCostPreview(cat, "1k", true); got != 2 {
		t.Errorf("1k/2fa = %d; want 2", got)
	}
	// 2FA on 10k: 3 + TwoFAPoints(1) = 4
	if got, _ := authCostPreview(cat, "10k", true); got != 4 {
		t.Errorf("10k/2fa = %d; want 4", got)
	}
}

func TestAuthCostPreview_UnknownBracket(t *testing.T) {
	cat := authFixtureCatalog()
	if _, err := authCostPreview(cat, "nope", false); err == nil {
		t.Error("unknown bracket must error")
	}
	if _, err := authCostPreview(nil, "1k", false); err == nil {
		t.Error("nil catalog must error")
	}
}

// The --users flag value IS the bracket slug. validateAuthBracket is the
// flag→slug guard: blank (server default) and a known slug pass; an unknown
// slug errors, listing the valid brackets.
func TestValidateAuthBracket(t *testing.T) {
	cat := authFixtureCatalog()
	if err := validateAuthBracket(cat, ""); err != nil {
		t.Errorf("blank bracket must be allowed (server default), got %v", err)
	}
	if err := validateAuthBracket(cat, "10k"); err != nil {
		t.Errorf("known slug must validate, got %v", err)
	}
	err := validateAuthBracket(cat, "5k")
	if err == nil {
		t.Fatal("unknown slug must error")
	}
	for _, want := range []string{"1k", "10k"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should list valid bracket %q", err.Error(), want)
		}
	}
}

// defaultAuthTier is the lowest-Position bracket — the server's default when
// --users is omitted. Derived from the catalog, not hardcoded.
func TestDefaultAuthTier(t *testing.T) {
	cat := authFixtureCatalog()
	dt, ok := defaultAuthTier(cat)
	if !ok || dt.Slug != "1k" {
		t.Errorf("default bracket = %q (ok=%v); want 1k", dt.Slug, ok)
	}
	if _, ok := defaultAuthTier(&api.MarketplaceCatalog{}); ok {
		t.Error("empty catalog must have no default bracket")
	}
}

// The bracket label renders capacity + points from the catalog.
func TestAuthBracketLabel(t *testing.T) {
	label := authBracketLabel(api.CatalogAuthTier{Slug: "10k", MaxUsers: 10000, PointsCost: 3})
	for _, want := range []string{"10k", "10000", "3 pts"} {
		if !strings.Contains(label, want) {
			t.Errorf("bracket label %q missing %q", label, want)
		}
	}
}

// bracketOrDefault renders a blank slug (server default) as "default" for the
// partial-failure message, otherwise the slug verbatim.
func TestBracketOrDefault(t *testing.T) {
	if got := bracketOrDefault(""); got != "default" {
		t.Errorf("blank = %q; want default", got)
	}
	if got := bracketOrDefault("10k"); got != "10k" {
		t.Errorf("10k = %q; want 10k", got)
	}
}

// swapAuthPickers snapshots the promptui-backed picker vars and returns a
// restore fn, keeping tests hermetic.
func swapAuthPickers() func() {
	bf, tf := promptAuthBracketFn, promptAuth2FAFn
	return func() {
		promptAuthBracketFn, promptAuth2FAFn = bf, tf
	}
}

// A promptui cancel (Ctrl-C) from ANY auth picker must propagate as a non-nil
// error so the caller aborts WITHOUT creating an auth app. Also verifies
// short-circuiting: pickers after the cancelled one never run. Pins the Task 3
// cancel-swallow bug against reintroduction.
func TestPromptAuthSelections_CancelAborts(t *testing.T) {
	for _, failStage := range []string{"bracket", "2fa"} {
		t.Run(failStage+" cancel", func(t *testing.T) {
			defer swapAuthPickers()()

			twofaCalled := false

			promptAuthBracketFn = func(*api.MarketplaceCatalog) (string, error) {
				if failStage == "bracket" {
					return "", promptui.ErrInterrupt
				}
				return "10k", nil
			}
			promptAuth2FAFn = func(*api.MarketplaceCatalog) (bool, error) {
				twofaCalled = true
				if failStage == "2fa" {
					return false, promptui.ErrInterrupt
				}
				return true, nil
			}

			bracket, twofa, err := promptAuthSelections(authFixtureCatalog())
			if err == nil {
				t.Fatal("expected a cancel error; nil would fall through to a create")
			}
			if bracket != "" || twofa {
				t.Errorf("cancel must return zero selections, got %q/%v", bracket, twofa)
			}
			if failStage == "bracket" && twofaCalled {
				t.Error("bracket cancel must short-circuit before the 2fa picker")
			}
		})
	}
}

// The happy path composes both picker results in order.
func TestPromptAuthSelections_AllSucceed(t *testing.T) {
	defer swapAuthPickers()()
	promptAuthBracketFn = func(*api.MarketplaceCatalog) (string, error) { return "10k", nil }
	promptAuth2FAFn = func(*api.MarketplaceCatalog) (bool, error) { return true, nil }

	bracket, twofa, err := promptAuthSelections(authFixtureCatalog())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bracket != "10k" || !twofa {
		t.Errorf("got %q/%v; want 10k/true", bracket, twofa)
	}
}
