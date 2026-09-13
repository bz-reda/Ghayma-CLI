package api

import (
	"bytes"
	"encoding/json"
)

// Tunnels (Ghayma-backend, 2026-09-13). A tunnel session is a short-lived
// grant that lets a developer's machine reach the databases the site is
// connected to, through the platform's gateway. Opening one needs the project
// admin role — the same bar as the effective environment, since the same
// credentials are what the tunnel carries.

// TunnelTarget is one database the session may reach. Host and Port are the
// in-cluster address the site's variables name, which is what the CLI rewrites
// to a local listener.
type TunnelTarget struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	Host string `json:"host"`
	Port int    `json:"port"`
}

// TunnelSession is an open session: the bearer token the gateway accepts, when
// it stops accepting it, the gateway to dial and what may be dialed there.
type TunnelSession struct {
	Token      string         `json:"token"`
	ExpiresAt  string         `json:"expires_at"`
	GatewayURL string         `json:"gateway_url"`
	Targets    []TunnelTarget `json:"targets"`
}

// OpenTunnelSession opens a session for one site (POST …/sites/:siteId/
// tunnel-sessions → 201). A 409 means the site has no database connection to
// tunnel; the server's message says so and is passed through.
func (c *Client) OpenTunnelSession(projectID, siteID string) (*TunnelSession, error) {
	resp, err := c.authRequest("POST", "/api/v1/projects/"+projectID+"/sites/"+siteID+"/tunnel-sessions", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, decodeAPIError(resp)
	}
	var session TunnelSession
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		return nil, err
	}
	return &session, nil
}

// CloseTunnelSession revokes a session early (POST …/tunnel-sessions/close).
// The server answers 200 whether or not the session was still open, so a close
// after an expiry is not an error.
func (c *Client) CloseTunnelSession(projectID, siteID, token string) error {
	body, _ := json.Marshal(map[string]string{"token": token})
	resp, err := c.authRequest("POST", "/api/v1/projects/"+projectID+"/sites/"+siteID+"/tunnel-sessions/close", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeAPIError(resp)
	}
	return nil
}
