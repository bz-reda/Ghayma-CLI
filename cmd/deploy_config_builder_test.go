package cmd

import "testing"

func boolPtr(b bool) *bool { return &b }

// TestConfigBuilder: "auto" and "dockerfile" pass (trimmed), empty leaves the
// choice to the site, anything else stops the deploy naming the file.
func TestConfigBuilder(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"auto", builderAuto},
		{"dockerfile", builderDockerfile},
		{" dockerfile ", builderDockerfile},
	} {
		ctx := &SiteContext{ConfigPath: "/w/.ghayma.json", Site: SiteEntry{Builder: tc.in}}
		got, err := configBuilder(ctx)
		if err != nil || got != tc.want {
			t.Errorf("configBuilder(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}

	for _, tc := range []struct {
		ctx  *SiteContext
		want string
	}{
		{
			&SiteContext{ConfigPath: "/w/.ghayma.json", FromManifest: true, Site: SiteEntry{SiteSlug: "web", Builder: "Dockerfile"}},
			`site "web" has an unknown builder "Dockerfile" in /w/.ghayma.json (expected "auto" or "dockerfile")`,
		},
		{
			&SiteContext{ConfigPath: "/w/.ghayma.json", Site: SiteEntry{SiteSlug: "web", Builder: "railpack"}},
			`unknown builder "railpack" in /w/.ghayma.json (expected "auto" or "dockerfile")`,
		},
	} {
		_, err := configBuilder(tc.ctx)
		if err == nil || err.Error() != tc.want {
			t.Errorf("configBuilder(%q) error = %v; want %q", tc.ctx.Site.Builder, err, tc.want)
		}
	}
}
