package api

import (
	"errors"
	"net/http"
	"strconv"
	"testing"
)

// TestRollback_ConflictSurfacesAPIMessage pins the 409 the API answers when a
// version can no longer be restored: the CLI must hand the customer that exact
// sentence, not the raw body it used to print ("failed: {\"error\":…}").
func TestRollback_ConflictSurfacesAPIMessage(t *testing.T) {
	const msg = "This deployment is outside the rollback window: only the last 10 successful deployments of a site can be restored. Deploy again from source instead."
	ts := jsonStatusServer(t, http.StatusConflict, `{"error":`+strconv.Quote(msg)+`}`)

	result, err := newTestClient(ts.URL).Rollback("d1")
	if err == nil {
		t.Fatal("Rollback on 409: want an error")
	}
	if result != nil {
		t.Errorf("result = %+v; want nil on 409", result)
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %T; want *APIError", err)
	}
	if apiErr.Status != http.StatusConflict {
		t.Errorf("APIError.Status = %d; want 409", apiErr.Status)
	}
	if err.Error() != msg {
		t.Errorf("err = %q; want the API message verbatim", err)
	}
}

// TestRollback_Succeeds keeps the happy path decoding after the switch to
// decodeJSON.
func TestRollback_Succeeds(t *testing.T) {
	ts := jsonStatusServer(t, http.StatusOK, `{"id":"d9","status":"live","domains":["app.ghayma.app"]}`)

	result, err := newTestClient(ts.URL).Rollback("d1")
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if result.ID != "d9" || len(result.Domains) != 1 || result.Domains[0] != "app.ghayma.app" {
		t.Errorf("result = %+v; want the parsed rollback response", result)
	}
}

// TestListDeployments_RollbackAvailableIsTristate is why the field is a
// pointer: absent (an older API) must stay distinguishable from an explicit
// false, so the CLI lists everything against a backend that predates it.
func TestListDeployments_RollbackAvailableIsTristate(t *testing.T) {
	ts := jsonStatusServer(t, http.StatusOK, `[
		{"id":"d3","status":"live","image_tag":"t3","rollback_available":true},
		{"id":"d2","status":"live","image_tag":"t2","rollback_available":false},
		{"id":"d1","status":"live","image_tag":"t1"}
	]`)

	deployments, err := newTestClient(ts.URL).ListDeployments("p1")
	if err != nil {
		t.Fatalf("ListDeployments: %v", err)
	}
	if len(deployments) != 3 {
		t.Fatalf("got %d deployments; want 3", len(deployments))
	}
	if deployments[0].RollbackAvailable == nil || !*deployments[0].RollbackAvailable {
		t.Errorf("d3 rollback_available = %v; want true", deployments[0].RollbackAvailable)
	}
	if deployments[1].RollbackAvailable == nil || *deployments[1].RollbackAvailable {
		t.Errorf("d2 rollback_available = %v; want false", deployments[1].RollbackAvailable)
	}
	if deployments[2].RollbackAvailable != nil {
		t.Errorf("d1 rollback_available = %v; want nil when the API omits it", *deployments[2].RollbackAvailable)
	}
}
