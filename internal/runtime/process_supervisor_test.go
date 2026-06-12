package runtime

import (
	"context"
	"testing"
	"time"
)

func TestProcessSupervisorCapturesExit(t *testing.T) {
	result := (ProcessSupervisor{Root: t.TempDir(), Timeout: time.Second}).Run(context.Background(), "printf frontier")
	if result.ExitCode != 0 || result.Output != "frontier" || result.TimedOut {
		t.Fatalf("unexpected result: %#v", result)
	}
}
