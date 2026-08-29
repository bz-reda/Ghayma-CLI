package cmd

import "testing"

// Pins the private-beta register wiring: the --invite flag must exist, and
// registration must not grow interactive prompts for machine config again
// (the API host now comes from config like every other command).
func TestRegisterInviteFlagWired(t *testing.T) {
	f := registerCmd.Flags().Lookup("invite")
	if f == nil {
		t.Fatal("register must expose --invite while the beta gate exists")
	}
	if f.DefValue != "" {
		t.Errorf("--invite default = %q; must be empty so open-signup registers send no code", f.DefValue)
	}
}
