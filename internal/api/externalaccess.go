package api

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// External access (Ghayma-backend #372, Connections phase 3c). An external
// principal is a NAMED identity outside Ghayma — "Metabase", "partner-x CI" —
// that may reach ONE database or ONE bucket with its OWN engine credential, an
// optional source-CIDR allowlist and an optional expiry. It is a site
// connection minus the site: nothing derives it into an app's variables, and it
// is revoked or rotated alone.
//
// The routes hang off the project scope, one static prefix per kind:
//
//	GET    /api/v1/projects/:id/{databases|buckets}/:resourceId/access
//	POST   …/access                      → 201 {access, credential, warning}
//	DELETE …/access/:accessId            → 204
//	POST   …/access/:accessId/rotate     → 200 {credential, warning}
//	PUT    …/access/:accessId/allowlist  → 200 {access}
//
// `:id` is a project id or slug; `:resourceId` is the resource's UUID, never
// its name. Reads need the project `read` role, every mutation `write`.
//
// Auth apps are deliberately not a kind: an auth app's external access IS its
// restricted project keys, so there is no route here to call for one.

// accessSegment maps a connection kind onto its route segment. An unknown kind
// yields "", which the callers turn into an error rather than a bad path.
func accessSegment(kind string) string {
	switch kind {
	case "database":
		return "databases"
	case "bucket":
		return "buckets"
	}
	return ""
}

func accessPath(projectID, kind, resourceID string) (string, error) {
	segment := accessSegment(kind)
	if segment == "" {
		return "", fmt.Errorf("%q has no external access", kind)
	}
	return "/api/v1/projects/" + projectID + "/" + segment + "/" + resourceID + "/access", nil
}

// ExternalAccess is one principal as the API renders it. It never carries the
// secret: that is returned once, by create and rotate, and this shape has no
// field for it. The timestamps are the server's RFC3339 strings, empty when the
// server omitted them.
type ExternalAccess struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	ResourceID string   `json:"resource_id"`
	Name       string   `json:"name"`
	Level      string   `json:"level"`
	AllowCIDRs []string `json:"allow_cidrs"`
	ExpiresAt  string   `json:"expires_at"`
	CreatedBy  string   `json:"created_by"`
	CreatedAt  string   `json:"created_at"`
	RevokedAt  string   `json:"revoked_at"`
	// LastUsedAt is empty for databases in this release: an external
	// principal's traffic never passes through paas-api, and no log shipper
	// reads the front door yet.
	LastUsedAt string `json:"last_used_at"`
	Active     bool   `json:"active"`
}

// ExternalAccessRequest is the add form. An empty Level takes the kind's
// default, an empty AllowCIDRs means any source, an empty ExpiresAt means no
// expiry — each is omitted from the body so the server applies its own default.
type ExternalAccessRequest struct {
	Name       string   `json:"name"`
	Level      string   `json:"level,omitempty"`
	AllowCIDRs []string `json:"allow_cidrs,omitempty"`
	ExpiresAt  string   `json:"expires_at,omitempty"`
}

// ExternalCredential is the engine credential itself. Ref is the identity (a
// Postgres role, a Mongo user, a Garage access key id) and is safe to show
// again; Secret is the password or S3 secret key and is readable exactly once.
type ExternalCredential struct {
	Ref      string `json:"credential_ref"`
	Secret   string `json:"secret"`
	URI      string `json:"uri"`
	Endpoint string `json:"endpoint"`
	Bucket   string `json:"bucket"`
}

// ExternalAccessSecret is the one-time answer of a create or a rotate. Access
// is set by create only — a rotation moves nothing but the secret.
type ExternalAccessSecret struct {
	Access     *ExternalAccess     `json:"access"`
	Credential *ExternalCredential `json:"credential"`
	Warning    string              `json:"warning"`
}

// ListExternalAccess returns one resource's external principals, revoked ones
// included. Never nil on success. The site rows beside them in a console's
// Access list come from ListConnections: this endpoint is externals only.
func (c *Client) ListExternalAccess(projectID, kind, resourceID string) ([]ExternalAccess, error) {
	path, err := accessPath(projectID, kind, resourceID)
	if err != nil {
		return nil, err
	}
	resp, err := c.authRequest("GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Access []ExternalAccess `json:"access"`
	}
	if err := c.decodeJSON(resp, &out); err != nil {
		return nil, err
	}
	if out.Access == nil {
		out.Access = []ExternalAccess{}
	}
	return out.Access, nil
}

// CreateExternalAccess mints a principal and returns its credential ONCE (201).
// A 400 is a malformed name, level, CIDR or expiry; a 503 is an engine that
// could not mint, in which case NO principal was created.
func (c *Client) CreateExternalAccess(projectID, kind, resourceID string, req ExternalAccessRequest) (*ExternalAccessSecret, error) {
	path, err := accessPath(projectID, kind, resourceID)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(req)
	resp, err := c.authRequest("POST", path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out ExternalAccessSecret
	if err := c.decodeJSON(resp, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RevokeExternalAccess drops the principal's credential and closes its row
// (204). Revoking twice is not an error, so a retry after a lost response is
// safe.
func (c *Client) RevokeExternalAccess(projectID, kind, resourceID, accessID string) error {
	path, err := accessPath(projectID, kind, resourceID)
	if err != nil {
		return err
	}
	resp, err := c.authRequest("DELETE", path+"/"+accessID, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return c.decodeJSON(resp, nil)
}

// RotateExternalAccess gives the principal a new secret on the same identity
// and returns it once. The name, level and allowlist are untouched. A 409 is a
// principal that has already been revoked; a 503 is an unreachable engine, and
// the old secret then still works.
func (c *Client) RotateExternalAccess(projectID, kind, resourceID, accessID string) (*ExternalAccessSecret, error) {
	path, err := accessPath(projectID, kind, resourceID)
	if err != nil {
		return nil, err
	}
	resp, err := c.authRequest("POST", path+"/"+accessID+"/rotate", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out ExternalAccessSecret
	if err := c.decodeJSON(resp, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetExternalAccessAllowlist replaces the principal's source CIDRs with the
// whole list given. An empty list clears it, which means any source — the
// credential is then the only gate. The body always carries the field, so a
// clear is a `[]` and never an omitted key.
func (c *Client) SetExternalAccessAllowlist(projectID, kind, resourceID, accessID string, cidrs []string) (*ExternalAccess, error) {
	path, err := accessPath(projectID, kind, resourceID)
	if err != nil {
		return nil, err
	}
	if cidrs == nil {
		cidrs = []string{}
	}
	body, _ := json.Marshal(map[string][]string{"allow_cidrs": cidrs})
	resp, err := c.authRequest("PUT", path+"/"+accessID+"/allowlist", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Access ExternalAccess `json:"access"`
	}
	if err := c.decodeJSON(resp, &out); err != nil {
		return nil, err
	}
	return &out.Access, nil
}
