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

func TestPlanBuildsHierarchicalPhaseTreeWithDependencies(t *testing.T) {
	plan := newPlan("ship safely", []string{"inspect code", "patch implementation", "run tests"})
	plan.applyDependencies([]modelToolAction{
		{ID: "inspect", Name: "read_file"},
		{ID: "patch", Name: "apply_patch", DependsOn: []string{"inspect"}},
		{ID: "verify", Name: "go_test", DependsOn: []string{"patch"}},
	})
	plan.rebuildMaps()
	if len(plan.Tree) != 3 {
		t.Fatalf("phase tree has %d roots, want 3: %#v", len(plan.Tree), plan.Tree)
	}
	if got := plan.Tree[1].Children[0].DependsOn; len(got) != 1 || got[0] != "task-1" {
		t.Fatalf("implementation dependency = %#v, want task-1", got)
	}
	rendered := plan.Render()
	for _, want := range []string{"Investigation", "Implementation", "Verification", "← task-1", "← task-2"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered tree missing %q:\n%s", want, rendered)
		}
	}
}
