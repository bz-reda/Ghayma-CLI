package cmd

import (
	"strings"
	"testing"
)

// `db create` help advertises the engines and tiers a user can actually pick.
// Both lists had gone stale against the server, which is worse than saying
// nothing: the flag is passed through untouched, so a user who believes the
// help gets a server-side rejection instead of a CLI-side one.

// Managed Redis was withdrawn as a product on 2026-07-27 and the backend hard
// rejects it at create. Offering it in --type help sends users down a path that
// cannot succeed.
func TestDBCreateTypeHelp_NoRetiredEngines(t *testing.T) {
	f := dbCreateCmd.Flags().Lookup("type")
	if f == nil {
		t.Fatal("db create must keep --type")
	}
	if strings.Contains(strings.ToLower(f.Usage), "redis") {
		t.Errorf("--type help still offers redis, withdrawn 2026-07-27 and rejected by the API: %q", f.Usage)
	}
	for _, engine := range []string{"postgres", "mongodb"} {
		if !strings.Contains(f.Usage, engine) {
			t.Errorf("--type help must still list %q; it is creatable today: %q", engine, f.Usage)
		}
	}
}

// The tier ladder gained `xl` (30 pts, 4 CPU / 4 GB) via the admin tier CRUD.
// The help enumerated xs/s/m/l only, hiding the top of the ladder from anyone
// who does not open the interactive picker.
func TestDBCreateTierHelp_ListsFullLadder(t *testing.T) {
	f := dbCreateCmd.Flags().Lookup("tier")
	if f == nil {
		t.Fatal("db create must keep --tier")
	}
	if !strings.Contains(f.Usage, "xl") {
		t.Errorf("--tier help omits the xl tier, which is live and selectable: %q", f.Usage)
	}
}
