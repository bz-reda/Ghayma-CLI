package cmd

import (
	"strings"
	"testing"
)

// TestDeployHeadline pins the line the user reads before an upload starts.
// The three per-app forms are the ones deploy printed before the manifest
// existed and must stay identical; the manifest forms have to say WHICH tree
// is being uploaded, because "app" and "workspace" send different tarballs
// and only the headline distinguishes them (2026-08-16).
func TestDeployHeadline(t *testing.T) {
	cases := []struct {
		name string
		ctx  SiteContext
		want string
	}{
		{
			name: "manifest app mode",
			ctx: SiteContext{
				FromManifest: true,
				ProjectName:  "taarefni",
				Site:         SiteEntry{SiteSlug: "admin", RootDirectory: "apps/taarefni-admin"},
			},
			want: "🚀 Deploying taarefni [site: admin] (uploading apps/taarefni-admin only)...",
		},
		{
			name: "manifest workspace mode",
			ctx: SiteContext{
				FromManifest:  true,
				ProjectName:   "taarefni",
				Site:          SiteEntry{SiteSlug: "admin", RootDirectory: "apps/taarefni-admin"},
				RootDirectory: "apps/taarefni-admin",
			},
			want: "🚀 Deploying taarefni [site: admin] (uploading the workspace, building apps/taarefni-admin)...",
		},
		{
			name: "per-app root_directory from config",
			ctx: SiteContext{
				ProjectName:    "taarefni",
				RootDirectory:  "apps/web",
				RootFromConfig: true,
			},
			want: "🚀 Deploying taarefni (monorepo: apps/web, from config)...",
		},
		{
			name: "per-app root_directory from the filesystem",
			ctx:  SiteContext{ProjectName: "taarefni", RootDirectory: "apps/web"},
			want: "🚀 Deploying taarefni (monorepo: apps/web)...",
		},
		{
			name: "per-app secondary site",
			ctx:  SiteContext{ProjectName: "taarefni", Site: SiteEntry{SiteName: "admin"}},
			want: "🚀 Deploying taarefni [site: admin]...",
		},
		{
			name: "per-app main site",
			ctx:  SiteContext{ProjectName: "taarefni", Site: SiteEntry{SiteName: "main"}},
			want: "🚀 Deploying taarefni...",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deployHeadline(&tc.ctx); got != tc.want {
				t.Errorf("deployHeadline()\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestDeploy_UsesSiteResolver pins the wiring: deploy resolves its target
// through the shared resolver and exposes --site, while keeping the legacy
// turbo scan-and-pick fallback for repos that have no manifest at all.
func TestDeploy_UsesSiteResolver(t *testing.T) {
	src := readCmdSource(t, "deploy.go")
	for _, want := range []string{
		"resolveSiteContext(",
		`"site"`,
		"findInitializedApps(",
		"errNoProjectConfig",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("deploy.go should reference %q", want)
		}
	}
}
