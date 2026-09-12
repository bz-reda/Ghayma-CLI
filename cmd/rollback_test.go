package cmd

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"paas-cli/internal/api"
)

// deployment builds a listed row: id, status, and the rollback_available the
// API sent (nil = the field was absent, i.e. an older API).
func deployment(id, status string, available *bool) api.DeploymentInfo {
	return api.DeploymentInfo{ID: id, Status: status, ImageTag: "img-" + id, RollbackAvailable: available}
}

// TestRollbackOptions is the filter pin for the 2026-09-12 rollback window:
// the picker must offer only versions the API says it can still restore, so a
// user never picks a deployment whose image left the registry (amber-gas,
// `rollback image tag "1787742399" not found in registry`).
func TestRollbackOptions(t *testing.T) {
	cases := []struct {
		name        string
		deployments []api.DeploymentInfo
		wantIDs     []string
		wantPrev    bool
	}{
		{
			name: "unavailable rows are dropped",
			deployments: []api.DeploymentInfo{
				deployment("d5", "live", boolPtr(true)), // currently serving
				deployment("d4", "live", boolPtr(true)),
				deployment("d3", "live", boolPtr(false)), // outside the window
				deployment("d2", "live", boolPtr(false)),
			},
			wantIDs:  []string{"d4"},
			wantPrev: true,
		},
		{
			name: "older API omits the field — everything stays listed",
			deployments: []api.DeploymentInfo{
				deployment("d3", "live", nil),
				deployment("d2", "live", nil),
				deployment("d1", "live", nil),
			},
			wantIDs:  []string{"d2", "d1"},
			wantPrev: true,
		},
		{
			name: "mixed: only the flagged rows go",
			deployments: []api.DeploymentInfo{
				deployment("d4", "live", boolPtr(true)),
				deployment("d3", "live", nil),
				deployment("d2", "live", boolPtr(false)),
				deployment("d1", "live", boolPtr(true)),
			},
			wantIDs:  []string{"d3", "d1"},
			wantPrev: true,
		},
		{
			name: "failed and building rows never reach the picker",
			deployments: []api.DeploymentInfo{
				deployment("d4", "live", boolPtr(true)),
				deployment("d3", "failed", boolPtr(true)),
				deployment("d2", "building", nil),
				deployment("d1", "live", boolPtr(true)),
			},
			wantIDs:  []string{"d1"},
			wantPrev: true,
		},
		{
			name: "a row without an image is not restorable",
			deployments: []api.DeploymentInfo{
				deployment("d3", "live", boolPtr(true)),
				{ID: "d2", Status: "live", RollbackAvailable: boolPtr(true)},
				deployment("d1", "live", boolPtr(true)),
			},
			wantIDs:  []string{"d1"},
			wantPrev: true,
		},
		{
			name: "every previous version is outside the window",
			deployments: []api.DeploymentInfo{
				deployment("d3", "live", boolPtr(true)),
				deployment("d2", "live", boolPtr(false)),
				deployment("d1", "live", boolPtr(false)),
			},
			wantIDs:  nil,
			wantPrev: true,
		},
		{
			name:        "a single deployment has no previous version",
			deployments: []api.DeploymentInfo{deployment("d1", "live", boolPtr(true))},
			wantIDs:     nil,
			wantPrev:    false,
		},
		{
			name:        "no deployments at all",
			deployments: nil,
			wantIDs:     nil,
			wantPrev:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			options, hasPrevious := rollbackOptions(tc.deployments)
			if hasPrevious != tc.wantPrev {
				t.Errorf("hasPrevious = %v; want %v", hasPrevious, tc.wantPrev)
			}
			var gotIDs []string
			for _, d := range options {
				gotIDs = append(gotIDs, d.ID)
			}
			if strings.Join(gotIDs, ",") != strings.Join(tc.wantIDs, ",") {
				t.Errorf("options = %v; want %v", gotIDs, tc.wantIDs)
			}
		})
	}
}

// TestRollbackUnavailableMsg pins the wording shown when nothing is listable —
// it has to name the window, since the deployments are still on screen in the
// dashboard with their Rollback button disabled.
func TestRollbackUnavailableMsg(t *testing.T) {
	want := "No deployment is available for rollback (only the last 10 successful deployments of a site can be restored)."
	if rollbackUnavailableMsg != want {
		t.Errorf("rollbackUnavailableMsg = %q; want %q", rollbackUnavailableMsg, want)
	}
}

// TestRollbackErrorText proves the 409 reason reaches the user verbatim: the
// API writes that sentence for the customer, the CLI must not reword or bury
// it behind a generic "Rollback failed".
func TestRollbackErrorText(t *testing.T) {
	apiMsg := "This deployment's image is no longer stored in the registry, so it can't be restored. Deploy again from source instead."

	got := rollbackErrorText(&api.APIError{Status: http.StatusConflict, Message: apiMsg})
	if got != apiMsg {
		t.Errorf("rollbackErrorText(409) = %q; want the API message verbatim", got)
	}

	// A status with no message body still says something actionable.
	got = rollbackErrorText(&api.APIError{Status: http.StatusBadGateway})
	if !strings.Contains(got, "502") {
		t.Errorf("rollbackErrorText(502, no body) = %q; want the status in the text", got)
	}

	// Transport errors keep the generic prefix.
	got = rollbackErrorText(errors.New("dial tcp: connection refused"))
	if !strings.HasPrefix(got, "Rollback failed: ") || !strings.Contains(got, "connection refused") {
		t.Errorf("rollbackErrorText(transport) = %q; want the generic prefix + cause", got)
	}
}
