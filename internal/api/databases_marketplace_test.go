package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestClassifyAPIError_MaxTierAnyStatus pins the Task-3 widening: a max-tier
// rejection is detected by the unique substring "exceeds the plan's maximum" on
// ANY status, not just 409. `db create` maps this to HTTP 400 (default branch),
// while `db resize` returns 409 — both must render the maxtier class.
func TestClassifyAPIError_MaxTierAnyStatus(t *testing.T) {
	body := []byte(`{"error":"requested database tier \"l\" (Large) exceeds the plan's maximum \"m\" (Medium)"}`)
	err := classifyAPIError(http.StatusBadRequest, body)
	var me *MarketplaceError
	if !errors.As(err, &me) {
		t.Fatalf("400 max-tier body = %v; want *MarketplaceError", err)
	}
	if me.Kind != "maxtier" {
		t.Errorf("kind = %q; want maxtier", me.Kind)
	}
}

// TestCreateDatabase_SendsPointsFields pins that create forwards the marketplace
// selectors: tier, disk_gb (omitted at 0), backup_tier_slug (omitted when
// blank).
func TestCreateDatabase_SendsPointsFields(t *testing.T) {
	var captured map[string]interface{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/databases" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &captured)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"database":{"id":"d1","name":"pg","type":"postgres","tier_slug":"s","disk_gb":20,"backup_tier_slug":"daily"}}`)
	}))
	defer ts.Close()

	db, err := newTestClient(ts.URL).CreateDatabase("pg", "postgres", "proj-1", nil, "s", 20, "daily")
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if captured["tier"] != "s" {
		t.Errorf("tier sent = %v; want s", captured["tier"])
	}
	if captured["disk_gb"] != float64(20) {
		t.Errorf("disk_gb sent = %v; want 20", captured["disk_gb"])
	}
	if captured["backup_tier_slug"] != "daily" {
		t.Errorf("backup_tier_slug sent = %v; want daily", captured["backup_tier_slug"])
	}
	if db.DiskGB != 20 || db.TierSlug != "s" || db.BackupTierSlug != "daily" {
		t.Errorf("parsed db = %+v; want disk 20, tier s, backup daily", db)
	}
}

// disk_gb == 0 and a blank backup slug must be omitted so the server applies its
// own defaults (ceil(size_mb/1024) disk, weekly backup).
func TestCreateDatabase_OmitsZeroDiskAndBlankBackup(t *testing.T) {
	var captured map[string]interface{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &captured)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"database":{"id":"d1","name":"pg","type":"postgres"}}`)
	}))
	defer ts.Close()

	if _, err := newTestClient(ts.URL).CreateDatabase("pg", "postgres", "proj-1", nil, "", 0, ""); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if _, ok := captured["disk_gb"]; ok {
		t.Errorf("disk_gb must be omitted at 0, got %v", captured["disk_gb"])
	}
	if _, ok := captured["backup_tier_slug"]; ok {
		t.Errorf("backup_tier_slug must be omitted when blank, got %v", captured["backup_tier_slug"])
	}
	if _, ok := captured["tier"]; ok {
		t.Errorf("tier must be omitted when blank, got %v", captured["tier"])
	}
}

// A non-201 create response routes through classifyAPIError so the marketplace
// classes render (here: a 400 max-tier rejection).
func TestCreateDatabase_ClassifiesError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"requested database tier \"l\" exceeds the plan's maximum \"m\""}`)
	}))
	defer ts.Close()

	_, err := newTestClient(ts.URL).CreateDatabase("pg", "postgres", "proj-1", nil, "l", 0, "")
	var me *MarketplaceError
	if !errors.As(err, &me) || me.Kind != "maxtier" {
		t.Fatalf("create error = %v; want *MarketplaceError kind maxtier", err)
	}
}

// TestRetierDatabase_SendsBodyAndParses pins the PATCH /:id/tier round-trip.
func TestRetierDatabase_SendsBodyAndParses(t *testing.T) {
	var captured map[string]interface{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/databases/d1/tier" || r.Method != http.MethodPatch {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &captured)
		io.WriteString(w, `{"database":{"id":"d1","name":"pg","type":"postgres","tier_slug":"m","disk_gb":30,"backup_tier_slug":"daily"}}`)
	}))
	defer ts.Close()

	db, err := newTestClient(ts.URL).RetierDatabase("d1", "m", 30, "daily")
	if err != nil {
		t.Fatalf("RetierDatabase: %v", err)
	}
	if captured["tier"] != "m" || captured["disk_gb"] != float64(30) || captured["backup_tier_slug"] != "daily" {
		t.Errorf("retier body = %v; want tier m, disk 30, backup daily", captured)
	}
	if db.TierSlug != "m" || db.DiskGB != 30 {
		t.Errorf("parsed db = %+v; want tier m disk 30", db)
	}
}

// Omit unset retier fields so a tier-only (or disk-only) change doesn't clobber
// the others.
func TestRetierDatabase_OmitsUnsetFields(t *testing.T) {
	var captured map[string]interface{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &captured)
		io.WriteString(w, `{"database":{"id":"d1","name":"pg","type":"postgres","tier_slug":"m"}}`)
	}))
	defer ts.Close()

	if _, err := newTestClient(ts.URL).RetierDatabase("d1", "m", 0, ""); err != nil {
		t.Fatalf("RetierDatabase: %v", err)
	}
	if captured["tier"] != "m" {
		t.Errorf("tier = %v; want m", captured["tier"])
	}
	if _, ok := captured["disk_gb"]; ok {
		t.Errorf("disk_gb must be omitted at 0, got %v", captured["disk_gb"])
	}
	if _, ok := captured["backup_tier_slug"]; ok {
		t.Errorf("backup_tier_slug must be omitted when blank, got %v", captured["backup_tier_slug"])
	}
}

// A non-200 retier response routes through classifyAPIError — a 409 points-
// budget rejection must surface the insufficient class.
func TestRetierDatabase_ClassifiesError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, `{"error":"this change would exceed your plan's points budget; upgrade your plan, free up resources, or switch to pay-as-you-go (PAYG)"}`)
	}))
	defer ts.Close()

	_, err := newTestClient(ts.URL).RetierDatabase("d1", "l", 0, "")
	var me *MarketplaceError
	if !errors.As(err, &me) || me.Kind != "insufficient" {
		t.Fatalf("retier error = %v; want *MarketplaceError kind insufficient", err)
	}
}
