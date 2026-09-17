package cmd

import (
	"fmt"
	"sort"
	"strings"

	"paas-cli/internal/api"
)

// Cross-environment database connections (Environments design §3, Reda
// 2026-09-17): warn, do not block.
//
// A development site pointed at the database production writes to can corrupt
// real data — from a migration, a seed script, a test that truncates a table.
// The recommended path is a separate database for the non-production site,
// which the flat-name rule then exposes there as its own DATABASE_URL with no
// further configuration. But there are legitimate reasons to share one (a
// read-only reporting app, a staging site deliberately pointed at a copy), so
// the connection is never refused: it is named, confirmed, and made.
//
// Everything this needs is already readable — the project's connections and its
// sites with their kinds — so it is a client-side pre-flight with no API change.
// It is also FAIL-OPEN: a pre-flight that cannot read is a pre-flight that says
// nothing, never a connect that fails for a reason the user did not ask about.

// siteKind pairs a site slug with its environment, the shape the warning needs.
type siteKind struct {
	Slug        string
	Environment string
}

// isProductionKind treats an unknown environment as production, matching the
// backend's own defensive read (a row written before the column reads as
// production) — the safe direction for a warning: it may say too much, never
// too little.
func isProductionKind(environment string) bool {
	return environment != api.EnvironmentStaging && environment != api.EnvironmentDevelopment
}

// crossEnvWarning is the sentence to print before connecting a database, or ""
// when the two sides are on the same side of the production line.
//
// Both directions are covered, because the danger is the SHARING, not the
// order: connecting dev to production's database, and connecting production to
// a database a dev site already has its hands on, are the same accident.
func crossEnvWarning(dbName string, target siteKind, others []siteKind) string {
	targetIsProd := isProductionKind(target.Environment)
	var conflicting []siteKind
	for _, other := range others {
		if other.Slug == target.Slug {
			continue
		}
		if isProductionKind(other.Environment) != targetIsProd {
			conflicting = append(conflicting, other)
		}
	}
	if len(conflicting) == 0 {
		return ""
	}
	sort.Slice(conflicting, func(i, j int) bool { return conflicting[i].Slug < conflicting[j].Slug })

	used := make([]string, 0, len(conflicting))
	for _, c := range conflicting {
		used = append(used, fmt.Sprintf("%s site '%s'", kindWord(c.Environment), c.Slug))
	}
	if targetIsProd {
		// The production site is the one being connected: the database is
		// already exposed to a non-production site, so that is the one to move
		// off it.
		return fmt.Sprintf("⚠️  %s is used by %s. A production database shared with a non-production site can be corrupted from there; consider a separate database for %s instead.",
			dbName, strings.Join(used, " and "), quotedSlugs(conflicting))
	}
	return fmt.Sprintf("⚠️  %s is used by %s. A %s site sharing a production database can corrupt real data; consider a separate database for '%s' instead.",
		dbName, strings.Join(used, " and "), kindWord(target.Environment), target.Slug)
}

// kindWord names an environment in prose. An empty one reads as production,
// exactly as isProductionKind treats it.
func kindWord(environment string) string {
	if environment == "" {
		return api.EnvironmentProduction
	}
	return environment
}

func quotedSlugs(sites []siteKind) string {
	out := make([]string, len(sites))
	for i, s := range sites {
		out[i] = "'" + s.Slug + "'"
	}
	return strings.Join(out, " and ")
}

// databaseSiteKinds lists the OTHER sites already connected to one database,
// with their environments. Fail-open: any read that does not work yields no
// sites, so the caller simply has nothing to warn about.
func databaseSiteKinds(client *api.Client, projectID, resourceID string) []siteKind {
	connections, err := client.ListConnections(projectID, "")
	if err != nil {
		return nil
	}
	sites, err := client.ListSites(projectID)
	if err != nil {
		return nil
	}
	kindBySlug := make(map[string]string, len(sites))
	idToSlug := make(map[string]string, len(sites))
	for _, s := range sites {
		kindBySlug[s.Slug] = s.Environment
		idToSlug[s.ID] = s.Slug
	}

	seen := map[string]bool{}
	var out []siteKind
	for _, c := range connections {
		if c.Kind != "database" || c.ResourceID != resourceID {
			continue
		}
		slug := c.SiteSlug
		if slug == "" {
			slug = idToSlug[c.SiteID]
		}
		if slug == "" || seen[slug] {
			continue
		}
		seen[slug] = true
		out = append(out, siteKind{Slug: slug, Environment: kindBySlug[slug]})
	}
	return out
}

// confirmCrossEnv prints the warning and asks. --yes skips the question, not the
// warning: the user still sees what they are doing.
func confirmCrossEnv(warning string, yes bool, ask func() string) bool {
	if warning == "" {
		return true
	}
	fmt.Println(warning)
	if yes {
		return true
	}
	fmt.Print("   Connect anyway? [y/N] ")
	answer := strings.TrimSpace(ask())
	return answer == "y" || answer == "Y"
}
