package api

import (
	"os"
	"strings"
	"testing"
)

// Concurrent deploys from one machine must never share an archive path.
func TestUploadArchiveHasUniqueTempName(t *testing.T) {
	src, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatal(err)
	}
	code := string(src)
	if strings.Contains(code, `"paas-source.tar.gz"`) {
		t.Fatal("the upload archive must not use a fixed temp file name")
	}
	if !strings.Contains(code, `os.CreateTemp(os.TempDir(), "ghayma-source-*.tar.gz")`) {
		t.Fatal("the upload archive must be created with os.CreateTemp and a unique pattern")
	}
}
