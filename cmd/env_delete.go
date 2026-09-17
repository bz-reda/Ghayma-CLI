package cmd

import (
	"errors"
	"fmt"
	"net/http"

	"paas-cli/internal/api"
)

// Per-key env var deletion (Environments design §3, backend #368).
//
// Deleting one variable is not the same operation on a site that inherits: the
// key may be the site's own OVERRIDE of an inherited row — removing it brings
// the base site's value back — or a row the site only inherits, which cannot be
// removed from here at all. The replace-all PUT can express neither: it writes
// the site's own rows, so dropping an inherited key from the map is a no-op the
// user reads as a deletion while every deploy keeps injecting the value.

// envRouteMissing reports a 404 that is the ROUTE being absent rather than the
// key: the per-key refusals all carry a machine code, and a platform that
// predates the endpoint answers gin's bare 404.
func envRouteMissing(err error) bool {
	var apiErr *api.APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound && apiErr.Code == ""
}

// envDeleteLines renders one per-key outcome, and says whether the server
// refused it. The 409's sentence is printed as it came: it names the site that
// actually holds the variable, which is the one fact the user needs, and the
// server is where that wording belongs.
func envDeleteLines(key string, res *api.EnvVarDeletion, err error) (lines []string, refused bool) {
	if err != nil {
		var apiErr *api.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.Code {
			case api.CodeEnvVarInherited:
				return []string{
					fmt.Sprintf("❌ %s", apiErr.Message),
					fmt.Sprintf("   Override it here instead: ghayma env set %s=<value>", key),
				}, true
			case api.CodeEnvVarNotFound:
				return []string{fmt.Sprintf("ℹ️  %s is not set on this site — no change.", key)}, false
			}
		}
		return []string{fmt.Sprintf("❌ Failed to remove %s: %v", key, err)}, true
	}
	if res != nil && res.InheritedValueRestored {
		return []string{fmt.Sprintf("✅ %s: the override is gone — the value inherited%s applies again", key, fromClause(res.InheritedFrom))}, false
	}
	return []string{fmt.Sprintf("✅ %s removed", key)}, false
}

// deleteEnvKeys removes each key through the per-key endpoint. It reports
// false — having changed nothing — when the platform does not serve that route,
// so the caller can fall back to the read-modify-write path. Only the FIRST key
// can trigger that: once one delete has landed, a fallback would replay it.
func deleteEnvKeys(client *api.Client, projectID, siteID string, keys []string) bool {
	removed, refused := 0, 0
	for i, key := range keys {
		res, err := client.DeleteEnvVar(projectID, siteID, key)
		if i == 0 && envRouteMissing(err) {
			return false
		}
		lines, wasRefused := envDeleteLines(key, res, err)
		for _, line := range lines {
			fmt.Println(line)
		}
		switch {
		case wasRefused:
			refused++
		case err == nil:
			removed++
		}
	}
	if removed > 0 {
		fmt.Println("🔄 Redeploy to apply: ghayma deploy")
	}
	if refused > 0 {
		exitFn(1)
	}
	return true
}
