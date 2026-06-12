package runtime

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

type CommandResult struct {
	Command  string        `json:"command"`
	Output   string        `json:"output"`
	ExitCode int           `json:"exit_code"`
	Duration time.Duration `json:"duration"`
	TimedOut bool          `json:"timed_out"`
}

type ProcessSupervisor struct {
	Root      string
	Timeout   time.Duration
	MaxOutput int
}

func (s ProcessSupervisor) Run(ctx context.Context, command string) CommandResult {
	started := time.Now()
	result := CommandResult{Command: command, ExitCode: -1}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var executable string
	var args []string
	if runtime.GOOS == "windows" {
		executable, args = "cmd", []string{"/C", command}
	} else {
		executable, args = "sh", []string{"-lc", command}
	}
	cmd := exec.CommandContext(callCtx, executable, args...)
	cmd.Dir = s.Root
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	result.Duration = time.Since(started)
	result.TimedOut = errors.Is(callCtx.Err(), context.DeadlineExceeded)
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	text := strings.TrimSpace(output.String())
	limit := s.MaxOutput
	if limit <= 0 {
		limit = 128 << 10
	}
	if len(text) > limit {
		text = text[:limit] + "\n… output truncated"
	}
	result.Output = text
	if err == nil {
		result.ExitCode = 0
	}
	return result
}
