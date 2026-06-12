package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Handler executes a dynamically registered tool. It receives the active
// registry so workspace boundaries and approval settings remain consistent.
type Handler func(context.Context, Registry, Call, func(string)) Result

type registeredTool struct {
	Tool    Tool
	Handler Handler
}

var toolCatalog = struct {
	sync.RWMutex
	items map[string]registeredTool
}{items: map[string]registeredTool{}}

func init() {
	for _, tool := range builtinTools() {
		if err := Register(tool, nil); err != nil {
			panic(err)
		}
	}
}

// Register adds or replaces a tool contract and optional local handler.
func Register(tool Tool, handler Handler) error {
	tool.Name = strings.TrimSpace(tool.Name)
	if tool.Name == "" {
		return fmt.Errorf("tool name is required")
	}
	if strings.TrimSpace(tool.Description) == "" {
		return fmt.Errorf("tool %q description is required", tool.Name)
	}
	if tool.Risk != RiskRead && tool.Risk != RiskWrite && tool.Risk != RiskShell {
		return fmt.Errorf("tool %q has unsupported risk %q", tool.Name, tool.Risk)
	}
	toolCatalog.Lock()
	toolCatalog.items[tool.Name] = registeredTool{Tool: tool, Handler: handler}
	toolCatalog.Unlock()
	return nil
}

// Register adds a tool through the active registry API. Registration is
// process-wide so provider schemas, MCP bridges, and execution share one view.
func (r Registry) Register(tool Tool, handler Handler) error { return Register(tool, handler) }

// Builtins returns a deterministic snapshot of all registered local tools.
func Builtins() []Tool {
	toolCatalog.RLock()
	out := make([]Tool, 0, len(toolCatalog.items))
	for _, item := range toolCatalog.items {
		out = append(out, item.Tool)
	}
	toolCatalog.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func registeredHandler(name string) Handler {
	toolCatalog.RLock()
	item := toolCatalog.items[name]
	toolCatalog.RUnlock()
	return item.Handler
}
