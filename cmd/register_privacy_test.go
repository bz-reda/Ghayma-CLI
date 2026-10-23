package cmd

import (
	"errors"
	"strings"
	"testing"
)

func TestPrivacyConsentText(t *testing.T) {
	want := "Ghayma is in beta and your data is hosted on servers in Paris, France,\n" +
		"and in the other countries listed in the policy, outside Algeria.\n" +
		"Privacy Policy: https://dash.ghayma.cloud/privacy"
	if got := privacyConsentText(); got != want {
		t.Errorf("privacyConsentText() =\n%q\nwant\n%q", got, want)
	}
}

func TestConfirmPrivacy(t *testing.T) {
	if !confirmPrivacy(true, func() (string, error) {
		t.Fatal("ask must not be called when --accept-privacy is set")
		return "", nil
	}) {
		t.Error("flag set: want true")
	}

	answer := func(s string) func() (string, error) {
		return func() (string, error) { return s, nil }
	}
	for _, s := range []string{"y", "Y", " yes "} {
		if !confirmPrivacy(false, answer(s)) {
			t.Errorf("answer %q: want true", s)
		}
	}
	for _, s := range []string{"", "n", "no", "ok"} {
		if confirmPrivacy(false, answer(s)) {
			t.Errorf("answer %q: want false", s)
		}
	}

	if confirmPrivacy(false, func() (string, error) { return "y", errors.New("interrupt") }) {
		t.Error("ask error: want false")
	}
}

func TestRegisterHint_PrivacyRequired(t *testing.T) {
	hint := registerHint("privacy_required")
	for _, want := range []string{"--accept-privacy", "https://dash.ghayma.cloud/register"} {
		if !strings.Contains(hint, want) {
			t.Errorf("privacy_required hint %q lacks %q", hint, want)
		}
	}
	if got, want := registerHint("invite_required"), "💡 Have a code? Run: ghayma register --invite <code>"; got != want {
		t.Errorf("invite_required hint = %q; want %q", got, want)
	}
	if got := registerHint(""); got != "" {
		t.Errorf("empty code hint = %q; want empty", got)
	}
}

func TestRegisterAcceptPrivacyFlagWired(t *testing.T) {
	f := registerCmd.Flags().Lookup("accept-privacy")
	if f == nil {
		t.Fatal("register must expose --accept-privacy")
	}
	if f.DefValue != "false" {
		t.Errorf("--accept-privacy default = %q; must be false so consent is asked", f.DefValue)
	}
}
