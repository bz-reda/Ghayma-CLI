package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"paas-cli/internal/api"
)

// External access (Connections phase 3c, 2026-09-19). An external principal is
// a NAMED identity outside Ghayma that may reach one database or one bucket
// with its own credential — a site connection minus the site. This file holds
// the pure halves of the `ghayma access` family: naming kinds, matching a
// resource and a principal by name, the collapse rule, and every line the
// commands print.

// accessKinds is what a user may type for a kind that HAS external access.
// Deliberately not parseConnectionKind: that one also accepts `auth`, and an
// auth app has no external principal to mint (its external access is its
// restricted project keys), so the two vocabularies are not the same.
var accessKinds = map[string]string{
	"database": "database", "db": "database",
	"bucket": "bucket", "storage": "bucket",
}

// authAccessRefusal is the answer for `ghayma access … auth …`. The CLI has no
// project-keys command family — keys are minted in the console — so the pointer
// names the console page rather than a command that does not exist.
const authAccessRefusal = "An auth app has no external principals — its external access IS its restricted project keys.\n" +
	"   Create or revoke one in the console: Project → Settings → API keys (https://dash.ghayma.cloud)."

func parseAccessKind(arg string) (string, error) {
	lower := strings.ToLower(arg)
	if kind, ok := accessKinds[lower]; ok {
		return kind, nil
	}
	if _, isConnectable := connectionKinds[lower]; isConnectable {
		return "", errors.New(authAccessRefusal)
	}
	return "", fmt.Errorf("unknown kind %q — use database or bucket", arg)
}

// accessTarget is the resolved (project, resource) an access command acts on.
// The API addresses a resource by UUID, never by name, so every command
// resolves the name the user typed before it can call anything.
type accessTarget struct {
	ProjectID    string
	ProjectName  string
	Kind         string
	ResourceID   string
	ResourceName string
}

// resourceHint names the command that creates a resource of the kind.
func resourceHint(kind string) string {
	if kind == "bucket" {
		return "ghayma storage create <name>"
	}
	return "ghayma db create <name>"
}

// pickResource matches the name the user typed against the project's resources
// of one kind. names must already be narrowed to this project: a name is only
// unique inside one.
func pickResource(kind, name string, ids, names []string) (string, string, error) {
	for i := range names {
		if strings.EqualFold(names[i], name) {
			return ids[i], names[i], nil
		}
	}
	if len(names) == 0 {
		return "", "", fmt.Errorf("this project has no %s yet — create one with: %s", kind, resourceHint(kind))
	}
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	return "", "", fmt.Errorf("no %s named %q in this project (available: %s)", kind, name, strings.Join(sorted, ", "))
}

// findPrincipal locates the principal a user named among one resource's rows.
// A full id is accepted too, which is the way out of the one case a name cannot
// answer: two live principals sharing a name.
func findPrincipal(rows []api.ExternalAccess, name string) (*api.ExternalAccess, error) {
	for i := range rows {
		if rows[i].ID == name {
			return &rows[i], nil
		}
	}
	var matched []*api.ExternalAccess
	for i := range rows {
		if strings.EqualFold(rows[i].Name, name) {
			matched = append(matched, &rows[i])
		}
	}
	var live []*api.ExternalAccess
	for _, row := range matched {
		if row.RevokedAt == "" {
			live = append(live, row)
		}
	}
	switch {
	case len(live) == 1:
		return live[0], nil
	case len(live) > 1:
		ids := make([]string, len(live))
		for i, row := range live {
			ids[i] = row.ID
		}
		sort.Strings(ids)
		return nil, fmt.Errorf("several principals are named %q — name the one you mean by its id (%s)", name, strings.Join(ids, ", "))
	case len(matched) > 0:
		// Every match is revoked: hand the row back and let the command decide
		// (revoke says "no change", rotate and allow earn the server's 409).
		return matched[0], nil
	}
	return nil, fmt.Errorf("no external principal named %q here%s", name, principalHint(rows))
}

func principalHint(rows []api.ExternalAccess) string {
	var names []string
	for _, row := range rows {
		if row.RevokedAt == "" {
			names = append(names, row.Name)
		}
	}
	if len(names) == 0 {
		return " — this resource has no principal in force"
	}
	sort.Strings(names)
	return " (in force: " + strings.Join(names, ", ") + ")"
}

