package cmd

import (
	"os"
	"strings"
	"testing"
)

// TestLogin_DropsLegacyAPIToken pins that login and register stop copying the
// server's users.api_token UUID into cfg.APIToken. It is not a bearer, so the
// CLI never sent it; keeping it there now would shadow the real gh_ token that
// Bearer() looks for (2026-08-16).
func TestLogin_DropsLegacyAPIToken(t *testing.T) {
	for _, file := range []string{"login.go", "register.go"} {
		if src := readCmdSource(t, file); strings.Contains(src, "cfg.APIToken = resp.APIToken") {
			t.Errorf("%s still stores the legacy users.api_token UUID as the CLI bearer", file)
		}
	}
}

// TestLogin_ProvisionsLongLivedToken pins the mint: every successful login and
// register path routes through provisionCLIToken, which trades the 7-day
// browser JWT for a personal access token the API is designed to be driven by.
func TestLogin_ProvisionsLongLivedToken(t *testing.T) {
	login := readCmdSource(t, "login.go")
	for _, want := range []string{
		"func provisionCLIToken(",
		"cliTokenTTLDays",
		"cliTokenScope",
		"CreateAPIToken(",
		"ListAPITokens(",
		"DeleteAPIToken(",
	} {
		if !strings.Contains(login, want) {
			t.Errorf("login.go should reference %q for the long-lived token flow", want)
		}
	}

	// Both login paths and register's auto-login must call it.
	if got := strings.Count(login, "provisionCLIToken("); got < 3 {
		t.Errorf("login.go mentions provisionCLIToken %d times; want the definition plus both login paths", got)
	}
	if !strings.Contains(readCmdSource(t, "register.go"), "provisionCLIToken(") {
		t.Error("register.go should provision a CLI token on its auto-login path")
	}
}

// TestLogin_PersistsTheHostItAuthenticatedAgainst pins the one place where a
// host override is deliberately written to the config file. GHAYMA_API_HOST is
// per-command everywhere else and Save() drops it, but the token these paths
// mint is only valid against the backend that issued it — so an active
// override goes through SetHost, before the client is built and the first
// request goes out. login also takes --host; register deliberately does not
// (it is not the place to choose a backend), so it only carries the env guard.
func TestLogin_PersistsTheHostItAuthenticatedAgainst(t *testing.T) {
	for _, file := range []string{"login.go", "register.go"} {
		t.Run(file, func(t *testing.T) {
			src := readCmdSource(t, file)

			envPath := strings.Index(src, `cfg.SetHost(cfg.APIHost)`)
			if envPath < 0 {
				t.Errorf("%s should persist an active GHAYMA_API_HOST through cfg.SetHost, so the config records the host the token was minted against", file)
			}
			if !strings.Contains(src, `os.Getenv("GHAYMA_API_HOST")`) {
				t.Errorf("%s should only persist the env host when it is actually set; an unconditional SetHost would write the default into a config that had none", file)
			}

			client := strings.Index(src, "api.NewClient(cfg)")
			if client < 0 {
				t.Fatalf("%s no longer builds a client from cfg", file)
			}
			if envPath > client {
				t.Errorf("%s applies the GHAYMA_API_HOST host after building the client; the first request would go to the old backend", file)
			}

			// login alone offers --host, and it must persist the same way.
			if file != "login.go" {
				return
			}
			flagPath := strings.Index(src, `cfg.SetHost(h)`)
			if flagPath < 0 {
				t.Error("login.go should persist --host through cfg.SetHost; a plain cfg.APIHost assignment is dropped on save")
			} else if flagPath > client {
				t.Error("login.go applies the --host value after building the client; the first request would go to the old backend")
			}
		})
	}
}

// TestProvisionCLIToken_ResetsStaleCredentials pins the ordering trap: the
// mint call itself is authenticated, so a stale PAT left over from an earlier
// login on this machine has to be cleared BEFORE the request goes out, or
// Bearer() prefers it and the mint 401s.
func TestProvisionCLIToken_ResetsStaleCredentials(t *testing.T) {
	src := readCmdSource(t, "login.go")
	start := strings.Index(src, "func provisionCLIToken(")
	if start < 0 {
		t.Fatal("login.go has no provisionCLIToken helper")
	}
	body := src[start:]

	reset := strings.Index(body, `cfg.APIToken = ""`)
	mint := strings.Index(body, "CreateAPIToken(")
	if reset < 0 || mint < 0 {
		t.Fatal("provisionCLIToken must clear the stale token and then mint a new one")
	}
	if reset > mint {
		t.Error("provisionCLIToken clears cfg.APIToken after minting; the stale token would be sent on the mint call")
	}
	if !strings.Contains(body, `cfg.APITokenID = ""`) {
		t.Error("provisionCLIToken must clear cfg.APITokenID alongside the token")
	}
}

// TestLogout_RevokesCLIToken pins that logout deletes the token it minted
// instead of orphaning it against the account's 10-token ceiling.
func TestLogout_RevokesCLIToken(t *testing.T) {
	src := readCmdSource(t, "logout.go")
	for _, want := range []string{"config.Load()", "cfg.APITokenID", "DeleteAPIToken("} {
		if !strings.Contains(src, want) {
			t.Errorf("logout.go should reference %q to revoke the CLI token", want)
		}
	}
	if strings.Index(src, "DeleteAPIToken(") > strings.Index(src, "os.Remove(configFile)") {
		t.Error("logout must revoke before deleting the config that holds the token id")
	}
}

// TestNoRawTokenGuards pins the sweep: every command gates on cfg.LoggedIn(),
// never on the JWT field alone — a token-only config is a valid login.
func TestNoRawTokenGuards(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if strings.Contains(readCmdSource(t, name), `cfg.Token == ""`) {
			t.Errorf("%s gates on cfg.Token alone; use cfg.LoggedIn()", name)
		}
	}
}
