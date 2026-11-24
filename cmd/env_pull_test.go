package cmd

import (
	"strings"
	"testing"

	"paas-cli/internal/api"
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
	h := pullHeader("shop", "main", true)
	if !strings.HasPrefix(h, "# ") || !strings.Contains(h, "shop/main") || !strings.Contains(h, "ghayma env pull") {
		t.Errorf("header = %q", h)
	}
	for _, line := range strings.Split(strings.TrimRight(h, "\n"), "\n") {
		if !strings.HasPrefix(line, "#") {
			t.Errorf("every header line must be a comment, got %q", line)
		}
	}
}

func TestPullHeaderSaysWhichSetTheFileHolds(t *testing.T) {
	local, all := pullHeader("shop", "main", true), pullHeader("shop", "main", false)
	if !strings.Contains(local, "local development") || strings.Contains(local, "exactly what the deployed app receives") {
		t.Errorf("local header = %q", local)
	}
	if !strings.Contains(all, "exactly what the deployed app receives") {
		t.Errorf("--all header = %q", all)
	}
}

func TestLocalViewNotes(t *testing.T) {
	// One auth app: both kinds left out, one address swapped.
	notes := localViewNotes(&api.EffectiveEnv{
		View:      api.ViewLocal,
		Omitted:   api.EffectiveOmitted{LegacyAliases: 8, PerAppDuplicates: 4},
		Rewritten: []string{"GHAYMA_AUTH_URL"},
	})
	if len(notes) != 2 {
		t.Fatalf("notes = %q; want two lines", notes)
	}
	for _, want := range []string{"8 legacy aliases", "4 per-app duplicates", "ghayma env pull --all"} {
		if !strings.Contains(notes[0], want) {
			t.Errorf("first note %q lacks %q", notes[0], want)
		}
	}
	if !strings.HasPrefix(notes[1], "GHAYMA_AUTH_URL holds the public address") {
		t.Errorf("second note = %q", notes[1])
	}

	// Singular forms, and several swapped names.
	notes = localViewNotes(&api.EffectiveEnv{
		View:      api.ViewLocal,
		Omitted:   api.EffectiveOmitted{LegacyAliases: 1},
		Rewritten: []string{"GHAYMA_AUTH_URL", "GHAYMA_AUTH_URL_SHOP"},
	})
	if !strings.Contains(notes[0], "1 legacy alias —") || !strings.Contains(notes[1], "GHAYMA_AUTH_URL, GHAYMA_AUTH_URL_SHOP hold the public address") {
		t.Errorf("notes = %q", notes)
	}

	// Nothing to say: an older platform (no view), --all, or a view that changed nothing.
	for name, e := range map[string]*api.EffectiveEnv{
		"nil":             nil,
		"older platform":  {Omitted: api.EffectiveOmitted{LegacyAliases: 8}},
		"nothing changed": {View: api.ViewLocal},
	} {
		if got := localViewNotes(e); len(got) != 0 {
			t.Errorf("%s: notes = %q; want none", name, got)
		}
	}
}

func TestLocalViewNotes_NamesAddressesThatStayInsideThePlatform(t *testing.T) {
	notes := localViewNotes(&api.EffectiveEnv{
		View: api.ViewLocal,
		Env: map[string]string{
			"DATABASE_URL":    "postgres://u:secret@pg-x.databases.svc.cluster.local:5432/db",
			"GHAYMA_AUTH_URL": "http://auth-service.paas-system.svc.cluster.local:9000/v1/app",
			"LOG_LEVEL":       "debug",
		},
	})
	if len(notes) != 1 || !strings.HasPrefix(notes[0], "DATABASE_URL, GHAYMA_AUTH_URL point inside the platform") || !strings.Contains(notes[0], "ghayma connect --local") {
		t.Fatalf("notes = %q", notes)
	}
	if strings.Contains(notes[0], "secret") || strings.Contains(notes[0], "svc.cluster.local") {
		t.Errorf("the note prints a value: %q", notes[0])
	}
	one := localViewNotes(&api.EffectiveEnv{View: api.ViewLocal, Env: map[string]string{"MONGODB_URI": "mongodb://m.databases.svc.cluster.local/x"}})
	if len(one) != 1 || !strings.HasPrefix(one[0], "MONGODB_URI points inside") {
		t.Errorf("notes = %q", one)
	}
}
