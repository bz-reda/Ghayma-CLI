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
// Level is omitted so the server applies the kind's default level. It is also
// an element of a deploy's connect_resources field and of what it connected.
type ConnectionItem struct {
	Kind       string `json:"kind"`
	ResourceID string `json:"resource_id"`
	Level      string `json:"level,omitempty"`
}

// ConnectChoice is what a create did with the sites chosen for the new
// service: connected, pending until the engine accepts logins (Postgres), or
// failed with the reason. A create returns it as nil when the server sent none
// — one older than 2026-10-02, which may still connect the service on its own
// — and empty when it sent one this CLI cannot read; the resource exists
// either way.
type ConnectChoice struct {
	Connected []string             `json:"connected"`
	Pending   []string             `json:"pending"`
	Failed    []SiteConnectFailure `json:"failed"`
}

// readConnectChoice reads a create's connections on their own, so nothing in
// the resource half costs them: nil when the server sent none, and an empty
// choice, never a half-read one, when it sent one this CLI cannot read.
func readConnectChoice(raw json.RawMessage) *ConnectChoice {
	if len(raw) == 0 {
		return nil
	}
	var choice *ConnectChoice
	if json.Unmarshal(raw, &choice) != nil {
		return &ConnectChoice{}
	}
	return choice
}

// SiteConnectFailure is a chosen site the new service could not be connected
// to; Error is a sentence safe to show the user.
type SiteConnectFailure struct {
	SiteID string `json:"site_id"`
	Error  string `json:"error"`
}

// siteChoice is a create body's connect_site_ids: the chosen sites, or [] for
// none — never null, so every create states its choice.
func siteChoice(siteIDs []string) []string {
	if siteIDs == nil {
		return []string{}
	}
	return siteIDs
}

// DeployConnections is what a deploy did with the services chosen to connect
// to its site, keyed by resource since the site is the deploy's own. Pending
// ones, a database not ready yet, connect once they accept connections; a
// server that predates the field leaves it empty.
type DeployConnections struct {
	Connected []ConnectionItem       `json:"connected"`
	Pending   []ConnectionItem       `json:"pending"`
	Failed    []DeployConnectFailure `json:"failed"`
}

// DeployConnectFailure is a chosen service the deploy could not connect; Error
// is a sentence safe to show the user.
type DeployConnectFailure struct {
	Kind       string `json:"kind"`
	ResourceID string `json:"resource_id"`
	Error      string `json:"error"`
}

// Unconnected is a service of the project that no site holds, with the level
// a connection to it gets by default.
type Unconnected struct {
	Kind         string `json:"kind"`
	ResourceID   string `json:"resource_id"`
	ResourceName string `json:"resource_name"`
	DefaultLevel string `json:"default_level"`
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

// ListUnconnected returns the project's services that no site holds, which a
// deploy offers to connect (GET …/connections/unconnected). Never nil on
// success.
func (c *Client) ListUnconnected(projectID string) ([]Unconnected, error) {
	resp, err := c.authRequest("GET", "/api/v1/projects/"+projectID+"/connections/unconnected", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Unconnected []Unconnected `json:"unconnected"`
	}
	if err := c.decodeJSON(resp, &out); err != nil {
		return nil, err
	}
	if out.Unconnected == nil {
		out.Unconnected = []Unconnected{}
	}
	return out.Unconnected, nil
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
// already connected (POST …/sites/:siteId/connections → 201 with the row). A
// refusal is *APIError with the server's code (database_not_running for a
// Valkey still starting).
func (c *Client) AddSiteConnection(projectID, siteID string, item ConnectionItem) (*Connection, error) {
	body, _ := json.Marshal(item)
	resp, err := c.authRequest("POST", "/api/v1/projects/"+projectID+"/sites/"+siteID+"/connections", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, c.decodeJSON(resp, nil)
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

// CodeNothingToRotate is a rotation refused because the connection (an auth
// app's) has no credential of its own.
const CodeNothingToRotate = "nothing_to_rotate"

// RotateSiteConnection replaces the credential this site holds for one service
// (POST …/connections/:kind/:resourceId/rotate → 204, no body). The server
// answers 404 when nothing is connected, 409 when the connection has no
// credential of its own to rotate yet, and 503 while the engine is unreachable
// or the rotation did not complete. Each comes back as *APIError carrying the
// status, code and the server's message, so the command renders its own
// sentence per case.
func (c *Client) RotateSiteConnection(projectID, siteID, kind, resourceID string) error {
	resp, err := c.authRequest("POST", "/api/v1/projects/"+projectID+"/sites/"+siteID+"/connections/"+kind+"/"+resourceID+"/rotate", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return c.decodeJSON(resp, nil)
}

// GetEffectiveSiteEnv returns the exact environment a pod of the site receives:
// the stored variables plus every connection-derived value (GET …/env/effective
// → {"env":{…}}). Project admin role; the server audits every read. Never nil
// on success. Callers that also want to know where each value came from take
// EffectiveSiteEnv instead (envvars_inherit.go).
func (c *Client) GetEffectiveSiteEnv(projectID, siteID string) (map[string]string, error) {
	out, err := c.EffectiveSiteEnv(projectID, siteID)
	if err != nil {
		return nil, err
	}
	return out.Env, nil
}
