package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"paas-cli/internal/api"
	"paas-cli/internal/config"
)

func tokenRows() []api.APITokenInfo {
	revoked := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	return []api.APITokenInfo{
		{ID: "tok-1", Name: "ci", TokenPrefix: "gh_aaaaaaa", Scope: "deploy"},
		{ID: "tok-2", Name: "laptop", TokenPrefix: "gh_bbbbbbb", Scope: "full"},
		{ID: "tok-3", Name: "dup", TokenPrefix: "gh_ccccccc", Scope: "deploy"},
		{ID: "tok-4", Name: "dup", TokenPrefix: "gh_ddddddd", Scope: "deploy"},
		{ID: "tok-5", Name: "gone", TokenPrefix: "gh_eeeeeee", Scope: "deploy", RevokedAt: &revoked},
	}
}

func TestSelectToken(t *testing.T) {
	rows := tokenRows()
	for ref, want := range map[string]string{"tok-2": "tok-2", "gh_aaaaaaa": "tok-1", "laptop": "tok-2"} {
		got, err := selectToken(rows, ref)
		if err != nil || got.ID != want {
			t.Errorf("selectToken(%q) = %v, %v; want %s", ref, got, err, want)
		}
	}

	if _, err := selectToken(rows, "nope"); err == nil || err.Error() != `no token matches "nope"` {
		t.Errorf("no match: err = %v", err)
	}
	// Revoked rows are never a target, by any selector.
	for _, ref := range []string{"gone", "tok-5", "gh_eeeeeee"} {
		if _, err := selectToken(rows, ref); err == nil || !strings.Contains(err.Error(), "no token matches") {
			t.Errorf("revoked %q: err = %v; want no match", ref, err)
		}
	}
	// A prefix is matched exactly, never as a substring.
	if _, err := selectToken(rows, "gh_aaa"); err == nil {
		t.Error("a partial prefix must not match")
	}

	_, err := selectToken(rows, "dup")
	if err == nil || !strings.HasPrefix(err.Error(), `"dup" matches 2 tokens — use the id`) {
		t.Fatalf("ambiguous: err = %v", err)
	}
	if !strings.Contains(err.Error(), "tok-3") || !strings.Contains(err.Error(), "tok-4") {
		t.Errorf("ambiguous error must list the candidates, got %q", err)
	}
}

func TestTokenProjectsColumn(t *testing.T) {
	if got := tokenProjectsColumn(nil, nil); got != "*" {
		t.Errorf("unrestricted = %q; want *", got)
	}
	got := tokenProjectsColumn([]string{"p1", "p2"}, []api.TokenProject{{ID: "p1", Slug: "shop"}, {ID: "p2", Slug: "blog"}})
	if got != "shop,blog" {
		t.Errorf("restricted = %q; want shop,blog", got)
	}
	// A project the listing could not name still shows, by id.
	if got := tokenProjectsColumn([]string{"p9"}, nil); got != "p9" {
		t.Errorf("unnamed = %q; want p9", got)
	}
}

func TestRenderTokenTable_RevokedAndColumns(t *testing.T) {
	used := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	rows := tokenRows()
	rows[0].LastUsedAt = &used
	rows[0].CreatedAt = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	out := captureStdout(t, func() { renderTokenTable(visibleTokens(rows, false)) })
	if !strings.Contains(out, "PREFIX") || !strings.Contains(out, "LAST USED") || !strings.Contains(out, "CREATED") {
		t.Errorf("header missing, got %q", out)
	}
	if strings.Contains(out, "gone") {
		t.Errorf("revoked row shown without --all: %q", out)
	}
	if !strings.Contains(out, "2026-09-21") || !strings.Contains(out, "never") {
		t.Errorf("last-used / expiry cells wrong: %q", out)
	}

	out = captureStdout(t, func() { renderTokenTable(visibleTokens(rows, true)) })
	var goneLine string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "gone") {
			goneLine = l
		}
	}
	if !strings.Contains(goneLine, "REVOKED") {
		t.Errorf("revoked row must read REVOKED with --all, got %q", goneLine)
	}
}

func TestRenderTokenSecret(t *testing.T) {
	out := captureStdout(t, func() {
		renderTokenSecret(&api.CreatedAPIToken{ID: "tok-1", Name: "ci", Token: "gh_secret", Scope: "deploy"})
	})
	if !strings.Contains(out, "Token (shown once): gh_secret\n") || !strings.Contains(out, "Save it now — it will not be shown again.") {
		t.Errorf("secret block wrong: %q", out)
	}
}

