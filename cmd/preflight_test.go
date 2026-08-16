package cmd

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

func jwtExpiring(t *testing.T, at time.Time) string {
	t.Helper()
	enc := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	return enc(map[string]string{"alg": "HS256"}) + "." + enc(map[string]int64{"exp": at.Unix()}) + ".sig"
}

// TestSessionPreflight is the guard that turns the 2026-08-16 failure into a
// straight answer: a lapsed 7-day session stops the command up front instead
// of letting a 401 read as "you don't have any projects yet".
func TestSessionPreflight(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	expired := &config.Config{Token: jwtExpiring(t, now.Add(-time.Hour))}
	live := &config.Config{Token: jwtExpiring(t, now.Add(24*time.Hour))}
	withPAT := &config.Config{Token: jwtExpiring(t, now.Add(-time.Hour)), APIToken: "gh_x"}

	if err := sessionPreflight(expired, "link", now); err == nil {
		t.Fatal("expired session should block an authenticated command")
	} else if err.Error() != "Your CLI session has expired (sessions last 7 days). Run: ghayma login" {
		t.Errorf("error = %q; want the exact recovery message", err)
	}

	if err := sessionPreflight(live, "link", now); err != nil {
		t.Errorf("live session: %v; want nil", err)
	}
	if err := sessionPreflight(withPAT, "link", now); err != nil {
		t.Errorf("long-lived token should outlive the stale jwt: %v", err)
	}
	if err := sessionPreflight(&config.Config{}, "link", now); err != nil {
		t.Errorf("empty config should fall through to the usual login guard: %v", err)
	}
}

// TestSessionPreflight_SkipsUnauthenticatedCommands keeps the guard from
// locking the user out of the very commands that fix an expired session.
func TestSessionPreflight_SkipsUnauthenticatedCommands(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	expired := &config.Config{Token: jwtExpiring(t, now.Add(-time.Hour))}

	for _, name := range []string{"login", "logout", "register", "version", "whoami", "help", "completion"} {
		if err := sessionPreflight(expired, name, now); err != nil {
			t.Errorf("sessionPreflight(%q) = %v; want nil (must stay reachable)", name, err)
		}
	}
	for _, name := range []string{"deploy", "link", "init", "logs", "status", "env", "db"} {
		if err := sessionPreflight(expired, name, now); err == nil {
			t.Errorf("sessionPreflight(%q) = nil; want the expired-session error", name)
		}
	}
}

// TestLink_ChecksAuthBeforeMonorepoPrompt pins the ordering: the user should
// hear "your session expired" before being asked which subdirectory to link.
func TestLink_ChecksAuthBeforeMonorepoPrompt(t *testing.T) {
	src := readCmdSource(t, "link.go")
	list := strings.Index(src, "ListProjects(")
	subdir := strings.Index(src, "detectMonorepoAppSubdir(")
	if list < 0 || subdir < 0 {
		t.Fatal("link.go must both list projects and detect the monorepo subdir")
	}
	if list > subdir {
		t.Error("link.go prompts for the monorepo subdir before checking the session; an expired login should be reported first")
	}
	if strings.Index(src, "findProjectConfig(") < subdir {
		t.Error("the already-linked check depends on configDir and must stay after subdir detection")
	}
}

// TestWhoamiAuthLine pins the strings whoami prints for each credential state.
// Tokens themselves are never part of it.
func TestWhoamiAuthLine(t *testing.T) {
	exp := time.Date(2026, 8, 23, 9, 30, 0, 0, time.UTC)

	cases := []struct {
		name string
		cfg  config.Config
		want string
	}{
		{"api token", config.Config{Token: jwtExpiring(t, exp), APIToken: "gh_secret"}, "long-lived CLI token"},
		{"live session", config.Config{Token: jwtExpiring(t, time.Now().Add(48*time.Hour))}, "7-day session (expires "},
		{"expired session", config.Config{Token: jwtExpiring(t, time.Now().Add(-time.Hour))}, "7-day session (EXPIRED — run: ghayma login)"},
		{"unknown expiry", config.Config{Token: "opaque"}, "7-day session"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := authDescription(&tc.cfg)
			if !strings.HasPrefix(got, tc.want) {
				t.Errorf("authDescription() = %q; want prefix %q", got, tc.want)
			}
			if strings.Contains(got, "gh_secret") || strings.Contains(got, "opaque") {
				t.Errorf("authDescription() leaks the credential: %q", got)
			}
		})
	}
}

// TestWhoamiAuthJSON pins the machine-readable half.
func TestWhoamiAuthJSON(t *testing.T) {
	exp := time.Date(2026, 8, 23, 9, 30, 0, 0, time.UTC)

	pat := whoamiAuthFields(&config.Config{Token: jwtExpiring(t, exp), APIToken: "gh_x"})
	if pat["auth_method"] != "api_token" {
		t.Errorf("auth_method = %v; want api_token", pat["auth_method"])
	}
	if _, present := pat["session_expires_at"]; present {
		t.Error("session_expires_at should be omitted when a long-lived token is in use")
	}

	sess := whoamiAuthFields(&config.Config{Token: jwtExpiring(t, exp)})
	if sess["auth_method"] != "session" {
		t.Errorf("auth_method = %v; want session", sess["auth_method"])
	}
	if got := sess["session_expires_at"]; got != exp.Format(time.RFC3339) {
		t.Errorf("session_expires_at = %v; want %s", got, exp.Format(time.RFC3339))
	}

	unknown := whoamiAuthFields(&config.Config{Token: "opaque"})
	if _, present := unknown["session_expires_at"]; present {
		t.Error("session_expires_at should be omitted when the expiry can't be read")
	}
}

// TestTopLevelCommandName keeps the skip list keyed on the command under root:
// `ghayma completion zsh` reports Name()=="zsh", which is not in the list, so
// keying on Name() would lock a dead session out of shell completion.
func TestTopLevelCommandName(t *testing.T) {
	root := &cobra.Command{Use: "ghayma"}
	completion := &cobra.Command{Use: "completion"}
	zsh := &cobra.Command{Use: "zsh"}
	db := &cobra.Command{Use: "db"}
	list := &cobra.Command{Use: "list"}
	root.AddCommand(completion, db)
	completion.AddCommand(zsh)
	db.AddCommand(list)

	for cmd, want := range map[*cobra.Command]string{zsh: "completion", completion: "completion", list: "db", db: "db", root: "ghayma"} {
		if got := topLevelCommandName(cmd); got != want {
			t.Errorf("topLevelCommandName(%s) = %q; want %q", cmd.Name(), got, want)
		}
	}
	// And the property that matters end to end:
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	expired := &config.Config{Token: jwtExpiring(t, now.Add(-time.Hour))}
	if err := sessionPreflight(expired, topLevelCommandName(zsh), now); err != nil {
		t.Errorf("completion zsh must stay reachable on an expired session: %v", err)
	}
	if err := sessionPreflight(expired, topLevelCommandName(list), now); err == nil {
		t.Error("db list must be blocked on an expired session")
	}
}
