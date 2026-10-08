package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"strconv"
)

// SetValkeyMode switches a Valkey database between cache and store. The server
// restarts the database before it answers.
func (c *Client) SetValkeyMode(id, mode string) (*DatabaseInfo, error) {
	body, _ := json.Marshal(map[string]string{"mode": mode})
	resp, err := c.authRequest("PATCH", "/api/v1/databases/"+id+"/valkey-mode", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Database DatabaseInfo `json:"database"`
	}
	if err := c.decodeJSON(resp, &out); err != nil {
		return nil, err
	}
	return &out.Database, nil
}

// StreamDatabaseLogs opens a database's engine log: the last `lines` lines,
// then new ones while follow holds and ctx lives (the server ends a follow
// after 10 minutes). The caller closes the stream.
func (c *Client) StreamDatabaseLogs(ctx context.Context, id string, lines int, follow bool) (io.ReadCloser, error) {
	q := url.Values{}
	q.Set("tail", strconv.Itoa(lines))
	if follow {
		q.Set("follow", "1")
	}
	resp, err := c.authRequestContext(ctx, "GET", "/api/v1/databases/"+id+"/logs?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		return nil, c.decodeJSON(resp, nil)
	}
	return resp.Body, nil
}
