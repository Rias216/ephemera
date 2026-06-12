package agent

import (
	"context"
	"fmt"

	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/tools"
)

// discoverMCPTools translates every discovered MCP capability into the normal
// runtime registry. From this point onward MCP tools use the same validation,
// approval, dry-run, timeout, sandbox, and debug middleware as built-ins.
func (r Runner) discoverMCPTools(ctx context.Context) []error {
	if r.MCP == nil || !r.MCP.Configured() {
		return nil
	}
	errs := r.MCP.Discover(ctx)
	definitions := r.MCP.Definitions()
	current := make(map[string]bool, len(definitions))
	for _, definition := range definitions {
		current[definition.Name] = true
		if existing, exists := r.Tools.Lookup(definition.Name); exists {
			source, _ := existing.ProviderHints["source"].(string)
			server, _ := existing.ProviderHints["server"].(string)
			remote, _ := existing.ProviderHints["remote_name"].(string)
			newServer, _ := definition.ProviderHints["server"].(string)
			newRemote, _ := definition.ProviderHints["remote_name"].(string)
			if source == "mcp" && server == newServer && remote == newRemote {
				continue
			}
			errs = append(errs, fmt.Errorf("register MCP tool %s: name collides with existing %s tool", definition.Name, firstNonEmpty(source, "built-in")))
			continue
		}
		if err := r.Tools.Register(definition); err != nil {
			errs = append(errs, fmt.Errorf("register MCP tool %s: %w", definition.Name, err))
		}
	}
	for _, definition := range r.Tools.ToolSpecs() {
		source, _ := definition.ProviderHints["source"].(string)
		if source == "mcp" && !current[definition.Name] {
			r.Tools.Unregister(definition.Name)
		}
	}
	return errs
}

func (r Runner) normalizeToolCall(call tools.Call) (tools.Call, error) {
	return r.Tools.Normalize(call)
}

func (r Runner) toolRisk(name string) tools.Risk {
	if tool, ok := r.Tools.Lookup(name); ok {
		return tool.Risk
	}
	return ""
}

func (r Runner) requiresApproval(call tools.Call) bool { return r.Tools.RequiresApprovalCall(call) }

func (r Runner) eventRisk(event history.Event) tools.Risk {
	if risk := metadataString(event.Metadata, "risk"); risk != "" {
		return tools.Risk(risk)
	}
	return r.toolRisk(event.Tool)
}

func mcpDiscoveryObservation(err error) string {
	return fmt.Sprintf("[MCP discovery warning]\n%s\nContinue with the available tools; do not repeatedly retry this server during the same run.", err)
}
