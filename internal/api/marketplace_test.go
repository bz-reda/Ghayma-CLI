package api

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Representative catalog payload — numbers are test inputs, not production
// pricing (the client hardcodes none). Shape mirrors
// paas-api/internal/points/catalog_handler.go MarketplaceCatalog 1:1.
const catalogJSON = `{
  "app_tiers": [
    {"slug":"a","cpu_limit_milli":250,"memory_limit_mb":512,"points_cost":10,"max_image_mb":2048,"ephemeral_gb":1,"position":0}
  ],
  "db_tiers": [
    {"slug":"xs","cpu_limit_milli":250,"memory_limit_mb":512,"points_cost":8,"position":0}
  ],
  "auth_tiers": [
    {"slug":"free","max_users":1000,"points_cost":0,"sms_points":1,"sms_included_monthly":0,"position":0}
  ],
  "backup_tiers": [
    {"slug":"weekly","interval_hours":168,"retention_count":4,"multiplier":0,"position":0}
  ],
  "rates": {"db_block_gb":10,"db_block_points":5,"obj_block_gb":10,"obj_block_points":3,"backup_block_gb":10,"twofa_points":2},
  "plans": [
    {"slug":"hobby","points":100,"max_app_tier":"b","max_db_tier":"s"}
  ]
}`

// TestGetMarketplaceCatalog_Parses pins the 200 round-trip: correct path,
// Bearer header, and every sub-collection + rates + plans parsed 1:1.
func TestGetMarketplaceCatalog_Parses(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/marketplace/catalog" || r.Method != http.MethodGet {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("auth header = %q; want Bearer test-token", got)
		}
		io.WriteString(w, catalogJSON)
	}))
	defer ts.Close()

	cat, err := newTestClient(ts.URL).GetMarketplaceCatalog()
	if err != nil {
		t.Fatalf("GetMarketplaceCatalog: %v", err)
	}
	if len(cat.AppTiers) != 1 || cat.AppTiers[0].Slug != "a" || cat.AppTiers[0].PointsCost != 10 {
		t.Errorf("app tiers = %+v; want one tier a with points_cost 10", cat.AppTiers)
	}
	if cat.AppTiers[0].MaxImageMB != 2048 || cat.AppTiers[0].EphemeralGB != 1 {
		t.Errorf("app tier extra fields wrong: %+v", cat.AppTiers[0])
	}
	if len(cat.DBTiers) != 1 || cat.DBTiers[0].Slug != "xs" || cat.DBTiers[0].MemoryLimitMB != 512 {
		t.Errorf("db tiers = %+v; want one tier xs 512MB", cat.DBTiers)
	}
	if len(cat.AuthTiers) != 1 || cat.AuthTiers[0].MaxUsers != 1000 || cat.AuthTiers[0].SMSPoints != 1 {
		t.Errorf("auth tiers = %+v; want one free tier 1000 users", cat.AuthTiers)
	}
	if len(cat.BackupTiers) != 1 || cat.BackupTiers[0].IntervalHours != 168 || cat.BackupTiers[0].Multiplier != 0 {
		t.Errorf("backup tiers = %+v; want weekly 168h mult 0", cat.BackupTiers)
	}
	if cat.Rates.DBBlockGB != 10 || cat.Rates.DBBlockPoints != 5 || cat.Rates.ObjBlockPoints != 3 || cat.Rates.TwoFAPoints != 2 {
		t.Errorf("rates = %+v; want the six public knobs parsed", cat.Rates)
	}
	if len(cat.Plans) != 1 || cat.Plans[0].Slug != "hobby" || cat.Plans[0].Points != 100 || cat.Plans[0].MaxAppTier != "b" || cat.Plans[0].MaxDBTier != "s" {
		t.Errorf("plans = %+v; want one hobby plan 100pts", cat.Plans)
	}
}

// TestGetMarketplaceCatalog_404_Unavailable pins the older-server fallback:
// a 404 (endpoint not deployed) surfaces the ErrCatalogUnavailable sentinel,
// not a generic HTTP error, so callers can degrade gracefully.
func TestGetMarketplaceCatalog_404_Unavailable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":"404 page not found"}`)
	}))
	defer ts.Close()

	cat, err := newTestClient(ts.URL).GetMarketplaceCatalog()
	if !errors.Is(err, ErrCatalogUnavailable) {
		t.Fatalf("err = %v; want ErrCatalogUnavailable", err)
	}
	if cat != nil {
		t.Errorf("catalog = %+v; want nil on 404", cat)
	}
}

