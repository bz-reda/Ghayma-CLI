package api

import (
	"bytes"
	"encoding/json"
	"net/url"
)

// Connections (Ghayma-backend, 2026-09-08). A connection says one SITE may use
// one SERVICE at one LEVEL; it is the single record behind the variables a
// site's pods receive, the per-database network policy and the site's managed
// platform key. The routes live on the project scope (`:id` is a project id or
// slug); reads need the project `read` role, mutations `write`.

// Connection is one row of every connections response. ResourceName is a
// database's or bucket's name and an auth app's app_id. EnvNames are the
// variables the connection puts into the app, in the server's order; it is
// empty while the service is still provisioning and on backends older than
// 2026-09-16, which omit the field.
type Connection struct {
	SiteID       string   `json:"site_id"`
	SiteSlug     string   `json:"site_slug"`
	Kind         string   `json:"kind"`
	ResourceID   string   `json:"resource_id"`
	ResourceName string   `json:"resource_name"`
	Level        string   `json:"level"`
	CreatedAt    string   `json:"created_at"`
	EnvNames     []string `json:"env_names"`
}

// AvailableConnection is a project resource the site is NOT connected to, with
// the levels it accepts and the variables connecting it would inject — the
// "connect something" half of a site's view.
type AvailableConnection struct {
	Kind         string   `json:"kind"`
	ResourceID   string   `json:"resource_id"`
	ResourceName string   `json:"resource_name"`
	Levels       []string `json:"levels"`
	EnvNames     []string `json:"env_names"`
}

// SiteConnections is one site's view: what it holds and what it could add.
type SiteConnections struct {
	Connections []Connection          `json:"connections"`
	Available   []AvailableConnection `json:"available"`
}

// ConnectionItem is the request shape for connecting one resource. An empty
// Level is omitted so the server applies the kind's weakest level.
type ConnectionItem struct {
	Kind       string `json:"kind"`
	ResourceID string `json:"resource_id"`
	Level      string `json:"level,omitempty"`
}

// ListConnections returns the project's connections, narrowed to one site when
// siteID is set (GET /api/v1/projects/:id/connections[?site_id=]). Never nil on
// success.
func (c *Client) ListConnections(projectID, siteID string) ([]Connection, error) {
	path := "/api/v1/projects/" + projectID + "/connections"
	if siteID != "" {
		path += "?site_id=" + url.QueryEscape(siteID)
	}
	resp, err := c.authRequest("GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, decodeAPIError(resp)
	}
	var out struct {
		Connections []Connection `json:"connections"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Connections == nil {
		out.Connections = []Connection{}
	}
	return out.Connections, nil
}

// GetSiteConnections returns one site's connections plus the project resources
// it could still be connected to (GET …/sites/:siteId/connections).
func (c *Client) GetSiteConnections(projectID, siteID string) (*SiteConnections, error) {
	resp, err := c.authRequest("GET", "/api/v1/projects/"+projectID+"/sites/"+siteID+"/connections", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, decodeAPIError(resp)
	}
	var out SiteConnections
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AddSiteConnection connects one resource, or changes its level when it is
// already connected (POST …/sites/:siteId/connections → 201 with the row).
func (c *Client) AddSiteConnection(projectID, siteID string, item ConnectionItem) (*Connection, error) {
	body, _ := json.Marshal(item)
	resp, err := c.authRequest("POST", "/api/v1/projects/"+projectID+"/sites/"+siteID+"/connections", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, decodeAPIError(resp)
	}
	var row Connection
	if err := json.NewDecoder(resp.Body).Decode(&row); err != nil {
		return nil, err
	}
	return &row, nil
}

// RemoveSiteConnection disconnects one resource (DELETE …/connections/:kind/
// :resourceId). The server answers 200 either way; removed says whether a row
// was actually there, so a retry after a lost response is not an error.
func (c *Client) RemoveSiteConnection(projectID, siteID, kind, resourceID string) (bool, error) {
	resp, err := c.authRequest("DELETE", "/api/v1/projects/"+projectID+"/sites/"+siteID+"/connections/"+kind+"/"+resourceID, nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false, decodeAPIError(resp)
	}
	var out struct {
		Removed bool `json:"removed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, err
	}
	return out.Removed, nil
}

// GetEffectiveSiteEnv returns the exact environment a pod of the site receives:
// the stored variables plus every connection-derived value (GET …/env/effective
// → {"env":{…}}). Project admin role; the server audits every read. Never nil
// on success.
func (c *Client) GetEffectiveSiteEnv(projectID, siteID string) (map[string]string, error) {
	resp, err := c.authRequest("GET", "/api/v1/projects/"+projectID+"/sites/"+siteID+"/env/effective", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, decodeAPIError(resp)
	}
	var out struct {
		Env map[string]string `json:"env"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Env == nil {
		out.Env = map[string]string{}
	}
	return out.Env, nil
}
