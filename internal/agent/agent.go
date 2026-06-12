// Package agent implements Ephemera's provider-neutral project agent loop.
package agent

import (
	"context"
	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/llm"
	"github.com/ephemera-ai/ephemera/internal/mcp"
	"github.com/ephemera-ai/ephemera/internal/reasoning"
	"github.com/ephemera-ai/ephemera/internal/tools"
	"time"
)

// PendingApproval is a tool call that must be approved by the user.
type PendingApproval struct {
	Call            tools.Call
	Reason          string
	LocalOnly       bool
	RunID           string
	Purpose         string
	Fingerprint     string
	ProviderCallID  string
	CallEventID     string
	ApprovalEventID string
	SnapshotPath    string
}

// StreamKind identifies one live agent update sent to the TUI.
type StreamKind string

const (
	StreamStatus       StreamKind = "status"
	StreamDelta        StreamKind = "delta"
	StreamReasoning    StreamKind = "reasoning_delta"
	StreamActivity     StreamKind = "activity_delta"
	StreamToolProgress StreamKind = "tool_progress"
	StreamPlan         StreamKind = "plan"
	StreamEvent        StreamKind = "event"
	StreamDone         StreamKind = "done"
)

// StreamUpdate is a provider-neutral live agent update. Delta may contain final
// answer text or an explicitly returned provider reasoning summary, depending
// on Kind. Raw private reasoning is never surfaced or persisted.
type StreamUpdate struct {
	Kind            StreamKind
	RunID           string
	Phase           string
	Iteration       int
	Delta           string
	Event           *history.Event
	Text            string
	Pending         *PendingApproval
	Plan            *Plan
	Err             error
	ContextTokens   int
	OutputTokens    int
	SentMessages    int
	TotalMessages   int
	DroppedMessages int
	Tool            string
	StartedAt       time.Time
	Verified        bool
}

// StreamFunc consumes live updates. It should return quickly.
type StreamFunc func(StreamUpdate)

// RunUsage contains bounded, provider-neutral token and tool estimates.
type RunUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	ToolCalls    int `json:"tool_calls"`
}

// RunResult contains the visible output and structured timeline deltas.
type RunResult struct {
	Text       string
	Events     []history.Event
	Pending    *PendingApproval
	Usage      RunUsage
	Completion *CompletionGateReport
}

// Runner executes agent turns with the configured provider and tools.
type Runner struct {
	Config          config.Config
	Provider        llm.Provider
	Tools           tools.Registry
	MCP             *mcp.Manager
	Embedder        Embedder
	index           *codebaseIndexManager
	delegationDepth int
	delegateRole    string
}

// NewRunner creates an agent runner.
func NewRunner(cfg config.Config, provider llm.Provider) Runner {
	registry := tools.NewRegistry(cfg)
	return Runner{
		Config:   cfg,
		Provider: provider,
		Tools:    registry,
		MCP:      mcp.NewManager(cfg.MCPServers, registry.WorkspaceRoot, cfg.MaxToolOutputTokens),
		Embedder: configuredEmbedder(),
		index:    newCodebaseIndexManager(registry.WorkspaceRoot),
	}
}

func (r Runner) toolSpecs(state *runState) []llm.ToolSpec {
	specs := r.Tools.ToolSpecs()
	filtered := make([]llm.ToolSpec, 0, len(specs))
	for _, spec := range specs {
		source, _ := spec.ProviderHints["source"].(string)
		dynamic := source == "mcp" || source == "plugin"
		if !dynamic && !reasoning.ToolAllowed(r.Config.Mode, spec.Name) {
			continue
		}
		if spec.Name == "delegate" && (r.delegationDepth > 0 || !r.Config.SubagentEnabled) {
			continue
		}
		if state != nil && state.suppressedTools[spec.Name] {
			continue
		}
		filtered = append(filtered, spec)
	}
	return filtered
}

// Run advances the agent until it produces a final answer or needs approval.
func (r Runner) Run(ctx context.Context, session history.Session) RunResult {
	return r.run(ctx, session, nil)
}

// RunStream advances the agent while publishing provider deltas, structured
// reasoning summaries, plans, tool calls, tool results, and completion state.
func (r Runner) RunStream(ctx context.Context, session history.Session, emit StreamFunc) RunResult {
	return r.run(ctx, session, emit)
}

// RunDirector executes the normal agent loop with director mode enabled. The
// configured instrument remains advisory and never receives tool schemas.
func (r Runner) RunDirector(ctx context.Context, session history.Session, emit StreamFunc) RunResult {
	r.Config.DirectorEnabled = true
	return r.run(ctx, session, emit)
}
