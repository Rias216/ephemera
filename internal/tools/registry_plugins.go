package tools

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

type toolCatalogState struct {
	mu    sync.RWMutex
	items map[string]Tool
}

func newToolCatalog() *toolCatalogState {
	return &toolCatalogState{items: map[string]Tool{}}
}

func (catalog *toolCatalogState) clone() *toolCatalogState {
	if catalog == nil {
		return newToolCatalog()
	}
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	copyCatalog := newToolCatalog()
	for name, tool := range catalog.items {
		copyCatalog.items[name] = tool
	}
	return copyCatalog
}

func (catalog *toolCatalogState) register(tool Tool, handler ...Handler) error {
	tool.Name = strings.TrimSpace(tool.Name)
	if len(handler) > 0 {
		tool.Execute = handler[0]
	}
	if tool.Name == "" {
		return fmt.Errorf("tool name is required")
	}
	if strings.TrimSpace(tool.Description) == "" {
		return fmt.Errorf("tool %q description is required", tool.Name)
	}
	if tool.Risk != RiskRead && tool.Risk != RiskWrite && tool.Risk != RiskShell {
		return fmt.Errorf("tool %q has unsupported risk %q", tool.Name, tool.Risk)
	}
	if tool.Execute == nil {
		return fmt.Errorf("tool %q executor is required", tool.Name)
	}
	if tool.Version == "" {
		tool.Version = "1.0.0"
	}
	tool.Parameters = tool.ParameterSchema()
	if tool.Parameters.Type == "" {
		tool.Parameters.Type = "object"
	}
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	if _, exists := catalog.items[tool.Name]; exists {
		return fmt.Errorf("tool %q is already registered", tool.Name)
	}
	catalog.items[tool.Name] = tool
	return nil
}

func (catalog *toolCatalogState) unregister(name string) bool {
	if catalog == nil {
		return false
	}
	name = strings.TrimSpace(name)
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	if _, exists := catalog.items[name]; !exists {
		return false
	}
	delete(catalog.items, name)
	return true
}

func (catalog *toolCatalogState) lookup(name string) (Tool, bool) {
	if catalog == nil {
		return Tool{}, false
	}
	catalog.mu.RLock()
	tool, ok := catalog.items[strings.TrimSpace(name)]
	catalog.mu.RUnlock()
	return tool, ok
}

func (catalog *toolCatalogState) snapshot() []Tool {
	if catalog == nil {
		return nil
	}
	catalog.mu.RLock()
	out := make([]Tool, 0, len(catalog.items))
	for _, item := range catalog.items {
		out = append(out, item)
	}
	catalog.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

type registryResources struct {
	mu      sync.Mutex
	closers []io.Closer
}

func (resources *registryResources) add(closer io.Closer) {
	if resources == nil || closer == nil {
		return
	}
	resources.mu.Lock()
	resources.closers = append(resources.closers, closer)
	resources.mu.Unlock()
}

func (resources *registryResources) close() error {
	if resources == nil {
		return nil
	}
	resources.mu.Lock()
	closers := append([]io.Closer(nil), resources.closers...)
	resources.closers = nil
	resources.mu.Unlock()
	var first error
	for index := len(closers) - 1; index >= 0; index-- {
		if err := closers[index].Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

var defaultToolCatalog = newToolCatalog()

func init() {
	for _, tool := range builtinTools() {
		if err := defaultToolCatalog.register(tool); err != nil {
			panic(err)
		}
	}
}

// Register adds one process-wide default tool definition. New registries clone
// the default catalog, so test tools and startup extensions remain isolated from
// registries that were already created.
func Register(tool Tool, handler ...Handler) error {
	return defaultToolCatalog.register(tool, handler...)
}

// Register adds one complete definition to this registry only.
func (r Registry) Register(tool Tool, handler ...Handler) error {
	if r.catalog == nil {
		return fmt.Errorf("tool registry is not initialized")
	}
	return r.catalog.register(tool, handler...)
}

// Lookup finds a process-wide default tool. Runtime code must prefer Registry.Lookup.
func Lookup(name string) (Tool, bool) { return defaultToolCatalog.lookup(name) }

// Unregister removes one runtime-local definition. It is used to reconcile
// dynamic catalogs such as MCP when a server stops advertising a tool.
func (r Registry) Unregister(name string) bool {
	if r.catalog == nil {
		return false
	}
	return r.catalog.unregister(name)
}

// Lookup finds a tool registered for this runtime.
func (r Registry) Lookup(name string) (Tool, bool) {
	if r.catalog == nil {
		return Lookup(name)
	}
	return r.catalog.lookup(name)
}

// Builtins returns a deterministic snapshot of process-wide default tools.
func Builtins() []Tool { return defaultToolCatalog.snapshot() }

// ToolSpecs returns a deterministic snapshot of tools available to this runtime.
func (r Registry) ToolSpecs() []Tool {
	if r.catalog == nil {
		return Builtins()
	}
	return r.catalog.snapshot()
}

// Close releases subprocess plugins and other dynamic registry resources.
func (r Registry) Close() error {
	if r.resources == nil {
		return nil
	}
	return r.resources.close()
}

func toolArgumentSpecs(tool Tool) []ArgumentSpec {
	if len(tool.Arguments) > 0 {
		return append([]ArgumentSpec(nil), tool.Arguments...)
	}
	required := make(map[string]bool, len(tool.Parameters.Required))
	for _, name := range tool.Parameters.Required {
		required[name] = true
	}
	names := make([]string, 0, len(tool.Parameters.Properties))
	for name := range tool.Parameters.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]ArgumentSpec, 0, len(names))
	for _, name := range names {
		property := tool.Parameters.Properties[name]
		out = append(out, ArgumentSpec{Name: name, Type: property.Type, Description: property.Description, Required: required[name]})
	}
	return out
}
