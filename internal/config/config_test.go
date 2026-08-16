package config

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// unsignedJWT builds a token with a real base64url payload and a junk
// signature — enough to exercise the claims decoder, which never verifies.
func unsignedJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	enc := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	return enc(map[string]string{"alg": "HS256", "typ": "JWT"}) + "." + enc(claims) + ".sig"
}

// TestBearer_PrefersAPIToken pins the credential the client must send: a
// gh_/et_ personal access token when one is stored, otherwise the session JWT.
func TestBearer_PrefersAPIToken(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		want    string
		usesPAT bool
	}{
		{"gh_ token wins", Config{Token: "jwt", APIToken: "gh_abc"}, "gh_abc", true},
		{"legacy et_ token wins", Config{Token: "jwt", APIToken: "et_abc"}, "et_abc", true},
		{"legacy uuid ignored", Config{Token: "jwt", APIToken: "3f7c1e02-1b6d-4c8a-9a11-2f0e5d6b7c88"}, "jwt", false},
		{"no api token", Config{Token: "jwt"}, "jwt", false},
		{"nothing stored", Config{}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.Bearer(); got != tc.want {
				t.Errorf("Bearer() = %q; want %q", got, tc.want)
			}
			if got := tc.cfg.UsesAPIToken(); got != tc.usesPAT {
				t.Errorf("UsesAPIToken() = %v; want %v", got, tc.usesPAT)
			}
		})
	}
}

// TestIsAPIToken covers the prefix guard on its own: only the two PAT prefixes
// the backend mints are bearers; anything else is a legacy users.api_token UUID.
func TestIsAPIToken(t *testing.T) {
	for _, s := range []string{"gh_deadbeef", "et_deadbeef"} {
		if !IsAPIToken(s) {
			t.Errorf("IsAPIToken(%q) = false; want true", s)
		}
	}
	for _, s := range []string{"", "jwt.body.sig", "3f7c1e02-1b6d-4c8a-9a11-2f0e5d6b7c88", "GH_upper"} {
		if IsAPIToken(s) {
			t.Errorf("IsAPIToken(%q) = true; want false", s)
		}
	}
}

// TestJWTExpiry decodes the exp claim without verifying the signature; the
// server stays the authority, so anything malformed reports "unknown".
func TestJWTExpiry(t *testing.T) {
	exp := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	got, ok := JWTExpiry(unsignedJWT(t, map[string]any{"exp": exp.Unix()}))
	if !ok {
		t.Fatal("JWTExpiry(valid) ok = false; want true")
	}
	if !got.Equal(exp) {
		t.Errorf("JWTExpiry = %s; want %s", got, exp)
	}

	for _, bad := range []string{
		"",
		"not-a-jwt",
		"only.two",
		"a." + base64.RawURLEncoding.EncodeToString([]byte("{not json}")) + ".c",
		"a.!!!not-base64!!!.c",
		unsignedJWT(t, map[string]any{"sub": "u1"}),       // no exp
		unsignedJWT(t, map[string]any{"exp": "tomorrow"}), // non-numeric exp
		"gh_1234567890abcdef",
	} {
		if _, ok := JWTExpiry(bad); ok {
			t.Errorf("JWTExpiry(%q) ok = true; want false", bad)
		}
	}
}

// TestSessionExpired is the regression pin for the 2026-08-16 incident: a
// 7-day session JWT silently aged out and every command reported nonsense
// (e.g. "you don't have any projects yet") instead of "log in again".
func TestSessionExpired(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	past := unsignedJWT(t, map[string]any{"exp": now.Add(-time.Hour).Unix()})
	future := unsignedJWT(t, map[string]any{"exp": now.Add(24 * time.Hour).Unix()})

	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"expired session", Config{Token: past}, true},
		{"live session", Config{Token: future}, false},
		{"api token outlives the stale jwt", Config{Token: past, APIToken: "gh_abc"}, false},
		{"malformed token never blocks", Config{Token: "not-a-jwt"}, false},
		{"empty config", Config{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.SessionExpired(now); got != tc.want {
				t.Errorf("SessionExpired() = %v; want %v", got, tc.want)
			}
		})
	}
}

// TestSessionExpiry exposes the session deadline for whoami.
func TestSessionExpiry(t *testing.T) {
	exp := time.Date(2026, 8, 23, 9, 30, 0, 0, time.UTC)
	cfg := Config{Token: unsignedJWT(t, map[string]any{"exp": exp.Unix()})}
	got, ok := cfg.SessionExpiry()
	if !ok || !got.Equal(exp) {
		t.Errorf("SessionExpiry() = %s,%v; want %s,true", got, ok, exp)
	}
	if _, ok := (&Config{Token: "gh_abc"}).SessionExpiry(); ok {
		t.Error("SessionExpiry() on a non-JWT should report unknown")
	}
}

// TestAPITokenIDPersisted keeps the revoke handle in the config file so
// logout can delete the token it minted.
func TestAPITokenIDPersisted(t *testing.T) {
	raw, err := json.Marshal(Config{APITokenID: "tok-1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"api_token_id":"tok-1"`) {
		t.Errorf("config JSON = %s; want an api_token_id field", raw)
	}

	var back Config
	if err := json.Unmarshal([]byte(`{"api_token_id":"tok-2"}`), &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.APITokenID != "tok-2" {
		t.Errorf("APITokenID = %q; want tok-2", back.APITokenID)
	}
}

// TestLoggedIn pins that a real API token counts as a login on its own, while
// the legacy users.api_token UUID (never a bearer) does not.
func TestLoggedIn(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"empty", Config{}, false},
		{"jwt only", Config{Token: "a.b.c"}, true},
		{"pat only", Config{APIToken: "gh_x"}, true},
		{"legacy uuid only", Config{APIToken: "0b8f2c1e-1111-2222-3333-444455556666"}, false},
	}
	for _, tc := range cases {
		if got := tc.cfg.LoggedIn(); got != tc.want {
			t.Errorf("%s: LoggedIn() = %v; want %v", tc.name, got, tc.want)
		}
	}
}
