package cmd

import (
	"os"
	"strings"
	"testing"
)

func TestProjectConfigHasBuildFields(t *testing.T) {
	for _, f := range []string{"init.go", "deploy.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		s := string(src)
		for _, tag := range []string{`json:"build_command,omitempty"`, `json:"install_command,omitempty"`, `json:"start_command,omitempty"`, `json:"output_directory,omitempty"`, `json:"port,omitempty"`} {
			if !strings.Contains(s, tag) {
				t.Errorf("%s missing build-config field %q", f, tag)
			}
		}
	}
}