// collapseNote is the allowlist collapse rule, stated about the rows it is
// given. The front door filters by source IP BEFORE any authentication, so it
// cannot tell which principal a connection belongs to: while one principal has
// no allowlist, no IP filter can be enforced at all without locking that one
// out. Empty when nothing collapses.
func collapseNote(rows []api.ExternalAccess) string {
	var unrestricted []string
	restricted := false
	for _, row := range rows {
		if !isInForce(row) {
			continue
		}
		if len(row.AllowCIDRs) == 0 {
			unrestricted = append(unrestricted, row.Name)
			continue
		}
		restricted = true
	}
	if !restricted || len(unrestricted) == 0 {
		return ""
	}
	sort.Strings(unrestricted)
	return fmt.Sprintf("ℹ️  %s no allowlist, so no IP filter is enforced at the front door while %s in force.\n"+
		"   Give every principal an allowlist, or expect none of them to be filtered.",
		subjectHas(unrestricted), subjectIs(len(unrestricted)))
}

func subjectHas(names []string) string {
	if len(names) == 1 {
		return "'" + names[0] + "' has"
	}
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = "'" + n + "'"
	}
	return strings.Join(quoted, ", ") + " have"
}

func subjectIs(n int) string {
	if n == 1 {
		return "it is"
	}
	return "they are"
}

// isInForce is the row's own view of itself: the server sends `active`, which
// already accounts for both the revocation and the expiry.
func isInForce(row api.ExternalAccess) bool { return row.Active }

// accessStatus is the STATUS column. An expiry that has passed but has not yet
// been swept (the sweep runs on a 2-minute tick) reads as expired, not active.
func accessStatus(row api.ExternalAccess) string {
	switch {
	case row.RevokedAt != "":
		return "revoked"
	case row.Active:
		return "active"
	case row.ExpiresAt != "":
		return "expired"
	}
	return "inactive"
}

// shortDate trims an RFC3339 timestamp to its date. Anything unparseable is
// printed as it came: the server's spelling beats a guess.
func shortDate(s string) string {
	if s == "" {
		return "—"
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format("2006-01-02")
	}
	return s
}

func allowSummary(cidrs []string) string {
	if len(cidrs) == 0 {
		return "any source"
	}
	return strings.Join(cidrs, ",")
}

// renderAccessRows renders the external half of the listing: NAME / LEVEL /
// ALLOWLIST / EXPIRES / LAST USED / STATUS, in force first, then alphabetical.
//
// There is no column for a secret and no field to fill one from: a credential
// is readable exactly once, at the moment it is minted.
func renderAccessRows(rows []api.ExternalAccess) string {
	sorted := append([]api.ExternalAccess(nil), rows...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if isInForce(sorted[i]) != isInForce(sorted[j]) {
			return isInForce(sorted[i])
		}
		return strings.ToLower(sorted[i].Name) < strings.ToLower(sorted[j].Name)
	})

	header := []string{"NAME", "LEVEL", "ALLOWLIST", "EXPIRES", "LAST USED", "STATUS"}
	cells := make([][]string, 0, len(sorted))
	for _, row := range sorted {
		cells = append(cells, []string{
			row.Name, row.Level, allowSummary(row.AllowCIDRs),
			shortDate(row.ExpiresAt), shortDate(row.LastUsedAt), accessStatus(row),
		})
	}
	return renderColumns(header, cells)
}

// renderSiteRows renders the app half: which of the project's sites may use
// this resource, from the connections listing beside it.
func renderSiteRows(rows []api.Connection) string {
	sorted := append([]api.Connection(nil), rows...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].SiteSlug < sorted[j].SiteSlug })
	cells := make([][]string, 0, len(sorted))
	for _, row := range sorted {
		cells = append(cells, []string{row.SiteSlug, row.Level})
	}
	return renderColumns([]string{"APP", "LEVEL"}, cells)
}

// renderColumns lays out a header plus rows, every column as wide as its widest
// cell so a long name cannot break the table.
func renderColumns(header []string, cells [][]string) string {
	widths := make([]int, len(header))
	for i, h := range header {
		widths[i] = len(h)
	}
	for _, row := range cells {
		for i, cell := range row {
			if len([]rune(cell)) > widths[i] {
				widths[i] = len([]rune(cell))
			}
		}
	}
	var b strings.Builder
	line := func(row []string) {
		b.WriteString("   ")
		for i, cell := range row {
			if i == len(row)-1 {
				b.WriteString(cell)
				break
			}
			pad := widths[i] - len([]rune(cell))
			b.WriteString(cell + strings.Repeat(" ", pad) + "  ")
		}
		b.WriteString("\n")
	}
	line(header)
	for _, row := range cells {
		line(row)
	}
	return b.String()
}

