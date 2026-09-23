package config

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	APIHost  string `json:"api_host"`
	Token    string `json:"token"`
	APIToken string `json:"api_token"`
	// APITokenID is the server-side id of the personal access token the CLI
	// minted for this machine, kept so logout can revoke it.
	APITokenID string   `json:"api_token_id,omitempty"`
	UserID     string   `json:"user_id"`
	Email      string   `json:"email"`
	CLI        CLIState `json:"cli,omitempty"`

	// fileAPIHost is the host as the config file had it, before any
	// GHAYMA_API_HOST override was applied. Save() writes this back so a
	// one-command override is never baked into the file by an unrelated
	// write (maybeWarnDeprecated does Load → mutate → Save on every run).
	// A Config built by hand rather than by Load() should set its host
	// through SetHost, which keeps the two fields in step. Unexported, so
	// encoding/json skips it without needing a tag.
	fileAPIHost string
}

// CLIState carries local client-side metadata that doesn't round-trip with
// the server — currently only deprecation-notice rate limiting. Nested
// under a `cli` key so future additions don't clutter the top-level config
// shape.
type CLIState struct {
	// DeprecationNotices maps a stable notice-id (e.g. "site.add") to the
	// RFC3339 timestamp at which that notice was last shown to the user.
	// Used to throttle repeat warnings to once per week per notice.
	DeprecationNotices map[string]string `json:"deprecation_notices,omitempty"`
}

// Path is the config file: GHAYMA_CONFIG when set, else ~/.paas-cli.json. A
// second file is how a staging login lives beside the production one.
func Path() string {
	if p := strings.TrimSpace(os.Getenv("GHAYMA_CONFIG")); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".paas-cli.json")
}

// defaultAPIHost is the backend used when neither the config file nor
// GHAYMA_API_HOST names one.
const defaultAPIHost = "https://api.ghayma.tech"

// envAPIHost is the GHAYMA_API_HOST override, normalised; empty when unset.
func envAPIHost() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv("GHAYMA_API_HOST")), "/")
}

func Load() *Config {
	cfg := &Config{}
	if data, err := os.ReadFile(Path()); err == nil {
		json.Unmarshal(data, cfg)
	}
	cfg.fileAPIHost = cfg.APIHost
	// GHAYMA_API_HOST wins over the file; the file wins over the default.
	if h := envAPIHost(); h != "" {
		cfg.APIHost = h
	}
	if cfg.APIHost == "" {
		cfg.APIHost = defaultAPIHost
	}
	return cfg
}

// SetHost points this config at a backend for good — the host is written to
// the file on the next Save even when GHAYMA_API_HOST is set. This is what
// `ghayma login --host` uses; a plain assignment to APIHost would be treated
// as an override and dropped on save.
func (c *Config) SetHost(h string) {
	h = strings.TrimRight(strings.TrimSpace(h), "/")
	c.APIHost = h
	c.fileAPIHost = h
}

func (c *Config) Save() error {
	out := *c
	// An override is a per-command thing, not a login: keep whatever host the
	// file already had.
	if envAPIHost() != "" {
		out.APIHost = c.fileAPIHost
	}
	data, err := json.MarshalIndent(&out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(Path(), data, 0600)
}

// IsAPIToken reports whether s looks like a personal access token the backend
// accepts as a bearer. gh_ is current, et_ the pre-rebrand prefix.
func IsAPIToken(s string) bool {
	return strings.HasPrefix(s, "gh_") || strings.HasPrefix(s, "et_")
}

// Bearer picks the credential to authenticate with: the long-lived personal
// access token when we have one, otherwise the login session JWT.
//
// The prefix guard matters — configs written before the CLI minted its own
// token stored the legacy users.api_token UUID in api_token, which is not a
// bearer at all. Sending it would 401 every request, so those configs keep
// riding the JWT path until the next login replaces it.
func (c *Config) Bearer() string {
	if IsAPIToken(c.APIToken) {
		return c.APIToken
	}
	return c.Token
}

// UsesAPIToken reports whether requests go out on a long-lived token rather
// than the 7-day session.
func (c *Config) UsesAPIToken() bool {
	return IsAPIToken(c.APIToken)
}

// JWTExpiry reads the exp claim out of a JWT WITHOUT verifying the signature.
// This is a UX affordance only (tell the user their session died instead of
// letting a 401 surface as "you have no projects" — 2026-08-16); the server
// stays the sole authority, so anything unparseable reports unknown.
func JWTExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Tolerate padded encoders.
		if payload, err = base64.URLEncoding.DecodeString(parts[1]); err != nil {
			return time.Time{}, false
		}
	}

	var claims struct {
		Exp json.Number `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == "" {
		return time.Time{}, false
	}
	secs, err := claims.Exp.Float64()
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(int64(secs), 0).UTC(), true
}

// SessionExpiry returns the deadline of the stored login session, if known.
func (c *Config) SessionExpiry() (time.Time, bool) {
	return JWTExpiry(c.Token)
}

// SessionExpired reports whether the CLI is riding a session JWT that has
// already lapsed. A long-lived token, or an expiry we can't read, never
// blocks — fail open and let the API answer.
func (c *Config) SessionExpired(now time.Time) bool {
	if c.UsesAPIToken() {
		return false
	}
	exp, ok := c.SessionExpiry()
	return ok && now.After(exp)
}

// LoggedIn reports whether the CLI holds any usable credential — a session
// JWT or a real (prefixed) API token. Commands gate on this rather than on
// Token alone, so a token-only config is not told to "login first" (2026-08-16).
func (c *Config) LoggedIn() bool {
	return c.Token != "" || IsAPIToken(c.APIToken)
}
