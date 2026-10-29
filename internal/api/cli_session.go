package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
)

// CLISession is a started browser login: the code to poll and the page that
// approves it.
type CLISession struct {
	Code      string `json:"code"`
	ExpiresIn int    `json:"expires_in"`
	LoginURL  string `json:"login_url"`
}

// CLISessionPoll is one answer to a poll: 202 while waiting, 200 once approved.
type CLISessionPoll struct {
	Status    int    `json:"-"`
	Confirmed bool   `json:"confirmed"`
	Token     string `json:"token"`
	Email     string `json:"email"`
}

// CreateCLISession starts a browser login. Any status but 200 is an *APIError
// carrying the server's message and how long it asked to wait.
func (c *Client) CreateCLISession(ctx context.Context) (*CLISession, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.APIHost+"/api/v1/auth/cli/session", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, newAPIError(resp)
	}
	var s CLISession
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, err
	}
	return &s, nil
}

// PollCLISession checks a browser login once. Any status but 200 and 202 is an
// *APIError. One attempt only: the caller's poll loop is the retry.
func (c *Client) PollCLISession(ctx context.Context, code string) (*CLISessionPoll, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.APIHost+"/api/v1/auth/cli/session/"+url.PathEscape(code), nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return nil, newAPIError(resp)
	}
	var p CLISessionPoll
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	p.Status = resp.StatusCode
	return &p, nil
}
