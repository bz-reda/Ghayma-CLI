package api

import (
	"bytes"
	"encoding/json"
)

// Per-DB site access. Each managed database enforces a network policy (per-DB
// CiliumNetworkPolicy) naming which of its owning project's sites may open a
// connection to it. A database reaches only the sites connected to it — chosen
// at create (connect_site_ids), at deploy, or with `ghayma connect database
// <name> --site <slug>`. Backend: Ghayma-backend #202, GET/PUT
// /api/v1/databases/:id/sites.

// DatabaseSiteAccess is one row of the sites response: a project site plus
// whether it may currently reach the database. The response carries EVERY site
// of the database's owning project, each flagged with has_access. Pending marks
// a site whose connection waits for the database to accept connections; it is
// part of the current set, though it cannot reach the database yet.
type DatabaseSiteAccess struct {
	SiteID    string `json:"site_id"`
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	HasAccess bool   `json:"has_access"`
	Pending   bool   `json:"pending,omitempty"`
}

// ListDatabaseSites returns every site of the database's owning project with a
// has_access flag (GET /api/v1/databases/:id/sites → {"sites":[...]}). An
// unlinked database yields an empty slice (the server returns {"sites":[]}). A
// non-200 surfaces the server's {"error":...} message via decodeAPIError.
func (c *Client) ListDatabaseSites(dbID string) ([]DatabaseSiteAccess, error) {
	resp, err := c.authRequest("GET", "/api/v1/databases/"+dbID+"/sites", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, decodeAPIError(resp)
	}

	var out struct {
		Sites []DatabaseSiteAccess `json:"sites"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Sites, nil
}

// SetDatabaseSites replaces the FULL set of sites allowed to reach the database
// (PUT /api/v1/databases/:id/sites, body {"site_ids":[...]}). siteIDs is the
// complete desired set; an empty slice revokes all access (deny-all). A nil
// slice is normalized to [] so the wire body is {"site_ids":[]}, never null (the
// backend reads null differently from an explicit empty set). It returns the
// same shape as ListDatabaseSites; a non-2xx (e.g. 400 unlinked /
// site-not-in-project) surfaces the server's message via decodeAPIError.
func (c *Client) SetDatabaseSites(dbID string, siteIDs []string) ([]DatabaseSiteAccess, error) {
	if siteIDs == nil {
		siteIDs = []string{}
	}
	body, _ := json.Marshal(map[string][]string{"site_ids": siteIDs})
	resp, err := c.authRequest("PUT", "/api/v1/databases/"+dbID+"/sites", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, decodeAPIError(resp)
	}

	var out struct {
		Sites []DatabaseSiteAccess `json:"sites"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Sites, nil
}
