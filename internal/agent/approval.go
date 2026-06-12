package agent

import (
	"context"
	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/tools"
)

// ExecuteApproved runs a previously approved tool call and preserves the
// approval identity so the resumed agent can treat it as completed evidence.
func (r Runner) ExecuteApproved(ctx context.Context, pending PendingApproval) history.Event {
	defer r.Tools.Close()
	if r.MCP != nil && r.MCP.Configured() {
		_ = r.discoverMCPTools(ctx)
		defer r.MCP.Close()
	}
	call := pending.Call
	if normalized, err := r.normalizeToolCall(call); err == nil {
		call = normalized
	}
	var snapshot *workspaceSnapshot
	if pending.SnapshotPath != "" {
		snapshot, _ = loadWorkspaceSnapshot(pending.SnapshotPath)
	}
	if snapshot == nil && !r.Config.AgentDryRun && r.Config.SandboxMode == config.SandboxSnapshot && r.toolRisk(call.Name) != tools.RiskRead {
		maxBytes := int64(r.Config.AgentSnapshotMaxMB) * 1024 * 1024
		created, err := createWorkspaceSnapshot(r.Tools.WorkspaceRoot, maxBytes)
		if err != nil {
			result := tools.Result{Tool: call.Name, OK: false, Error: "workspace snapshot failed; approved action was not executed: " + err.Error(), Metadata: map[string]any{"snapshot_failed": true}}
			return toolResultEvent(pending.RunID, 0, result)
		}
		snapshot = created
	}
	result := r.executeWithRecovery(tools.WithApproval(ctx), call, 0, nil)
	fingerprint := pending.Fingerprint
	if fingerprint == "" {
		fingerprint = toolFingerprint(call)
	}
	attachCallMetadata(&result, fingerprint)
	result.Metadata["approved"] = true
	result.Metadata["call_event_id"] = pending.CallEventID
	result.Metadata["approval_event_id"] = pending.ApprovalEventID
	if pending.ProviderCallID != "" {
		result.Metadata["provider_call_id"] = pending.ProviderCallID
	}
	if snapshot != nil {
		result.Metadata["snapshot_path"] = snapshot.Directory
	}
	return toolResultEvent(pending.RunID, 0, result)
}
