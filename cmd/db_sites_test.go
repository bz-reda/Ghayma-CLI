package cmd

import (
	"slices"
	"strings"
	"testing"

	"paas-cli/internal/api"
)

// sitesFixture is a project with three sites; only main is granted initially.
// The numbers/ids are test inputs — the command reads every value from the GET
// response at runtime.
func sitesFixture() []api.DatabaseSiteAccess {
	return []api.DatabaseSiteAccess{
		{SiteID: "id-main", Slug: "main", Name: "main", HasAccess: true},
		{SiteID: "id-admin", Slug: "admin", Name: "Admin", HasAccess: false},
		{SiteID: "id-api", Slug: "api", Name: "API", HasAccess: false},
	}
}

// --set replaces the whole set with exactly the named sites, emitted in project
// order (deterministic PUT body), and reports no per-slug no-ops.
func TestComputeDesiredSites_Set(t *testing.T) {
	desired, noops, err := computeDesiredSites(sitesFixture(), "set", []string{"admin", "api"})
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if len(noops) != 0 {
		t.Errorf("set noops = %v; want none", noops)
	}
	if !sameStringSet(desired, []string{"id-admin", "id-api"}) {
		t.Errorf("set desired = %v; want id-admin,id-api (main dropped)", desired)
	}
	// Project order: admin (position 1) before api (position 2).
	if len(desired) != 2 || desired[0] != "id-admin" || desired[1] != "id-api" {
		t.Errorf("set order = %v; want [id-admin id-api] in project order", desired)
	}
}

// --set with no slugs is rejected: deny-all is expressed via --remove, not an
// empty --set (documented in the command help).
func TestComputeDesiredSites_SetEmptyErrors(t *testing.T) {
	if _, _, err := computeDesiredSites(sitesFixture(), "set", nil); err == nil {
		t.Error("--set with no slugs must error (deny-all goes through --remove)")
	}
}

// --add grants the named site(s) while keeping the currently-granted ones.
func TestComputeDesiredSites_Add(t *testing.T) {
	desired, noops, err := computeDesiredSites(sitesFixture(), "add", []string{"admin"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(noops) != 0 {
		t.Errorf("add noops = %v; want none", noops)
	}
	if !sameStringSet(desired, []string{"id-main", "id-admin"}) {
		t.Errorf("add desired = %v; want main+admin", desired)
	}
}

// Adding an already-granted site is a no-op, not an error: it is reported and
// the desired set is left unchanged.
func TestComputeDesiredSites_AddAlreadyGrantedIsNoop(t *testing.T) {
	desired, noops, err := computeDesiredSites(sitesFixture(), "add", []string{"main"})
	if err != nil {
		t.Fatalf("add main: %v", err)
	}
	if len(noops) != 1 || noops[0] != "main" {
		t.Errorf("noops = %v; want [main]", noops)
	}
	if !sameStringSet(desired, []string{"id-main"}) {
		t.Errorf("desired = %v; want unchanged main-only", desired)
	}
}

// --remove revokes the named site(s), keeping the rest.
func TestComputeDesiredSites_Remove(t *testing.T) {
	current := sitesFixture()
	current[1].HasAccess = true // admin also granted
	desired, noops, err := computeDesiredSites(current, "remove", []string{"admin"})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(noops) != 0 {
		t.Errorf("remove noops = %v; want none", noops)
	}
	if !sameStringSet(desired, []string{"id-main"}) {
		t.Errorf("remove desired = %v; want main-only", desired)
	}
}

// Removing an ungranted site is a no-op, not an error.
func TestComputeDesiredSites_RemoveUngrantedIsNoop(t *testing.T) {
	desired, noops, err := computeDesiredSites(sitesFixture(), "remove", []string{"admin"})
	if err != nil {
		t.Fatalf("remove admin: %v", err)
	}
	if len(noops) != 1 || noops[0] != "admin" {
		t.Errorf("noops = %v; want [admin]", noops)
	}
	if !sameStringSet(desired, []string{"id-main"}) {
		t.Errorf("desired = %v; want unchanged main-only", desired)
	}
}

// Removing every granted site yields an empty desired set = deny-all.
func TestComputeDesiredSites_RemoveAllDenyAll(t *testing.T) {
	desired, _, err := computeDesiredSites(sitesFixture(), "remove", []string{"main"})
	if err != nil {
		t.Fatalf("remove main: %v", err)
	}
	if len(desired) != 0 {
		t.Errorf("desired = %v; want empty (deny-all)", desired)
	}
}

// An unknown slug errors, naming the bad slug and the valid slugs.
func TestComputeDesiredSites_UnknownSlugErrors(t *testing.T) {
	_, _, err := computeDesiredSites(sitesFixture(), "add", []string{"nope"})
	if err == nil {
		t.Fatal("unknown slug must error")
	}
	for _, want := range []string{"nope", "main", "admin", "api"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q (bad slug + valid slugs)", err.Error(), want)
		}
	}
}

// splitSlugs splits a comma list into trimmed, non-empty slugs.
func TestSplitSlugs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"main", []string{"main"}},
		{"main,admin", []string{"main", "admin"}},
		{" main , admin ", []string{"main", "admin"}},
		{"main,,admin,", []string{"main", "admin"}},
	}
	for _, c := range cases {
		if got := splitSlugs(c.in); !slices.Equal(got, c.want) {
			t.Errorf("splitSlugs(%q) = %v; want %v", c.in, got, c.want)
		}
	}
}

// grantedIDs returns the site IDs whose has_access flag is set.
func TestGrantedIDs(t *testing.T) {
	current := sitesFixture()
	current[2].HasAccess = true // api granted too
	if got := grantedIDs(current); !sameStringSet(got, []string{"id-main", "id-api"}) {
		t.Errorf("grantedIDs = %v; want main+api", got)
	}
}

// sameStringSet is order-insensitive set equality used to detect a no-op PUT.
func TestSameStringSet(t *testing.T) {
	if !sameStringSet([]string{"a", "b"}, []string{"b", "a"}) {
		t.Error("order must not matter")
	}
	if sameStringSet([]string{"a"}, []string{"a", "b"}) {
		t.Error("different sizes are not equal")
	}
	if sameStringSet([]string{"a"}, []string{"b"}) {
		t.Error("different members are not equal")
	}
	if !sameStringSet(nil, nil) {
		t.Error("two empties are equal")
	}
}
