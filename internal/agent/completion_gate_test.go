package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/reasoning"
)

func TestCompletionGateBlocksPlainTextFinalAfterUnverifiedWrite(t *testing.T) {
	cfg := config.Default()
	cfg.WorkspaceRoot = t.TempDir()
	cfg.ApprovalPolicy = config.ApprovalAutoApprove
	cfg.AgentAutoVerify = true
	cfg.AgentAutoReview = false
	cfg.AutoTestCommand = ""
	provider := &fakeProvider{responses: []string{
		`{"summary":"create","actions":[{"tool":"apply_patch","arguments":{"path":"main.go","content":"package main\n"}}]}`,
		`done`,
	}}
	session := history.New("completion-gate", "fake", cfg.Model(), reasoning.ModeNormal)
	session.Append("user", "create main.go")

	result := NewRunner(cfg, provider).Run(context.Background(), session)
	if result.Completion == nil || result.Completion.Passed {
		t.Fatalf("expected blocked completion report: %#v", result.Completion)
	}
	if !strings.Contains(strings.ToLower(result.Text), "completion blocked") {
		t.Fatalf("expected blocked final text, got %q", result.Text)
	}
	if !evalHasEvent(result.Events, history.EventVerification) {
		t.Fatalf("expected verification event: %#v", result.Events)
	}
}
