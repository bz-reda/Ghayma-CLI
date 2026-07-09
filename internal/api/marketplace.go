package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Points-allowance marketplace client surface. The structs mirror the backend
// response shapes 1:1 (json tags must stay identical) —
// paas-api/internal/points/catalog_handler.go (MarketplaceCatalog) and
// paas-api/internal/points/summary.go (ProjectPointsSummary). No pricing
// numbers live here; the values are read from the API at runtime.

// CatalogAppTier is one app-tier row of the public catalog.
type CatalogAppTier struct {
	Slug          string `json:"slug"`
	CPULimitMilli int    `json:"cpu_limit_milli"`
	MemoryLimitMB int    `json:"memory_limit_mb"`
	PointsCost    int    `json:"points_cost"`
	MaxImageMB    int    `json:"max_image_mb"`
	EphemeralGB   int    `json:"ephemeral_gb"`
	Position      int    `json:"position"`
}

// CatalogDBTier is one db-tier row (Postgres *_LIMIT shape).
type CatalogDBTier struct {
	Slug          string `json:"slug"`
	CPULimitMilli int    `json:"cpu_limit_milli"`
	MemoryLimitMB int    `json:"memory_limit_mb"`
	PointsCost    int    `json:"points_cost"`
	Position      int    `json:"position"`
}

// CatalogAuthTier is one auth-tier row.
type CatalogAuthTier struct {
	Slug               string `json:"slug"`
	MaxUsers           int64  `json:"max_users"`
	PointsCost         int    `json:"points_cost"`
	SMSPoints          int    `json:"sms_points"`
	SMSIncludedMonthly int    `json:"sms_included_monthly"`
	Position           int    `json:"position"`
}

// CatalogBackupTier is one backup-tier row.
type CatalogBackupTier struct {
	Slug           string `json:"slug"`
	IntervalHours  int    `json:"interval_hours"`
	RetentionCount int    `json:"retention_count"`
	Multiplier     int    `json:"multiplier"`
	Position       int    `json:"position"`
}

// CatalogRates is the public rate-knob subset (six knobs — the backend never
// exposes the three internal ones).
type CatalogRates struct {
	DBBlockGB      int64 `json:"db_block_gb"`
	DBBlockPoints  int64 `json:"db_block_points"`
	ObjBlockGB     int64 `json:"obj_block_gb"`
	ObjBlockPoints int64 `json:"obj_block_points"`
	BackupBlockGB  int64 `json:"backup_block_gb"`
	TwoFAPoints    int64 `json:"twofa_points"`
}

// CatalogPlan is the public marketplace subset of a plan: its points budget +
// the two max-tier guardrails.
type CatalogPlan struct {
	Slug       string `json:"slug"`
	Points     int    `json:"points"`
	MaxAppTier string `json:"max_app_tier"`
	MaxDBTier  string `json:"max_db_tier"`
}

// MarketplaceCatalog is the whole GET /api/v1/marketplace/catalog response.
type MarketplaceCatalog struct {
	AppTiers    []CatalogAppTier    `json:"app_tiers"`
	DBTiers     []CatalogDBTier     `json:"db_tiers"`
	AuthTiers   []CatalogAuthTier   `json:"auth_tiers"`
	BackupTiers []CatalogBackupTier `json:"backup_tiers"`
	Rates       CatalogRates        `json:"rates"`
	Plans       []CatalogPlan       `json:"plans"`
}

// ResourceCost is one priced resource in a project's points breakdown.
type ResourceCost struct {
	Kind   string `json:"kind"` // app | database | storage | auth
	ID     string `json:"id"`
	Name   string `json:"name"`
	Points int64  `json:"points"`
	Detail string `json:"detail"`
}

// ProjectPointsSummary is the meter + itemized breakdown for one project
// (GET /api/v1/projects/:id/points). For PAYG projects Budget is 0, Enforced
// is false and PAYG is true.
type ProjectPointsSummary struct {
	Budget    int64          `json:"budget"`
	Used      int64          `json:"used"`
	Remaining int64          `json:"remaining"`
	OverBy    int64          `json:"over_by"`
	Enforced  bool           `json:"enforced"`
	PAYG      bool           `json:"payg"`
	Breakdown []ResourceCost `json:"breakdown"`
}

// ErrCatalogUnavailable is returned by GetMarketplaceCatalog when the server
// answers 404 — an older backend deployed before the catalog endpoint existed.
// Callers can degrade (skip the buy UI) rather than surface a hard error.
var ErrCatalogUnavailable = errors.New("marketplace catalog unavailable (server does not support the catalog endpoint)")

// GetMarketplaceCatalog fetches the read-only marketplace catalog. Returns
// ErrCatalogUnavailable on 404 so callers can distinguish "endpoint missing"
// from a real failure.
func (c *Client) GetMarketplaceCatalog() (*MarketplaceCatalog, error) {
	resp, err := c.authRequest("GET", "/api/v1/marketplace/catalog", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrCatalogUnavailable
	}
	if resp.StatusCode != http.StatusOK {
		return nil, decodeAPIError(resp)
	}

	var out MarketplaceCatalog
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetProjectPoints fetches the points meter + breakdown for one project.
func (c *Client) GetProjectPoints(projectID string) (*ProjectPointsSummary, error) {
	resp, err := c.authRequest("GET", "/api/v1/projects/"+projectID+"/points", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, decodeAPIError(resp)
	}

	var out ProjectPointsSummary
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// MarketplaceError is a typed classification of a marketplace admission
// rejection. Kind is one of "insufficient" (over the plan's points budget),
// "capacity" (platform capacity exhausted), "maxtier" (tier above the plan's
// guardrail), or "other" (any other 409/503). Message is the server's
// human-readable text, safe to show the user. Later mutation commands
// type-assert *MarketplaceError to render the right guidance.
type MarketplaceError struct {
	Kind    string
	Message string
}

func (e *MarketplaceError) Error() string { return e.Message }

// classifyAPIError maps a (status, body) pair to a typed error. A 409/503 that
// matches a known marketplace class becomes a *MarketplaceError with the
// matching Kind; any other 409/503 becomes a *MarketplaceError with Kind
// "other" (so callers can still show Message); everything else falls back to a
// plain error carrying the server message.
//
// Classification is by (status, message substring), verified against the live
// handlers (internal/sites/handler.go, internal/databases/handler.go):
//   - 409 + "exceeds the plan's maximum" → maxtier
//   - 409 + "points budget"              → insufficient
//   - 503 + "platform capacity"          → capacity
func classifyAPIError(status int, body []byte) error {
	msg := errorMessageFromBody(body)
	switch status {
	case http.StatusConflict: // 409
		switch {
		case strings.Contains(msg, "exceeds the plan's maximum"):
			return &MarketplaceError{Kind: "maxtier", Message: msg}
		case strings.Contains(msg, "points budget"):
			return &MarketplaceError{Kind: "insufficient", Message: msg}
		default:
			return &MarketplaceError{Kind: "other", Message: msg}
		}
	case http.StatusServiceUnavailable: // 503
		if strings.Contains(msg, "platform capacity") {
			return &MarketplaceError{Kind: "capacity", Message: msg}
		}
		return &MarketplaceError{Kind: "other", Message: msg}
	default:
		return fmt.Errorf("%s", msg)
	}
}

// errorMessageFromBody pulls the human message out of a {"error":"..."} body,
// falling back to the raw body when it isn't that shape (decodeAPIError-style).
func errorMessageFromBody(body []byte) string {
	var errResp struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
		return errResp.Error
	}
	return string(body)
}
