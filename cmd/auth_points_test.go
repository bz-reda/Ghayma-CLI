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
			{Slug: "1k", MaxUsers: 1000, PointsCost: 1, SMSPoints: 1, SMSIncludedMonthly: 100, Position: 0},
			{Slug: "10k", MaxUsers: 10000, PointsCost: 3, SMSPoints: 3, SMSIncludedMonthly: 500, Position: 1},
		},
		Rates: api.CatalogRates{TwoFAPoints: 1},
	}
}

// authCostPreview mirrors the backend AuthCost EXACTLY. Vectors from
// paas-api/internal/points/pricing_test.go: AuthCost(3,3,1,true,true)=7 and
// AuthCost(1,1,1,false,false)=1, resolved from the fixture brackets.
func TestAuthCostPreview_BackendVectors(t *testing.T) {
	cat := authFixtureCatalog()

	// 10k bracket (3) + 2FA (rates.TwoFAPoints=1) + SMS (bracket.SMSPoints=3) = 7
	got, err := authCostPreview(cat, "10k", true, true)
	if err != nil {
		t.Fatalf("authCostPreview 10k: %v", err)
	}
	if got != 7 {
		t.Errorf("10k/2fa/sms = %d; want 7", got)
	}

	// 1k bracket (1), no features = 1
	got, err = authCostPreview(cat, "1k", false, false)
	if err != nil {
		t.Fatalf("authCostPreview 1k: %v", err)
	}
	if got != 1 {
		t.Errorf("1k/no-features = %d; want 1", got)
	}
}

// The SMS add-on price comes from the SELECTED bracket, not a flat rate: the
// same 2FA-only vs SMS-only choice costs differently per bracket.
func TestAuthCostPreview_SMSFromSelectedBracket(t *testing.T) {
	cat := authFixtureCatalog()

	// 2FA only on 1k: 1 + TwoFAPoints(1) = 2
	if got, _ := authCostPreview(cat, "1k", true, false); got != 2 {
		t.Errorf("1k/2fa-only = %d; want 2", got)
	}
	// SMS only on 10k: 3 + bracket.SMSPoints(3) = 6
	if got, _ := authCostPreview(cat, "10k", false, true); got != 6 {
		t.Errorf("10k/sms-only = %d; want 6", got)
	}
}

func TestAuthCostPreview_UnknownBracket(t *testing.T) {
	cat := authFixtureCatalog()
	if _, err := authCostPreview(cat, "nope", false, false); err == nil {
		t.Error("unknown bracket must error")
	}
	if _, err := authCostPreview(nil, "1k", false, false); err == nil {
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

// The SMS choice line MUST carry the selected bracket's price and included
// message allowance.
func TestAuthSMSLabel(t *testing.T) {
	label := authSMSLabel(api.CatalogAuthTier{Slug: "10k", SMSPoints: 3, SMSIncludedMonthly: 500})
	for _, want := range []string{"+3 pts", "includes 500", "msgs/mo"} {
		if !strings.Contains(label, want) {
			t.Errorf("SMS label %q missing %q", label, want)
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

// enabledFeatureList phrases the features being turned on, for both the success
// and partial-failure copy.
func TestEnabledFeatureList(t *testing.T) {
	cases := []struct {
		twofa, sms bool
		want       string
	}{
		{true, true, "2FA + SMS"},
		{true, false, "2FA"},
		{false, true, "SMS"},
		{false, false, ""},
	}
	for _, c := range cases {
		if got := enabledFeatureList(c.twofa, c.sms); got != c.want {
			t.Errorf("enabledFeatureList(%v,%v) = %q; want %q", c.twofa, c.sms, got, c.want)
		}
	}
}

// swapAuthPickers snapshots the promptui-backed picker vars and returns a
// restore fn, keeping tests hermetic.
func swapAuthPickers() func() {
	bf, tf, sf := promptAuthBracketFn, promptAuth2FAFn, promptAuthSMSFn
	return func() {
		promptAuthBracketFn, promptAuth2FAFn, promptAuthSMSFn = bf, tf, sf
	}
}

// A promptui cancel (Ctrl-C) from ANY auth picker must propagate as a non-nil
// error so the caller aborts WITHOUT creating an auth app. Also verifies
// short-circuiting: pickers after the cancelled one never run. Pins the Task 3
// cancel-swallow bug against reintroduction.
func TestPromptAuthSelections_CancelAborts(t *testing.T) {
	for _, failStage := range []string{"bracket", "2fa", "sms"} {
		t.Run(failStage+" cancel", func(t *testing.T) {
			defer swapAuthPickers()()

			twofaCalled, smsCalled := false, false

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
			promptAuthSMSFn = func(*api.MarketplaceCatalog, string) (bool, error) {
				smsCalled = true
				if failStage == "sms" {
					return false, promptui.ErrInterrupt
				}
				return true, nil
			}

			bracket, twofa, sms, err := promptAuthSelections(authFixtureCatalog())
			if err == nil {
				t.Fatal("expected a cancel error; nil would fall through to a create")
			}
			if bracket != "" || twofa || sms {
				t.Errorf("cancel must return zero selections, got %q/%v/%v", bracket, twofa, sms)
			}
			switch failStage {
			case "bracket":
				if twofaCalled || smsCalled {
					t.Error("bracket cancel must short-circuit before 2fa/sms pickers")
				}
			case "2fa":
				if smsCalled {
					t.Error("2fa cancel must short-circuit before sms picker")
				}
			}
		})
	}
}

// The happy path composes all three picker results in order.
func TestPromptAuthSelections_AllSucceed(t *testing.T) {
	defer swapAuthPickers()()
	promptAuthBracketFn = func(*api.MarketplaceCatalog) (string, error) { return "10k", nil }
	promptAuth2FAFn = func(*api.MarketplaceCatalog) (bool, error) { return true, nil }
	promptAuthSMSFn = func(*api.MarketplaceCatalog, string) (bool, error) { return false, nil }

	bracket, twofa, sms, err := promptAuthSelections(authFixtureCatalog())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bracket != "10k" || !twofa || sms {
		t.Errorf("got %q/%v/%v; want 10k/true/false", bracket, twofa, sms)
	}
}
