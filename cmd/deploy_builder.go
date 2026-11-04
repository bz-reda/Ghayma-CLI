package cmd

import (
	"fmt"
	"strings"

	"paas-cli/internal/api"
)

// A site's build choice: Ghayma generates the build, or it builds from the
// site's Dockerfile.
const (
	builderAuto       = "auto"
	builderDockerfile = "dockerfile"
)

// configBuilder returns the config's "builder" for the site being deployed,
// trimmed. Empty leaves the choice to the site; any value the platform does not
// know stops the deploy before the upload.
func configBuilder(ctx *SiteContext) (string, error) {
	b := strings.TrimSpace(ctx.Site.Builder)
	switch b {
	case "", builderAuto, builderDockerfile:
		return b, nil
	}
	if ctx.FromManifest {
		return "", fmt.Errorf("site %q has an unknown builder %q in %s (expected %q or %q)",
			siteLabel(ctx.Site), ctx.Site.Builder, ctx.ConfigPath, builderAuto, builderDockerfile)
	}
	return "", fmt.Errorf("unknown builder %q in %s (expected %q or %q)",
		ctx.Site.Builder, ctx.ConfigPath, builderAuto, builderDockerfile)
}

// deployBuilder is the builder this deploy's build runs with: the config's,
// which the server stores on the site before building, else the site's stored
// one. A server that sends none still gates on the project toggle, read only
// then. "" means it could not be read.
func deployBuilder(fromConfig, fromSite string, projectFlag func() (bool, error)) string {
	b := fromConfig
	if b == "" {
		b = fromSite
	}
	switch {
	case b == builderDockerfile:
		return builderDockerfile
	case b != "":
		return builderAuto
	}
	enabled, err := projectFlag()
	switch {
	case err != nil:
		return ""
	case enabled:
		return builderDockerfile
	default:
		return builderAuto
	}
}

// siteBuilderOf returns the stored builder of the site the upload lands on:
// the linked site, else the one the server picks. "" when the list does not
// name it or predates the field.
func siteBuilderOf(ctx *SiteContext, sites []api.Site) string {
	var site *api.Site
	if id := ctx.Site.SiteID; id != "" {
		for i := range sites {
			if sites[i].ID == id {
				site = &sites[i]
				break
			}
		}
	} else {
		site = serverPickedSite(sites)
	}
	if site == nil {
		return ""
	}
	return strings.TrimSpace(site.BuildSettings.Builder)
}
