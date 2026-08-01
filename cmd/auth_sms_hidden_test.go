package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"paas-cli/internal/api"
)

// SMS/WhatsApp as a second factor has NO send path in the backend — it is
// blocked on a messaging partner that has not been signed. The CLI must not
// offer it, must not send sms_enabled, and must not price it. TOTP 2FA is live
// and stays. These tests pin all three properties against reintroduction.

// The create command must expose --2fa and must NOT expose --sms. An unknown
// flag is a loud, non-zero-exit failure; an accepted-but-inert --sms would
// exit 0 while quietly not doing what it says.
func TestAuthCreateFlags_SMSGone2FAKept(t *testing.T) {
	if f := authCreateCmd.Flags().Lookup("sms"); f != nil {
		t.Errorf("auth create still registers --sms (%q)", f.Usage)
	}
	if authCreateCmd.Flags().Lookup("2fa") == nil {
		t.Error("auth create must keep --2fa; TOTP is live and partner-free")
	}
}

// No code path may put sms_enabled in an auth-app payload. Pinned at the source
// level because the write happens inside a cobra Run func that has no seam.
func TestAuthCommands_NeverSendSMSEnabled(t *testing.T) {
	for _, file := range []string{"auth.go", "auth_points.go"} {
		if strings.Contains(readCmdSource(t, file), "sms_enabled") {
			t.Errorf("%s still writes sms_enabled; the backend field must stay false", file)
		}
	}
}

// The preview must be correct WITHOUT sms_points, not silently zero-padded by
// Go's missing-field decode. This feeds a catalog that still carries the
// pre-removal SMS keys: the cost must ignore them entirely and equal
// bracket + 2FA, which is exactly what the backend will charge now that the
// CLI never enables SMS.
func TestAuthCostPreview_IgnoresCatalogSMSPricing(t *testing.T) {
	const legacyCatalogJSON = `{
	  "auth_tiers": [
	    {"slug":"10k","max_users":10000,"points_cost":3,"sms_points":3,"sms_included_monthly":500,"position":1}
	  ],
	  "rates": {"twofa_points":1}
	}`

	var cat api.MarketplaceCatalog
	if err := json.Unmarshal([]byte(legacyCatalogJSON), &cat); err != nil {
		t.Fatalf("decode legacy catalog: %v", err)
	}

	got, err := authCostPreview(&cat, "10k", true)
	if err != nil {
		t.Fatalf("authCostPreview: %v", err)
	}
	if got != 4 {
		t.Errorf("10k/2fa = %d; want 4 (bracket 3 + 2FA 1, sms_points 3 ignored)", got)
	}
}
