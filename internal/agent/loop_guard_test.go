package agent

import "testing"

func TestProgressGuardForcesReplanThenStops(t *testing.T) {
	guard := NewProgressGuard()
	snapshot := ProgressSnapshot{Goal: "fix", WorkspaceRevision: 1, EvidenceDigest: "same"}
	if decision := guard.Record(snapshot, false); decision.Action != LoopContinue {
		t.Fatalf("first observation should continue: %#v", decision)
	}
	if decision := guard.Record(snapshot, false); decision.Action != LoopReplan {
		t.Fatalf("second observation should replan: %#v", decision)
	}
	if decision := guard.Record(snapshot, false); decision.Action != LoopStop {
		t.Fatalf("post-replan repetition should stop: %#v", decision)
	}
}

func TestProgressGuardResetsOnProgress(t *testing.T) {
	guard := NewProgressGuard()
	snapshot := ProgressSnapshot{Goal: "fix"}
	_ = guard.Record(snapshot, false)
	if decision := guard.Record(ProgressSnapshot{Goal: "fix", WorkspaceRevision: 1}, true); decision.Action != LoopContinue {
		t.Fatalf("progress should reset guard: %#v", decision)
	}
}
