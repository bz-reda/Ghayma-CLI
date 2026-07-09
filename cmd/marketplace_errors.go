package cmd

import (
	"errors"
	"strings"

	"paas-cli/internal/api"
)

// formatMarketplaceError renders a marketplace admission rejection into
// user-facing guidance. It type-asserts *api.MarketplaceError and branches on
// Kind per the plan's Global Constraints; a non-marketplace error renders its
// raw text. db create + db resize (Task 3) and the app/storage/auth flows
// (Tasks 4 & 5) all call this — single source so the three-path insufficient
// copy and the capacity/maxtier lines never drift.
func formatMarketplaceError(err error) string {
	if err == nil {
		return ""
	}
	var me *api.MarketplaceError
	if !errors.As(err, &me) {
		return err.Error()
	}
	switch me.Kind {
	case "insufficient":
		// Shortfall (server message) + the three ways out.
		var b strings.Builder
		b.WriteString(me.Message)
		b.WriteString("\n")
		b.WriteString("You can:\n")
		b.WriteString("  • Upgrade your plan for a larger points budget\n")
		b.WriteString("  • Free up points by deleting or downsizing other resources\n")
		b.WriteString("  • Switch to pay-as-you-go (PAYG) to lift the budget cap")
		return b.String()
	case "capacity":
		// Never an upsell — the platform, not the plan, is the constraint.
		return "Ghayma is at capacity — contact support"
	case "maxtier":
		// The server message already names the requested + max tiers.
		return me.Message + "\nUpgrade your plan to unlock a larger tier."
	default:
		return me.Message
	}
}