func TestTokenFailure(t *testing.T) {
	if got := tokenFailure(&api.APIError{Status: 404, Message: "token not found"}, "ci", "revoke the token"); got != `no token matches "ci"` {
		t.Errorf("404 = %q", got)
	}
	msg := "a token cannot grant a project it does not reach"
	if got := tokenFailure(&api.APIError{Status: 403, Message: msg, Code: "projects_exceed_token"}, "", "create the token"); got != msg {
		t.Errorf("403 = %q; want the server's message verbatim", got)
	}
}

// tokenConfig points GHAYMA_CONFIG at a temp file holding the given config.
func tokenConfig(t *testing.T, apiHost string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cli.json")
	t.Setenv("GHAYMA_CONFIG", path)
	t.Setenv("GHAYMA_API_HOST", "")
	cfg := &config.Config{Token: "jwt", APIToken: "gh_old", APITokenID: "tok-1", Email: "a@b.c"}
	cfg.SetHost(apiHost)
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	return config.Load()
}

func tokenRotateStub(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/rotate") {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		io.WriteString(w, `{"id":"tok-new","name":"ci","token":"gh_new","scope":"deploy","expires_at":null}`)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestRotateToken_OwnUpdatesConfig(t *testing.T) {
	ts := tokenRotateStub(t)
	cfg := tokenConfig(t, ts.URL)
	stubExit(t)

	out := captureStdout(t, func() {
		rotateToken(api.NewClient(cfg), cfg, &api.APITokenInfo{ID: "tok-1", Name: "ci"}, 90, false)
	})
	saved := config.Load()
	if saved.APIToken != "gh_new" || saved.APITokenID != "tok-new" {
		t.Errorf("config = %s/%s; want gh_new/tok-new", saved.APIToken, saved.APITokenID)
	}
	if saved.Email != "a@b.c" || saved.Token != "jwt" {
		t.Errorf("rest of the config must survive, got %+v", saved)
	}
	if !strings.Contains(out, "This CLI's own token was rotated; the config was updated.") {
		t.Errorf("own-token note missing: %q", out)
	}
}

func TestRotateToken_OtherLeavesConfig(t *testing.T) {
	ts := tokenRotateStub(t)
	cfg := tokenConfig(t, ts.URL)
	stubExit(t)

	out := captureStdout(t, func() {
		rotateToken(api.NewClient(cfg), cfg, &api.APITokenInfo{ID: "tok-2", Name: "ci"}, 90, true)
	})
	if saved := config.Load(); saved.APIToken != "gh_old" || saved.APITokenID != "tok-1" {
		t.Errorf("config changed for another token: %s/%s", saved.APIToken, saved.APITokenID)
	}
	var decoded api.CreatedAPIToken
	if err := json.Unmarshal([]byte(out), &decoded); err != nil || decoded.Token != "gh_new" {
		t.Errorf("--json must print only the created token, got %q (%v)", out, err)
	}
}

func TestRevokeToken_OwnAsksAgainAndClears(t *testing.T) {
	var deleted string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deleted = r.Method + " " + r.URL.Path
		io.WriteString(w, `{"message":"token revoked"}`)
	}))
	t.Cleanup(ts.Close)
	cfg := tokenConfig(t, ts.URL)
	stubExit(t)
	own := &api.APITokenInfo{ID: "tok-1", Name: "ci", TokenPrefix: "gh_aaaaaaa"}

	// Declining the second question keeps the token.
	answers := []string{"y", "n"}
	ask := func() string { a := answers[0]; answers = answers[1:]; return a }
	out := captureStdout(t, func() { revokeToken(api.NewClient(cfg), cfg, own, false, ask) })
	if deleted != "" || !strings.Contains(out, "This is the token this CLI is logged in with; you will have to run ghayma login again. Continue? [y/N]") {
		t.Errorf("declined own revoke: deleted=%q out=%q", deleted, out)
	}

	// --yes skips both questions but still says what it means.
	out = captureStdout(t, func() {
		revokeToken(api.NewClient(cfg), cfg, own, true, func() string { t.Fatal("asked despite --yes"); return "" })
	})
	if deleted != "DELETE /api/v1/tokens/tok-1" {
		t.Errorf("request = %q; want DELETE /api/v1/tokens/tok-1", deleted)
	}
	if !strings.Contains(out, "you will have to run ghayma login again") {
		t.Errorf("--yes must still print the note: %q", out)
	}
	if saved := config.Load(); saved.APIToken != "" || saved.APITokenID != "" {
		t.Errorf("revoked own token left in the config: %s/%s", saved.APIToken, saved.APITokenID)
	}
}

func TestTokenCommandsRegistered(t *testing.T) {
	var names []string
	for _, c := range tokenCmd.Commands() {
		names = append(names, c.Name())
	}
	got := strings.Join(names, ",")
	for _, want := range []string{"create", "list", "revoke", "rotate"} {
		if !strings.Contains(got, want) {
			t.Errorf("token %s not registered (have %s)", want, got)
		}
	}
}
