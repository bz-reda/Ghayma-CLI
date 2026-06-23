package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultAPIHostIsGhayma pins the built-in default API host to the ghayma
// backend. Env/config still override this when set; this guards the fallback.
func TestDefaultAPIHostIsGhayma(t *testing.T) {
	const want = "https://api.ghayma.tech"

	// Point HOME at an empty dir so Load() can't find a real config file and
	// must fall back to the compiled-in default.
	t.Setenv("HOME", t.TempDir())

	if got := Load().APIHost; got != want {
		t.Fatalf("default APIHost = %q, want %q", got, want)
	}
}

// TestNoLegacyHostLiteralInConfig ensures the old espace-tech API host literal
// is fully gone from config.go.
func TestNoLegacyHostLiteralInConfig(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(".", "config.go"))
	if err != nil {
		t.Fatalf("read config.go: %v", err)
	}
	if strings.Contains(string(src), "api.espace-tech.com") {
		t.Fatal("config.go still contains the legacy literal \"api.espace-tech.com\"")
	}
}
