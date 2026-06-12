package agent

import "testing"

func TestClassifyExecutionIntentRequiresWorkspaceEvidenceForMutations(t *testing.T) {
	intent := classifyExecutionIntent("Fix the provider toolcalling in this repository, edit existing files, and run tests")
	if !intent.RequiresWorkspace || !intent.RequiresWrite || !intent.RequiresVerification {
		t.Fatalf("intent = %+v", intent)
	}
}

func TestClassifyExecutionIntentRecognizesGitMutation(t *testing.T) {
	intent := classifyExecutionIntent("Create a branch and git commit the current project changes")
	if !intent.RequiresWorkspace || !intent.RequiresGitMutation {
		t.Fatalf("intent = %+v", intent)
	}
}

func TestClassifyExecutionIntentDoesNotForceWritesForAdvice(t *testing.T) {
	intent := classifyExecutionIntent("How should I fix a file editing loop in a coding harness?")
	if intent.RequiresWrite {
		t.Fatalf("advisory prompt was classified as a mutation: %+v", intent)
	}
}
