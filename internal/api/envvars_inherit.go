package api

import (
	"encoding/json"
	"net/url"
)

// Env var inheritance (Environments design §3, backend #368). A site whose
// `inherit_env` flag is on resolves its environment as the project's DEFAULT
// site's rows, overridden key by key by its own.
//
// The listing keeps its old fields — they are the site's OWN rows, which is
// exactly what the replace-all PUT takes — and ADDS the resolved ladder, so a
// read-modify-write client can never PUT inherited rows back as overrides.

// Where a resolved variable came from. `derived` appears only in the
// effective-env answer (what a pod really receives); a stored-row listing can
// only be own or inherited.
const (
	SourceOwn       = "own"
	SourceInherited = "inherited"
	SourceDerived   = "derived"
)

// CodeEnvVarInherited / CodeEnvVarNotFound are the per-key delete refusals.
// A 404 carrying NO code is something else entirely: the route itself is
// missing, i.e. a backend older than the feature.
const (
	CodeEnvVarInherited = "env_var_inherited"
	CodeEnvVarNotFound  = "env_var_not_found"
)

// ResolvedEnvVar is one variable of a site's resolved environment: the winning
// row plus where it came from.
type ResolvedEnvVar struct {
	Key        string  `json:"key"`
	Value      string  `json:"value"`
	BuildTime  bool    `json:"build_time"`
	BuildValue *string `json:"build_value,omitempty"`
	Source     string  `json:"source"`
}

// EnvBaseSite names the site a child inherits from. The slug is what every
// message about inheritance shows — an id names nothing to a user.
type EnvBaseSite struct {
	SiteID string `json:"site_id"`
	Slug   string `json:"slug"`
}

// EnvVarDeletion is what removing ONE variable did. InheritedValueRestored is
// the difference between "the variable is gone" and "your override is gone and
// the base site's value is visible again".
type EnvVarDeletion struct {
	Key                    string `json:"key"`
	Deleted                bool   `json:"deleted"`
	InheritedValueRestored bool   `json:"inherited_value_restored"`
	InheritedFrom          string `json:"inherited_from"`
}

// DeleteEnvVar removes one variable from a site
// (DELETE /projects/:id/sites/:siteId/env/:key).
//
// It exists because the replace-all PUT cannot tell "remove my override" from
// "remove a variable I only inherit": the first re-exposes the base site's
// value, the second is refused with 409 CodeEnvVarInherited naming the site
// that actually holds it. A 404 with CodeEnvVarNotFound is a key the site
// neither owns nor inherits; a 404 with no code at all is a backend that does
// not serve this route yet, which callers fall back from.
func (c *Client) DeleteEnvVar(projectID, siteID, key string) (*EnvVarDeletion, error) {
	resp, err := c.authRequest("DELETE", "/api/v1/projects/"+projectID+"/sites/"+siteID+"/env/"+url.PathEscape(key), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out EnvVarDeletion
	if err := c.decodeJSON(resp, &out); err != nil {
		return nil, err
	}
	if out.Key == "" {
		out.Key = key
	}
	return &out, nil
}

// EffectiveEnv is the exact environment a pod of the site receives, with the
// provenance of each value. Sources is empty on a backend older than the
// field, which is why every caller renders provenance only when it is there.
type EffectiveEnv struct {
	Env     map[string]string `json:"env"`
	Sources map[string]string `json:"sources"`
}

// EffectiveSiteEnv returns the effective environment plus per-variable
// provenance (GET …/env/effective). Project admin role; the server audits
// every read. Never nil on success.
func (c *Client) EffectiveSiteEnv(projectID, siteID string) (*EffectiveEnv, error) {
	resp, err := c.authRequest("GET", "/api/v1/projects/"+projectID+"/sites/"+siteID+"/env/effective", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, decodeAPIError(resp)
	}
	var out EffectiveEnv
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Env == nil {
		out.Env = map[string]string{}
	}
	if out.Sources == nil {
		out.Sources = map[string]string{}
	}
	return &out, nil
}
