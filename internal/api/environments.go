package api

import (
	"bytes"
	"encoding/json"
)

// Environments (ENVIRONMENTS-DESIGN-2026-09-17). A site carries a KIND —
// production | staging | development — and that kind, not a flag on the
// deploy, decides whether a deployment is a production one. Promotion ships
// the exact image one site already runs onto another site.
//
// Backend: Ghayma-backend #366 (kind + derived is_production), #367 (promote),
// #368 (inherit_env toggle).

// The environment kinds, mirroring internal/sites.Environment*.
const (
	EnvironmentProduction  = "production"
	EnvironmentStaging     = "staging"
	EnvironmentDevelopment = "development"
)

// Machine codes the environment routes answer their refusals with. Branching
// on these rather than on the prose keeps the CLI's wording stable when the
// server's improves. The environment lock ships as `default_site_
// environment_locked`; the shorter form is accepted too so a rename on the
// server cannot silently turn the refusal into a generic error.
const (
	CodeDefaultSiteEnvironmentLocked = "default_site_environment_locked"
	CodeDefaultSiteLocked            = "default_site_locked"
	CodeDefaultSiteCannotInherit     = "default_site_cannot_inherit"
	CodeInvalidEnvironment           = "invalid_environment"
)

// SetSiteEnvironment re-kinds one secondary site
// (PUT /projects/:id/sites/:siteId/environment). The default site is refused
// with 409 CodeDefaultSiteEnvironmentLocked — it is always production.
func (c *Client) SetSiteEnvironment(projectID, siteID, environment string) (*Site, error) {
	body, _ := json.Marshal(map[string]string{"environment": environment})
	resp, err := c.authRequest("PUT", "/api/v1/projects/"+projectID+"/sites/"+siteID+"/environment", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var site Site
	if err := c.decodeJSON(resp, &site); err != nil {
		return nil, err
	}
	return &site, nil
}

// SetSiteInheritEnv turns the env var ladder on or off for one secondary site
// (PUT /projects/:id/sites/:siteId/inherit-env). The default site is refused
// with 409 CodeDefaultSiteCannotInherit — it IS the ladder's base.
func (c *Client) SetSiteInheritEnv(projectID, siteID string, inherit bool) (*Site, error) {
	body, _ := json.Marshal(map[string]bool{"inherit_env": inherit})
	resp, err := c.authRequest("PUT", "/api/v1/projects/"+projectID+"/sites/"+siteID+"/inherit-env", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var site Site
	if err := c.decodeJSON(resp, &site); err != nil {
		return nil, err
	}
	return &site, nil
}

// Promotion is the row POST …/promote answers with. It is an ordinary
// deployment — polled with GetDeployment like any other — plus the provenance
// of the image it ships.
type Promotion struct {
	ID                 string `json:"id"`
	SiteID             string `json:"site_id"`
	Status             string `json:"status"`
	Trigger            string `json:"trigger"`
	SourceImageRef     string `json:"source_image_ref"`
	SourceImageDigest  string `json:"source_image_digest"`
	SourceSite         string `json:"source_site"`
	SourceDeploymentID string `json:"source_deployment_id"`
}

// promoteRequest names what to promote. An empty DeploymentID means the source
// site's current live deployment.
type promoteRequest struct {
	FromSite      string `json:"from_site"`
	DeploymentID  string `json:"deployment_id,omitempty"`
	CommitMessage string `json:"commit_message,omitempty"`
}

// PromoteSite ships the image a source site already runs onto the target site
// (POST /projects/:id/sites/:siteId/promote). Nothing is rebuilt: the source
// deployment's digest is mounted into the target repository, re-admitted
// against the target's tier and rolled out there with the target's own env,
// connections, domains and tier.
//
// Refusals come back as *APIError with the server's own wording: 400 for a
// self-promotion or a missing source, 404 for an unknown source site or
// deployment, 409 when the source has no live deployment, 403 when the caller's
// write is restricted to other sites.
func (c *Client) PromoteSite(projectID, targetSiteID, fromSite, deploymentID, commitMessage string) (*Promotion, error) {
	body, _ := json.Marshal(promoteRequest{FromSite: fromSite, DeploymentID: deploymentID, CommitMessage: commitMessage})
	resp, err := c.authRequest("POST", "/api/v1/projects/"+projectID+"/sites/"+targetSiteID+"/promote", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out Promotion
	if err := c.decodeJSON(resp, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
