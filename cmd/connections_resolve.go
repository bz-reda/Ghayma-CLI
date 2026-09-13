package cmd

import (
	"fmt"
	"sort"
	"strings"

	"paas-cli/internal/api"
)

// Connections (2026-09-13). A connection says one SITE may use one SERVICE at
// one LEVEL — the single record behind the variables an app receives, the
// per-database network policy and the site's managed platform key. This file
// holds the pure halves of the commands: naming kinds, mapping the linked
// site onto a live one, matching a resource by name, and rendering.

// connectionKinds maps what a user types to the API's kind names.
var connectionKinds = map[string]string{
	"database": "database", "db": "database",
	"bucket": "bucket", "storage": "bucket",
	"auth": "auth_app", "auth_app": "auth_app", "auth-app": "auth_app",
}

// kindOrder is the documented listing order, database first.
var kindOrder = []string{"database", "bucket", "auth_app"}

// kindLevels mirrors the server's per-kind level sets, weakest first. The
// server stays the authority; this only makes a wrong --level fail before
// the request.
var kindLevels = map[string][]string{
	"database": {"connect"},
	"bucket":   {"read-write"},
	"auth_app": {"client", "admin"},
}

// adminNote is printed whenever a connection is made at the admin level —
// the same sentence the console shows next to that choice.
const adminNote = "admin lets this app manage all users of this auth app."

func parseConnectionKind(arg string) (string, error) {
	if kind, ok := connectionKinds[strings.ToLower(arg)]; ok {
		return kind, nil
	}
	return "", fmt.Errorf("unknown kind %q — use database, bucket or auth", arg)
}

func kindLabel(kind string) string {
	if kind == "auth_app" {
		return "auth app"
	}
	return kind
}

func defaultLevel(kind string) string {
	if levels := kindLevels[kind]; len(levels) > 0 {
		return levels[0]
	}
	return ""
}

// createHint names the command that creates a resource of the kind, for the
// "nothing to connect yet" message.
func createHint(kind string) string {
	switch kind {
	case "database":
		return "ghayma db create <name>"
	case "bucket":
		return "ghayma storage create <name>"
	default:
		return "ghayma auth create <name> --app-id <id>"
	}
}

func kindRank(kind string) int {
	for i, k := range kindOrder {
		if k == kind {
			return i
		}
	}
	return len(kindOrder)
}

// pickLiveSite maps the linked site (what the config names) onto the project's
// live site list: by id, then slug, then name, then "the only site". The
// config may carry a name with no id — init writes `site_name: main` before
// the backend materialises main — so the live list decides.
func pickLiveSite(sites []api.Site, entry SiteEntry) (*api.Site, error) {
	if entry.SiteID != "" {
		for i := range sites {
			if sites[i].ID == entry.SiteID {
				return &sites[i], nil
			}
		}
		return nil, fmt.Errorf("the linked site is no longer in this project — run 'ghayma link' to relink")
	}
	for _, want := range []string{entry.SiteSlug, entry.SiteName} {
		if want == "" {
			continue
		}
		for i := range sites {
			if strings.EqualFold(sites[i].Slug, want) || strings.EqualFold(sites[i].Name, want) {
				return &sites[i], nil
			}
		}
	}
	switch len(sites) {
	case 0:
		return nil, errNoSite
	case 1:
		return &sites[0], nil
	}
	return nil, fmt.Errorf("several sites in this project — pass --site <slug> (available: %s)", strings.Join(siteSlugs(sites), ", "))
}

func siteSlugs(sites []api.Site) []string {
	out := make([]string, len(sites))
	for i, s := range sites {
		out[i] = s.Slug
	}
	return out
}

// findConnectable locates the resource a user named within one kind, among the
// site's current connections and the project's still-available resources.
// Exactly one of held/avail is non-nil on success. Names are what the API
// lists: a database's or bucket's name, an auth app's app_id.
func findConnectable(view *api.SiteConnections, kind, name string) (*api.Connection, *api.AvailableConnection, error) {
	for i := range view.Connections {
		c := &view.Connections[i]
		if c.Kind == kind && strings.EqualFold(c.ResourceName, name) {
			return c, nil, nil
		}
	}
	for i := range view.Available {
		a := &view.Available[i]
		if a.Kind == kind && strings.EqualFold(a.ResourceName, name) {
			return nil, a, nil
		}
	}
	names := connectableNames(view, kind)
	if len(names) == 0 {
		return nil, nil, fmt.Errorf("this project has no %s yet — create one with: %s", kindLabel(kind), createHint(kind))
	}
	return nil, nil, fmt.Errorf("no %s named %q in this project (available: %s)", kindLabel(kind), name, strings.Join(names, ", "))
}

// connectableNames lists every resource of the kind the project has, held or
// not, sorted — the hint for a name that matched nothing.
func connectableNames(view *api.SiteConnections, kind string) []string {
	var names []string
	for _, c := range view.Connections {
		if c.Kind == kind {
			names = append(names, c.ResourceName)
		}
	}
	for _, a := range view.Available {
		if a.Kind == kind {
			names = append(names, a.ResourceName)
		}
	}
	sort.Strings(names)
	return names
}

// resolveLevel picks the level to send: "" when no flag was given (the server
// applies the kind's weakest), else the flag, normalised and checked against
// the levels the server listed for the resource — or, for an already-connected
// resource (the server lists levels only for unconnected ones), the kind's
// known set.
func resolveLevel(kind, flag string, accepted []string) (string, error) {
	if flag == "" {
		return "", nil
	}
	if len(accepted) == 0 {
		accepted = kindLevels[kind]
	}
	want := strings.ToLower(flag)
	for _, l := range accepted {
		if l == want {
			return l, nil
		}
	}
	return "", fmt.Errorf("level %q is not valid for a %s (accepted: %s)", flag, kindLabel(kind), strings.Join(accepted, ", "))
}

// renderConnectionsTable renders SITE / KIND / SERVICE / LEVEL, sites
// alphabetical, kinds in the documented order, names alphabetical. Widths fit
// the content so a long slug does not break the columns.
func renderConnectionsTable(rows []api.Connection) string {
	sorted := append([]api.Connection(nil), rows...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.SiteSlug != b.SiteSlug {
			return a.SiteSlug < b.SiteSlug
		}
		if kindRank(a.Kind) != kindRank(b.Kind) {
			return kindRank(a.Kind) < kindRank(b.Kind)
		}
		return a.ResourceName < b.ResourceName
	})

	header := []string{"SITE", "KIND", "SERVICE", "LEVEL"}
	widths := []int{len(header[0]), len(header[1]), len(header[2])}
	cells := make([][]string, 0, len(sorted))
	for _, r := range sorted {
		row := []string{r.SiteSlug, kindLabel(r.Kind), r.ResourceName, r.Level}
		for i := 0; i < 3; i++ {
			if len(row[i]) > widths[i] {
				widths[i] = len(row[i])
			}
		}
		cells = append(cells, row)
	}

	var b strings.Builder
	line := func(row []string) {
		fmt.Fprintf(&b, "   %-*s  %-*s  %-*s  %s\n", widths[0], row[0], widths[1], row[1], widths[2], row[2], row[3])
	}
	line(header)
	for _, row := range cells {
		line(row)
	}
	return b.String()
}
