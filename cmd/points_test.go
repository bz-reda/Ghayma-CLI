package cmd

import (
	"strings"
	"testing"

	"paas-cli/internal/api"
)

// A normal under-budget summary renders the meter line and a breakdown table
// with the KIND / NAME / DETAIL / PTS columns, and no warning/notes.
func TestRenderPointsSummary_UnderBudget(t *testing.T) {
	s := &api.ProjectPointsSummary{
		Budget:    30,
		Used:      12,
		Remaining: 18,
		OverBy:    0,
		Enforced:  true,
		PAYG:      false,
		Breakdown: []api.ResourceCost{
			{Kind: "app", ID: "app-1", Name: "web", Points: 6, Detail: "tier m × 2"},
			{Kind: "database", ID: "db-1", Name: "pg", Points: 6, Detail: "tier s + 10 GB"},
		},
	}

	out := renderPointsSummary(s)

	if !strings.Contains(out, "Points: 12/30 used · 18 remaining") {
		t.Errorf("missing meter line, got:\n%s", out)
	}
	for _, col := range []string{"KIND", "NAME", "DETAIL", "PTS"} {
		if !strings.Contains(out, col) {
			t.Errorf("missing table column %q, got:\n%s", col, out)
		}
	}
	if !strings.Contains(out, "web") || !strings.Contains(out, "tier m × 2") {
		t.Errorf("missing app row, got:\n%s", out)
	}
	if !strings.Contains(out, "pg") || !strings.Contains(out, "tier s + 10 GB") {
		t.Errorf("missing database row, got:\n%s", out)
	}
	if strings.Contains(out, "over budget by") {
		t.Errorf("under-budget summary must not show over-budget line, got:\n%s", out)
	}
	if strings.Contains(out, "enforcement not active yet") {
		t.Errorf("enforced summary must not show enforcement note, got:\n%s", out)
	}
}

// An over-budget summary shows the "over budget by N" warning line.
func TestRenderPointsSummary_OverBudget(t *testing.T) {
	s := &api.ProjectPointsSummary{
		Budget:    30,
		Used:      35,
		Remaining: 0,
		OverBy:    5,
		Enforced:  true,
		Breakdown: []api.ResourceCost{
			{Kind: "app", ID: "app-1", Name: "web", Points: 35, Detail: "tier l × 3"},
		},
	}

	out := renderPointsSummary(s)

	if !strings.Contains(out, "over budget by 5") {
		t.Errorf("missing over-budget line, got:\n%s", out)
	}
}

// When enforcement is not yet active the "(enforcement not active yet)" note
// is shown alongside the meter.
func TestRenderPointsSummary_EnforcementNotActive(t *testing.T) {
	s := &api.ProjectPointsSummary{
		Budget:    30,
		Used:      12,
		Remaining: 18,
		Enforced:  false,
		Breakdown: []api.ResourceCost{
			{Kind: "app", ID: "app-1", Name: "web", Points: 12, Detail: "tier m × 1"},
		},
	}

	out := renderPointsSummary(s)

	if !strings.Contains(out, "(enforcement not active yet)") {
		t.Errorf("missing enforcement note, got:\n%s", out)
	}
	if !strings.Contains(out, "Points: 12/30 used · 18 remaining") {
		t.Errorf("meter line still expected when not enforced, got:\n%s", out)
	}
}

// A PAYG summary hides the points meter and shows the pay-as-you-go note, but
// still itemizes the per-resource breakdown.
func TestRenderPointsSummary_PAYG(t *testing.T) {
	s := &api.ProjectPointsSummary{
		Budget:    0,
		Used:      12,
		Remaining: 0,
		Enforced:  false,
		PAYG:      true,
		Breakdown: []api.ResourceCost{
			{Kind: "app", ID: "app-1", Name: "web", Points: 12, Detail: "tier m × 1"},
		},
	}

	out := renderPointsSummary(s)

	if strings.Contains(out, "Points:") {
		t.Errorf("PAYG summary must hide the points meter, got:\n%s", out)
	}
	if !strings.Contains(out, "Pay-as-you-go") {
		t.Errorf("missing PAYG note, got:\n%s", out)
	}
	if !strings.Contains(out, "KIND") {
		t.Errorf("PAYG summary should still show the breakdown table, got:\n%s", out)
	}
}

// A breakdown row with an empty Detail is marked as untiered pending the next
// deploy.
func TestRenderPointsSummary_UntieredRow(t *testing.T) {
	s := &api.ProjectPointsSummary{
		Budget:    30,
		Used:      6,
		Remaining: 24,
		Enforced:  true,
		Breakdown: []api.ResourceCost{
			{Kind: "app", ID: "app-1", Name: "web", Points: 6, Detail: ""},
		},
	}

	out := renderPointsSummary(s)

	if !strings.Contains(out, "untiered (pending next deploy)") {
		t.Errorf("missing untiered marker, got:\n%s", out)
	}
}
