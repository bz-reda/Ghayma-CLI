package api

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Docker image deploys (DOCKER-DEPLOY-DESIGN-2026-09-17 §§2-3). A customer
// builds an image locally, pushes it straight to the Ghayma registry
// front-door (registry.ghayma.tech) and then deploys what was pushed. These
// are the two calls behind `ghayma docker push` and `ghayma deploy --image`.

// RegistryCredential is one short-lived push credential: the password half is
// a ghr_ token scoped to ONE repository and expiring in about half an hour,
// which is also how it is revoked. It is fed to `docker login` on stdin and
// never printed, logged or written to a file by the CLI.
type RegistryCredential struct {
	// Username is informational — the authorizer identifies the caller from
	// the token alone — but it is what the user's credential store shows.
	Username string `json:"username"`
	Token    string `json:"token"`
	// Repo is the repository name in the registry, computed server-side from
	// the (project, site) pair. The CLI never derives it: the composite-slug
	// rule lives in one place, and that place is the backend.
	Repo      string `json:"repo"`
	Server    string `json:"server"`
	ExpiresAt string `json:"expires_at"`
}

// mintRequest names what the credential is for. An omitted site goes through
// the server's shared site ladder; a project with no site row yet (lazy
// main-site creation) is authorized for its own main repository, which is what
// makes push-before-first-deploy work.
type mintRequest struct {
	Project string `json:"project"`
	Site    string `json:"site,omitempty"`
}

// MintRegistryToken mints the credential `docker login` uses
// (POST /api/v1/registry/token). Project write role; the server answers 404
// for an unknown project or site and 409 when the site reference is ambiguous,
// each as *APIError carrying its message.
func (c *Client) MintRegistryToken(project, site string) (*RegistryCredential, error) {
	body, _ := json.Marshal(mintRequest{Project: project, Site: strings.TrimSpace(site)})
	resp, err := c.authRequest("POST", "/api/v1/registry/token", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out RegistryCredential
	if err := c.decodeJSON(resp, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ImageDeployment is the row POST …/deployments/image answers with. The id key
// is `id`, not the upload endpoint's `deployment_id` — the two deploy entries
// differ there, and the polling loop takes whichever it is given.
type ImageDeployment struct {
	ID             string `json:"id"`
	SiteID         string `json:"site_id"`
	Status         string `json:"status"`
	Trigger        string `json:"trigger"`
	SourceImageRef string `json:"source_image_ref"`
}

// imageDeployRequest names the image to deploy. Exactly one of Tag and Digest
// is sent: the endpoint refuses both, because "deploy v1" and "deploy this
// exact digest" are different intents.
type imageDeployRequest struct {
	Tag           string `json:"tag,omitempty"`
	Digest        string `json:"digest,omitempty"`
	CommitMessage string `json:"commit_message,omitempty"`
	IsProduction  bool   `json:"is_production,omitempty"`
}

// DigestPrefix marks a reference that names an exact image rather than a tag.
const DigestPrefix = "sha256:"

// newImageDeployRequest routes one user-supplied reference into the right
// field. Pure, so which field a given reference lands in is a unit test rather
// than an E2E surprise.
func newImageDeployRequest(ref, commitMessage string, isProduction bool) imageDeployRequest {
	req := imageDeployRequest{CommitMessage: commitMessage, IsProduction: isProduction}
	if ref = strings.TrimSpace(ref); strings.HasPrefix(ref, DigestPrefix) {
		req.Digest = ref
		return req
	}
	req.Tag = ref
	return req
}

// DeployImage deploys an already-pushed image
// (POST /api/v1/projects/:id/sites/:siteId/deployments/image). It creates an
// ordinary deployment: admission (architecture, size, non-root user, digest
// pin) runs in the worker, so a refused image is a FAILED deployment whose
// build logs say why — poll it exactly like a source deploy.
func (c *Client) DeployImage(projectID, siteID, ref, commitMessage string, isProduction bool) (*ImageDeployment, error) {
	body, _ := json.Marshal(newImageDeployRequest(ref, commitMessage, isProduction))
	resp, err := c.authRequest("POST", "/api/v1/projects/"+projectID+"/sites/"+siteID+"/deployments/image", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out ImageDeployment
	if err := c.decodeJSON(resp, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
