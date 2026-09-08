package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGetDeployment_ParsesQueuePosition pins the wire contract the deploy's
// "Queued, position N of M" line reads: queue_position is the 1-based rank
// among the deployments queued in the same queue, queue_size how many wait
// there.
func TestGetDeployment_ParsesQueuePosition(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/deployments/d1" || r.Method != http.MethodGet {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("auth header = %q; want Bearer test-token", got)
		}
		io.WriteString(w, `{"id":"d1","status":"queued","queue_position":3,"queue_size":7}`)
	}))
	defer ts.Close()

	d, err := newTestClient(ts.URL).GetDeployment("d1")
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if d.Status != "queued" || d.QueuePosition != 3 || d.QueueSize != 7 {
		t.Errorf("got status=%q position=%d size=%d; want queued 3/7", d.Status, d.QueuePosition, d.QueueSize)
	}
}

// TestGetDeployment_QueueFieldsDefaultToZero covers a server that does not send
// the fields at all — an older backend, or a deployment that is no longer
// queued. Both must decode as 0/0, which is what the CLI reads as "no position
// to report" and falls back to the generic waiting line.
func TestGetDeployment_QueueFieldsDefaultToZero(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"d1","status":"building","image_tag":"1757000000","domains":["demo.ghayma.app"]}`)
	}))
	defer ts.Close()

	d, err := newTestClient(ts.URL).GetDeployment("d1")
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if d.QueuePosition != 0 || d.QueueSize != 0 {
		t.Errorf("absent fields decoded as %d/%d; want 0/0", d.QueuePosition, d.QueueSize)
	}
	if len(d.Domains) != 1 || d.Domains[0] != "demo.ghayma.app" {
		t.Errorf("the rest of the payload must still decode: %+v", d)
	}
}