// printAccessListing is the whole `ghayma access <kind> <name>` output: the
// apps that use the resource from inside, then the principals that reach it
// from outside, then the collapse note when the two allowlist kinds are mixed.
func printAccessListing(target *accessTarget, conns []api.Connection, rows []api.ExternalAccess) {
	fmt.Printf("🔐 Access to %s '%s'\n\n", target.Kind, target.ResourceName)

	fmt.Println("   Apps in this project")
	if len(conns) == 0 {
		fmt.Printf("   none — connect one with: ghayma connect %s %s\n", target.Kind, target.ResourceName)
	} else {
		fmt.Print(renderSiteRows(conns))
	}

	fmt.Println("\n   External principals")
	if len(rows) == 0 {
		fmt.Printf("   none — add one with: ghayma access add %s %s --name <principal>\n", target.Kind, target.ResourceName)
		return
	}
	fmt.Print(renderAccessRows(rows))
	if note := collapseNote(rows); note != "" {
		fmt.Println()
		fmt.Println("   " + note)
	}
}

// credentialWarning is the sentence printed under a freshly minted secret. The
// server sends its own; this is what stands in when it did not.
const credentialWarning = "this credential is shown once and cannot be retrieved again"

// printCredential prints a one-time credential and the warning that goes with
// it. Called by add and by rotate, which are the only two moments a secret
// exists outside the engine.
func printCredential(target *accessTarget, principal string, out *api.ExternalAccessSecret) {
	cred := out.Credential
	if cred == nil {
		fmt.Println("⚠️  The server returned no credential — nothing to store.")
		return
	}
	fmt.Printf("\n🔑 Credential for '%s' on %s '%s'\n\n", principal, target.Kind, target.ResourceName)
	if cred.URI != "" {
		fmt.Printf("   URI:        %s\n", cred.URI)
	}
	if cred.Endpoint != "" {
		fmt.Printf("   Endpoint:   %s\n", cred.Endpoint)
	}
	if cred.Bucket != "" {
		fmt.Printf("   Bucket:     %s\n", cred.Bucket)
	}
	if target.Kind == "bucket" {
		fmt.Printf("   Access key: %s\n", cred.Ref)
		fmt.Printf("   Secret key: %s\n", cred.Secret)
	} else {
		fmt.Printf("   Username:   %s\n", cred.Ref)
		fmt.Printf("   Password:   %s\n", cred.Secret)
	}

	warning := out.Warning
	if warning == "" {
		warning = credentialWarning
	}
	fmt.Printf("\n⚠️  %s — store it now.\n", warning)
	fmt.Printf("   Lost it? Mint a new one with: ghayma access rotate %s %s %s\n", target.Kind, target.ResourceName, principal)
}

// accessFailure turns the server's refusal into the sentence that names the way
// out. The 400s are printed VERBATIM — the server validates names, levels,
// CIDRs and expiries, and its message already says which entry was wrong and
// why, which no local paraphrase can improve on.
func accessFailure(err error, verb string) string {
	var apiErr *api.APIError
	if !errors.As(err, &apiErr) {
		return fmt.Sprintf("Failed to %s: %v", verb, err)
	}
	switch apiErr.Status {
	case http.StatusBadRequest:
		return messageOr(apiErr, "the request was refused as malformed")
	case http.StatusForbidden:
		return "You need the write role on this project to change its external access."
	case http.StatusNotFound:
		return messageOr(apiErr, "not found")
	case http.StatusConflict:
		return messageOr(apiErr, "this external access has been revoked") +
			"\n   A revoked principal stays revoked — add a new one with: ghayma access add"
	case http.StatusServiceUnavailable:
		// The engine, not the request. Said plainly and without a stack: the
		// same command works once the database or bucket answers again.
		return messageOr(apiErr, "the engine could not be reached") +
			"\n   Nothing was changed — run the same command again once it answers."
	}
	return fmt.Sprintf("Failed to %s: %v", verb, err)
}

func messageOr(apiErr *api.APIError, fallback string) string {
	if apiErr.Message == "" {
		return fallback
	}
	return apiErr.Message
}

// parseCIDRList splits a --allow/--set value. Only the shape is local: the
// server canonicalises every entry (a bare address widens to /32 or /128) and
// refuses the rest, so nothing here second-guesses it.
func parseCIDRList(raw string) []string {
	out := []string{}
	for _, entry := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// expiryFromDays turns --expires into the RFC3339 instant the API takes. 0 is
// "no expiry" (the flag was not given); a negative count is a principal revoked
// before it is handed over, which is refused here rather than at the server.
func expiryFromDays(days int, now time.Time) (string, error) {
	if days == 0 {
		return "", nil
	}
	if days < 0 {
		return "", fmt.Errorf("--expires takes a number of days in the future, got %d", days)
	}
	return now.UTC().AddDate(0, 0, days).Format(time.RFC3339), nil
}
