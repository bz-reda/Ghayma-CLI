package cmd

import (
	"strings"
	"testing"
)

func TestDotenvQuote(t *testing.T) {
	cases := map[string]string{
		"plain":                  `'plain'`,
		"postgres://u:p$w@h/db":  `'postgres://u:p$w@h/db'`, // single quotes: no $ expansion in any dotenv dialect
		"":                       `''`,
		`it's`:                   `"it's"`,
		"line1\nline2":           `"line1\nline2"`,
		`say "hi"`:               `'say "hi"'`,
		"it's \"both\"":          `"it's \"both\""`,
		`back\slash and 'quote'`: `"back\\slash and 'quote'"`,
	}
	for in, want := range cases {
		if got := dotenvQuote(in); got != want {
			t.Errorf("dotenvQuote(%q) = %s; want %s", in, got, want)
		}
	}
}

func TestRenderDotenv_SortedHeaderedAndTrailingNewline(t *testing.T) {
	out := renderDotenv("# hdr", map[string]string{"B": "2", "A": "1", "NEXT_PUBLIC_X": "y"})
	want := "# hdr\nA='1'\nB='2'\nNEXT_PUBLIC_X='y'\n"
	if out != want {
		t.Errorf("renderDotenv =\n%q\nwant\n%q", out, want)
	}
	if got := renderDotenv("# hdr", nil); got != "# hdr\n" {
		t.Errorf("empty env = %q", got)
	}
}

func TestEnvPullRefusal(t *testing.T) {
	if err := envPullRefusal(true, true, true, true, ".env.local"); err == nil || !strings.Contains(err.Error(), "git rm --cached") {
		t.Errorf("a tracked file is refused even with --force, got %v", err)
	}
	if err := envPullRefusal(false, false, true, false, ".env.local"); err == nil || !strings.Contains(err.Error(), ".gitignore") || !strings.Contains(err.Error(), "--force") {
		t.Errorf("an unignored file in a repo is refused with the fix and the override, got %v", err)
	}
	if err := envPullRefusal(false, false, true, true, ".env.local"); err != nil {
		t.Errorf("--force allows an unignored file, got %v", err)
	}
	if err := envPullRefusal(false, true, true, false, ".env.local"); err != nil {
		t.Errorf("an ignored file is fine, got %v", err)
	}
	if err := envPullRefusal(false, false, false, false, ".env.local"); err != nil {
		t.Errorf("outside a repository nothing can be tracked, got %v", err)
	}
}

func TestPullHeaderNamesSiteAndWarns(t *testing.T) {
	h := pullHeader("shop", "main")
	if !strings.HasPrefix(h, "# ") || !strings.Contains(h, "shop/main") || !strings.Contains(h, "ghayma env pull") {
		t.Errorf("header = %q", h)
	}
	for _, line := range strings.Split(strings.TrimRight(h, "\n"), "\n") {
		if !strings.HasPrefix(line, "#") {
			t.Errorf("every header line must be a comment, got %q", line)
		}
	}
}