// TestGetProjectPoints_Parses pins the points-summary round-trip: correct
// per-project path, Bearer header, meter figures + itemized breakdown.
func TestGetProjectPoints_Parses(t *testing.T) {
	const pointsJSON = `{
	  "budget":100,"used":42,"remaining":58,"over_by":0,"enforced":true,"payg":false,
	  "breakdown":[
	    {"kind":"app","id":"s1","name":"web","points":20,"detail":"tier a × 1 replicas"},
	    {"kind":"database","id":"d1","name":"pg","points":22,"detail":"tier xs"}
	  ]
	}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/projects/proj-1/points" || r.Method != http.MethodGet {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("auth header = %q; want Bearer test-token", got)
		}
		io.WriteString(w, pointsJSON)
	}))
	defer ts.Close()

	sum, err := newTestClient(ts.URL).GetProjectPoints("proj-1")
	if err != nil {
		t.Fatalf("GetProjectPoints: %v", err)
	}
	if sum.Budget != 100 || sum.Used != 42 || sum.Remaining != 58 || sum.OverBy != 0 {
		t.Errorf("meter = %+v; want budget100 used42 remaining58 over0", sum)
	}
	if !sum.Enforced || sum.PAYG {
		t.Errorf("flags = enforced:%v payg:%v; want enforced:true payg:false", sum.Enforced, sum.PAYG)
	}
	if len(sum.Breakdown) != 2 {
		t.Fatalf("breakdown len = %d; want 2", len(sum.Breakdown))
	}
	if sum.Breakdown[0].Kind != "app" || sum.Breakdown[0].Points != 20 || sum.Breakdown[0].Name != "web" {
		t.Errorf("breakdown[0] = %+v; want app web 20pts", sum.Breakdown[0])
	}
	if sum.Breakdown[1].Kind != "database" || sum.Breakdown[1].ID != "d1" {
		t.Errorf("breakdown[1] = %+v; want database d1", sum.Breakdown[1])
	}
}

// TestClassifyAPIError_Kinds pins the (status, message) → Kind contract with
// the REAL handler bodies (verified against internal/sites/handler.go and
// internal/databases/handler.go). A non-marketplace body must not be typed.
func TestClassifyAPIError_Kinds(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantKind   string // "" means: not a *MarketplaceError (plain error)
		wantMsgHas string
	}{
		{
			name:       "maxtier_db",
			status:     http.StatusConflict,
			body:       `{"error":"requested database tier exceeds the plan's maximum: tier \"l\" (Large) exceeds plan max tier \"s\" (Small)"}`,
			wantKind:   "maxtier",
			wantMsgHas: "exceeds the plan's maximum",
		},
		{
			name:       "maxtier_app",
			status:     http.StatusConflict,
			body:       `{"error":"requested app tier exceeds the plan's maximum: tier \"c\" exceeds plan max app tier \"b\""}`,
			wantKind:   "maxtier",
			wantMsgHas: "exceeds plan max app tier",
		},
		{
			name:       "insufficient_app",
			status:     http.StatusConflict,
			body:       `{"error":"this app size would exceed your plan's points budget; upgrade your plan, free up resources, or switch to pay-as-you-go (PAYG)"}`,
			wantKind:   "insufficient",
			wantMsgHas: "points budget",
		},
		{
			name:       "insufficient_db",
			status:     http.StatusConflict,
			body:       `{"error":"this change would exceed your plan's points budget; upgrade your plan, free up resources, or switch to pay-as-you-go (PAYG)"}`,
			wantKind:   "insufficient",
			wantMsgHas: "points budget",
		},
		{
			name:       "capacity",
			status:     http.StatusServiceUnavailable,
			body:       `{"error":"platform capacity is temporarily exhausted; please try again later"}`,
			wantKind:   "capacity",
			wantMsgHas: "platform capacity",
		},
		{
			name:       "other_409",
			status:     http.StatusConflict,
			body:       `{"error":"a database with that name already exists"}`,
			wantKind:   "other",
			wantMsgHas: "already exists",
		},
		{
			name:       "plain_400",
			status:     http.StatusBadRequest,
			body:       `{"error":"invalid request body"}`,
			wantKind:   "", // not a marketplace class → plain error
			wantMsgHas: "invalid request body",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := classifyAPIError(tt.status, []byte(tt.body))
			if err == nil {
				t.Fatal("classifyAPIError returned nil")
			}
			var me *MarketplaceError
			if errors.As(err, &me) {
				if tt.wantKind == "" {
					t.Fatalf("got *MarketplaceError kind=%q; want a plain error", me.Kind)
				}
				if me.Kind != tt.wantKind {
					t.Errorf("kind = %q; want %q", me.Kind, tt.wantKind)
				}
			} else if tt.wantKind != "" {
				t.Fatalf("got plain error %v; want *MarketplaceError kind=%q", err, tt.wantKind)
			}
			if !contains(err.Error(), tt.wantMsgHas) {
				t.Errorf("message = %q; want it to contain %q", err.Error(), tt.wantMsgHas)
			}
		})
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
