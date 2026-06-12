package agent

import "testing"

func TestRuntimeArtifactsDoNotBecomeTaskChanges(t *testing.T) {
	state := &runState{
		changedPaths:        map[string]bool{},
		changedDirectories:  map[string]bool{},
		runtimeChangedPaths: map[string]bool{},
	}
	state.recordChangedArtifact(`.ephemera/run.sh`, false)
	if state.changed || len(state.changedPaths) != 0 {
		t.Fatalf("runtime helper polluted task changes: %#v", state)
	}
	if !state.runtimeChangedPaths[".ephemera/run.sh"] {
		t.Fatalf("runtime helper was not retained for diagnostics: %#v", state.runtimeChangedPaths)
	}
	state.recordChangedArtifact(`pong/pong.go`, false)
	if !state.changed || !state.changedPaths["pong/pong.go"] {
		t.Fatalf("task artifact was not tracked: %#v", state.changedPaths)
	}
}

func TestRuntimeArtifactClassificationIsNarrow(t *testing.T) {
	for _, path := range []string{
		`.ephemera/run.sh`, `.ephemera/metrics.json`, `.ephemera/cache/index.json`, `.ephemera/sessions/current/debug.jsonl`,
	} {
		if !runtimeArtifactPath(path) {
			t.Fatalf("%q must be classified as runtime-generated", path)
		}
	}
	for _, path := range []string{`.ephemera/project.json`, `.ephemera/instructions.md`, `.ephemera/preferences.json`, `pong/pong.go`} {
		if runtimeArtifactPath(path) {
			t.Fatalf("%q is user/project-owned and must remain task-visible", path)
		}
	}
}
