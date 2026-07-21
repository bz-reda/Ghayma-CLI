package api

import (
	"encoding/json"
	"net/url"
	"time"
)

// Per-site cron jobs. Project-scoped REST surface (the site is a query filter /
// a field on each job). Backend: Ghayma-backend cron-jobs (spec §3/§8):
//   GET  /api/v1/projects/:id/crons?site_id=…
//   GET  /api/v1/projects/:id/crons/:cronId/runs
//   POST /api/v1/projects/:id/crons/:cronId/trigger

// CronRun is one execution record — also the shape of a job's embedded last_run.
// Only the fields the CLI renders are decoded; the server's row carries more.
type CronRun struct {
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Status     string     `json:"status"`
	HTTPStatus int        `json:"http_status"`
	DurationMS int64      `json:"duration_ms"`
	Error      string     `json:"error"`
}

// CronJob is one scheduled job plus its most-recent run (the list/get shape).
// The server never serializes the HMAC secret, so it is absent here by design.
type CronJob struct {
	ID             string     `json:"id"`
	SiteID         string     `json:"site_id"`
	Name           string     `json:"name"`
	Schedule       string     `json:"schedule"`
	Timezone       string     `json:"timezone"`
	Type           string     `json:"type"`
	Path           string     `json:"path"`
	Method         string     `json:"method"`
	TimeoutSeconds int        `json:"timeout_seconds"`
	Enabled        bool       `json:"enabled"`
	NextRunAt      *time.Time `json:"next_run_at"`
	LastRunAt      *time.Time `json:"last_run_at"`
	LastRun        *CronRun   `json:"last_run"`
}

// ListCrons returns a project's cron jobs, optionally filtered to one site
// (GET /api/v1/projects/:id/crons[?site_id=…] → {"crons":[...]}). An empty
// siteID lists every site's jobs. A non-200 surfaces the server's message.
func (c *Client) ListCrons(projectID, siteID string) ([]CronJob, error) {
	path := "/api/v1/projects/" + projectID + "/crons"
	if siteID != "" {
		path += "?" + url.Values{"site_id": {siteID}}.Encode()
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
		Crons []CronJob `json:"crons"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Crons, nil
}

// ListCronRuns returns a job's most recent runs (last 50 — spec §8) via
// GET /api/v1/projects/:id/crons/:cronId/runs → {"runs":[...]}.
func (c *Client) ListCronRuns(projectID, cronID string) ([]CronRun, error) {
	resp, err := c.authRequest("GET", "/api/v1/projects/"+projectID+"/crons/"+cronID+"/runs", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, decodeAPIError(resp)
	}

	var out struct {
		Runs []CronRun `json:"runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Runs, nil
}

// TriggerCron manually fires a job now (POST …/crons/:cronId/trigger → 202).
// The server still honors the one-run-at-a-time overlap rule. A non-2xx
// surfaces the server's message (already human — spec §7).
func (c *Client) TriggerCron(projectID, cronID string) error {
	resp, err := c.authRequest("POST", "/api/v1/projects/"+projectID+"/crons/"+cronID+"/trigger", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeAPIError(resp)
	}
	return nil
}
