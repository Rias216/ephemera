package metrics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistrySnapshotAndExports(t *testing.T) {
	registry := NewRegistry()
	registry.SetEnabled(true)
	registry.Add("agent_runs_total", 2)
	registry.Set("agent_active", 1)
	registry.Observe("agent_run_duration_seconds", 0.5)
	registry.Observe("agent_run_duration_seconds", 1.5)

	snapshot := registry.Snapshot()
	if snapshot.Counters["agent_runs_total"] != 2 || snapshot.Gauges["agent_active"] != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	h := snapshot.Histograms["agent_run_duration_seconds"]
	if h.Count != 2 || h.Sum != 2 || h.Min != 0.5 || h.Max != 1.5 {
		t.Fatalf("histogram = %#v", h)
	}
	text := registry.Prometheus()
	if !strings.Contains(text, "agent_runs_total 2") || !strings.Contains(text, "agent_run_duration_seconds_count 2") {
		t.Fatalf("prometheus = %q", text)
	}
	path := filepath.Join(t.TempDir(), "metrics", "snapshot.json")
	if err := registry.WriteJSON(path); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || !strings.Contains(string(data), "agent_runs_total") {
		t.Fatalf("json read err=%v data=%q", err, data)
	}
}
