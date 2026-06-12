package agent

import (
	"strings"
	"testing"

	"github.com/ephemera-ai/ephemera/internal/tools"
)

func TestPlanTracksToolEvidenceAndFailure(t *testing.T) {
	plan := newPlan("upgrade the agent", []string{"inspect", "patch", "verify"})
	plan.markStarted(0, "read_file")
	plan.markResult(0, tools.Call{Name: "read_file"}, tools.Result{OK: true, Output: "found provider.go"})
	plan.markStarted(1, "apply_patch")
	plan.markResult(1, tools.Call{Name: "apply_patch"}, tools.Result{OK: false, Error: "conflict"})
	if plan.Steps[0].Status != PlanDone {
		t.Fatalf("first step = %q", plan.Steps[0].Status)
	}
	if plan.Steps[1].Status != PlanFailed {
		t.Fatalf("second step = %q", plan.Steps[1].Status)
	}
	if !plan.Completed[1] || len(plan.Evidence[2]) == 0 {
		t.Fatalf("plan maps not updated: %#v", plan)
	}
}

func TestPlanRenderShowsCurrentState(t *testing.T) {
	plan := newPlan("ship safely", []string{"inspect", "verify"})
	plan.markStarted(0, "read_file")
	text := plan.Render()
	if !strings.Contains(text, "Goal: ship safely") || !strings.Contains(text, "[>] 1. inspect") {
		t.Fatalf("rendered plan = %q", text)
	}
}
