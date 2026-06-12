package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/tools"
)

func (r Runner) hasMCPTool(name string) bool { return r.MCP != nil && r.MCP.HasTool(name) }

func (r Runner) normalizeToolCall(call tools.Call) (tools.Call, error) {
	call.Name = strings.TrimSpace(call.Name)
	if call.Arguments == nil {
		call.Arguments = map[string]any{}
	}
	if r.hasMCPTool(call.Name) {
		return call, r.MCP.Validate(call.Name, call.Arguments)
	}
	return r.Tools.Normalize(call)
}

func (r Runner) toolRisk(name string) tools.Risk {
	if tool, ok := tools.Lookup(name); ok {
		return tool.Risk
	}
	if r.hasMCPTool(name) {
		if r.MCP.ReadOnly(name) {
			return tools.RiskRead
		}
		return tools.RiskShell
	}
	return ""
}

func (r Runner) requiresApproval(name string) bool {
	if !r.hasMCPTool(name) {
		return r.Tools.RequiresApproval(name)
	}
	switch r.Config.ApprovalPolicy {
	case config.ApprovalAutoApprove, config.ApprovalWorkspaceWrite:
		return false
	case config.ApprovalApproveWrites, config.ApprovalReadOnly:
		return !r.MCP.ReadOnly(name)
	default:
		return true
	}
}

func (r Runner) executeMCPTool(ctx context.Context, call tools.Call) tools.Result {
	started := time.Now()
	result := r.MCP.Call(ctx, call.Name, call.Arguments)
	metadata := result.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["risk"] = string(r.toolRisk(call.Name))
	metadata["duration_ms"] = time.Since(started).Milliseconds()
	return tools.Result{Tool: call.Name, OK: result.OK, Output: result.Output, Error: result.Error, Metadata: metadata, Duration: time.Since(started)}
}

func (r Runner) eventRisk(event history.Event) tools.Risk {
	if risk := metadataString(event.Metadata, "risk"); risk != "" {
		return tools.Risk(risk)
	}
	return r.toolRisk(event.Tool)
}

func mcpDiscoveryObservation(err error) string {
	return fmt.Sprintf("[MCP discovery warning]\n%s\nContinue with the available tools; do not repeatedly retry this server during the same run.", err)
}
