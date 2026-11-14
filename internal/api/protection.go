package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"time"
)

// Site protection (SITE-PROTECTION-DESIGN-2026-11-14, Ghayma-backend #520).
// A staging or development site can be made private: visitors sign in with an
// allowed email, automation sends a bypass token.

// Machine codes the protection routes answer with. An older backend answers
// 404 with no code at all.
const (
	CodeProtectionUnavailable = "site_protection_unavailable"
	CodeRouteApplyFailed      = "route_apply_failed"
	CodeInvalidEmail          = "invalid_email"
	CodeEmailExists           = "email_exists"
	CodeEmailLimitReached     = "email_limit_reached"
	CodeInvalidTokenName      = "invalid_name"
	CodeTokenLimitReached     = "token_limit_reached"
	CodeNotFound              = "not_found"
	CodeForbidden             = "forbidden"

	BypassHeader     = "X-Ghayma-Protection-Bypass"
	BypassQueryParam = "x-ghayma-protection-bypass"
)

type ProtectionEmail struct {
	ID         string    `json:"id"`
	Email      string    `json:"email"`
	HasAccount bool      `json:"has_account"`
	CreatedAt  time.Time `json:"created_at"`
}

type ProtectionToken struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Hint      string    `json:"hint"`
	CreatedAt time.Time `json:"created_at"`
}

type ProtectionLimits struct {
	Emails int `json:"emails"`
	Tokens int `json:"tokens"`
}

// Protection is the view every protection route answers with. Effective is
// whether the site is gated right now; a production site is public whatever
// Enabled says. Emails and Tokens are nil only if the server omitted them.
type Protection struct {
	Available   bool              `json:"available"`
	Enabled     bool              `json:"enabled"`
	Effective   bool              `json:"effective"`
	Environment string            `json:"environment"`
	Emails      []ProtectionEmail `json:"emails"`
	Tokens      []ProtectionToken `json:"tokens"`
	Limits      ProtectionLimits  `json:"limits"`
}

// CreatedBypassToken carries the only copy of the secret the server ever
// sends.
type CreatedBypassToken struct {
	Token string      `json:"token"`
	View  *Protection `json:"view"` // nil when the server could not read it back
}

func protectionPath(projectID, siteID string) string {
	return "/api/v1/projects/" + projectID + "/sites/" + siteID + "/protection"
}

func (c *Client) protectionCall(method, path string, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		raw, _ := json.Marshal(payload)
		body = bytes.NewReader(raw)
	}
	resp, err := c.authRequest(method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return c.decodeJSON(resp, out)
}

func (c *Client) protectionView(method, path string, payload any) (*Protection, error) {
	var p Protection
	if err := c.protectionCall(method, path, payload, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (c *Client) GetProtection(projectID, siteID string) (*Protection, error) {
	return c.protectionView("GET", protectionPath(projectID, siteID), nil)
}

// SetProtection turns protection on or off. A 502 CodeRouteApplyFailed when
// enabling means the setting was saved and the route is retried server-side.
func (c *Client) SetProtection(projectID, siteID string, enabled bool) (*Protection, error) {
	return c.protectionView("PUT", protectionPath(projectID, siteID), map[string]bool{"enabled": enabled})
}

func (c *Client) AddProtectionEmail(projectID, siteID, email string) (*Protection, error) {
	return c.protectionView("POST", protectionPath(projectID, siteID)+"/emails", map[string]string{"email": email})
}

func (c *Client) RemoveProtectionEmail(projectID, siteID, emailID string) (*Protection, error) {
	return c.protectionView("DELETE", protectionPath(projectID, siteID)+"/emails/"+url.PathEscape(emailID), nil)
}

func (c *Client) CreateBypassToken(projectID, siteID, name string) (*CreatedBypassToken, error) {
	var out CreatedBypassToken
	if err := c.protectionCall("POST", protectionPath(projectID, siteID)+"/tokens", map[string]string{"name": name}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) RevokeBypassToken(projectID, siteID, tokenID string) (*Protection, error) {
	return c.protectionView("DELETE", protectionPath(projectID, siteID)+"/tokens/"+url.PathEscape(tokenID), nil)
}
