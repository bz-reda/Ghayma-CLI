package cmd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"paas-cli/internal/api"
)

// cmdSourceFiles returns the non-test .go files in the cmd package. The
// branding pins scan source, not tests — test files intentionally reference
// the legacy .espacetech.json / .espacetechignore filenames for back-compat.
func cmdSourceFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read cmd dir: %v", err)
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, name)
	}
	if len(files) == 0 {
		t.Fatal("no cmd source files found")
	}
	return files
}

// TestNoLegacyCommandHints pins that no user-facing command hint references the
// old "espacetech " command name. The trailing SPACE is load-bearing: it
// matches hints like "espacetech login" but never the kept config filenames
// ".espacetech.json" / ".espacetechignore" (no space after the name).
func TestNoLegacyCommandHints(t *testing.T) {
	for _, name := range cmdSourceFiles(t) {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		src := string(data)
		for i, line := range strings.Split(src, "\n") {
			if strings.Contains(line, "espacetech ") {
				t.Errorf("%s:%d still has a legacy 'espacetech ' command hint: %s",
					name, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// TestNoLegacyBrandName pins that the old "Espace-Tech" brand string is gone
// from cmd source (Short/Long descriptions etc.).
func TestNoLegacyBrandName(t *testing.T) {
	for _, name := range cmdSourceFiles(t) {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(data), "Espace-Tech") {
			t.Errorf("%s still contains legacy brand literal %q", name, "Espace-Tech")
		}
	}
}

// TestLoginHintRebranded is a positive pin: the login hint must use the new
// "ghayma login" command name. Guards against an over-eager edit that drops
// the hint entirely instead of rebranding it.
func TestLoginHintRebranded(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(".", "init.go"))
	if err != nil {
		t.Fatalf("read init.go: %v", err)
	}
	if !strings.Contains(string(data), "ghayma login") {
		t.Errorf("init.go login hint not rebranded to 'ghayma login'")
	}
}

// legacyNameRe matches the pre-rebrand name in any spelling (Espace-Tech,
// espacetech, ESPACETECH_AUTH_*) but not "namespace" or "whitespace".
var legacyNameRe = regexp.MustCompile(`(?i)(?:^|[^a-z])espace|espace[-_ ]?tech`)

// legacyCompatLiterals are the only strings allowed to carry the old name:
// the legacy filenames still read for back-compat, never advertised.
var legacyCompatLiterals = map[string]bool{
	".espacetech.json":  true,
	".espacetechignore": true,
}

// TestNoLegacyNameInStringLiterals pins that no printed, prompted or help
// string in the module names the old brand. Comments may still describe the
// back-compat reads.
func TestNoLegacyNameInStringLiterals(t *testing.T) {
	root := ".."
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				s = lit.Value
			}
			if legacyCompatLiterals[s] {
				return true
			}
			for _, line := range strings.Split(s, "\n") {
				if legacyNameRe.MatchString(line) {
					t.Errorf("%s: string names the legacy brand: %q", fset.Position(lit.Pos()), strings.TrimSpace(line))
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
}

func TestNoLegacyNameInReadme(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	for i, line := range strings.Split(string(data), "\n") {
		if legacyNameRe.MatchString(line) {
			t.Errorf("README.md:%d names the legacy brand: %s", i+1, strings.TrimSpace(line))
		}
	}
}

// TestIgnoreRulesNoneFoundLine pins the deploy line shown when no ignore file
// exists: it names only the Ghayma files, though the legacy one is still read.
func TestIgnoreRulesNoneFoundLine(t *testing.T) {
	for _, rules := range []*api.IgnoreRules{nil, {}} {
		out := captureStdout(t, func() { printIgnoreRules(rules) })
		if !strings.Contains(out, "   (no .ghaymaignore or .dockerignore found)\n") {
			t.Errorf("printIgnoreRules(%v) = %q; want the .ghaymaignore/.dockerignore line", rules, out)
		}
	}
}

func TestConnectionsHelpNamesGhaymaAuth(t *testing.T) {
	if !strings.Contains(connectionsCmd.Long, "the GHAYMA_AUTH_* set") {
		t.Errorf("connections help must name the GHAYMA_AUTH_* set")
	}
}
