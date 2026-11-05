package cmd

import (
	"fmt"
	"strings"
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
