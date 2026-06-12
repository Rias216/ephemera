package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/llm"
)

type Result struct {
	OK       bool
	Output   string
	Error    string
	Metadata map[string]any
}

type binding struct {
	ServerName string
	RemoteName string
	Tool       Tool
	Client     *client
	Exposed    string
}

type Manager struct {
	configs         map[string]config.MCPServerConfig
	workspace       string
	maxOutputChars  int
	mu              sync.RWMutex
	bindings        map[string]binding
	clients         map[string]*client
	discoveryErrors []error
	discovered      bool
	discovering     bool
	discoverDone    chan struct{}
	reconnectMu     sync.Mutex
}

func NewManager(servers map[string]config.MCPServerConfig, workspace string, maxOutputTokens int) *Manager {
	copyConfigs := make(map[string]config.MCPServerConfig, len(servers))
	for name, server := range servers {
		copyConfigs[name] = server
	}
	maxChars := maxOutputTokens * 4
	if maxChars <= 0 {
		maxChars = 24_000
	}
	return &Manager{configs: copyConfigs, workspace: workspace, maxOutputChars: maxChars, bindings: map[string]binding{}, clients: map[string]*client{}}
}

func (m *Manager) Configured() bool {
	for _, server := range m.configs {
		if !server.Disabled {
			return true
		}
	}
	return false
}

func (m *Manager) Discover(ctx context.Context) []error {
	m.mu.Lock()
	if m.discovered {
		errs := append([]error(nil), m.discoveryErrors...)
		m.mu.Unlock()
		return errs
	}
	if m.discovering {
		done := m.discoverDone
		m.mu.Unlock()
		select {
		case <-done:
			return m.Errors()
		case <-ctx.Done():
			return []error{ctx.Err()}
		}
	}
	m.discovering = true
	m.discoverDone = make(chan struct{})
	done := m.discoverDone
	m.mu.Unlock()

	names := make([]string, 0, len(m.configs))
	for name, server := range m.configs {
		if !server.Disabled {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	type discoveredServer struct {
		name   string
		client *client
		tools  []Tool
		err    error
	}
	results := make(chan discoveredServer, len(names))
	var wg sync.WaitGroup
	for _, name := range names {
		server := m.configs[name]
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			client, tools, err := m.connect(ctx, name, server)
			results <- discoveredServer{name: name, client: client, tools: tools, err: err}
		}(name)
	}
	wg.Wait()
	close(results)
	var collected []discoveredServer
	for result := range results {
		collected = append(collected, result)
	}
	sort.Slice(collected, func(i, j int) bool { return collected[i].name < collected[j].name })

	m.mu.Lock()
	used := map[string]bool{}
	for _, result := range collected {
		if result.err != nil {
			m.discoveryErrors = append(m.discoveryErrors, fmt.Errorf("MCP server %s: %w", result.name, result.err))
			continue
		}
		m.clients[result.name] = result.client
		for _, tool := range result.tools {
			exposed := exposedToolName(result.name, tool.Name)
			if used[exposed] {
				exposed = exposedToolName(result.name, tool.Name+"-"+shortHash(tool.Name))
			}
			used[exposed] = true
			m.bindings[exposed] = binding{ServerName: result.name, RemoteName: tool.Name, Tool: tool, Client: result.client, Exposed: exposed}
		}
	}
	m.discovered = true
	m.discovering = false
	close(done)
	errs := append([]error(nil), m.discoveryErrors...)
	m.mu.Unlock()
	return errs
}

func (m *Manager) connect(parent context.Context, name string, server config.MCPServerConfig) (*client, []Tool, error) {
	timeout := time.Duration(server.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	var wire transport
	var err error
	switch server.Transport {
	case "stdio":
		cwd := server.Cwd
		if cwd == "" {
			cwd = m.workspace
		} else if !filepath.IsAbs(cwd) {
			cwd = filepath.Join(m.workspace, cwd)
		}
		wire, err = newStdioTransport(server.Command, server.Args, cwd, server.Env)
	case "http":
		wire, err = newHTTPTransport(server.URL, server.Headers, timeout)
	default:
		err = fmt.Errorf("unsupported transport %q", server.Transport)
	}
	if err != nil {
		return nil, nil, err
	}
	client := newClient(wire)
	if err := client.Initialize(ctx); err != nil {
		_ = wire.Close()
		return nil, nil, err
	}
	tools, err := client.ListTools(ctx)
	if err != nil {
		_ = client.Close()
		return nil, nil, err
	}
	return client, tools, nil
}

func (m *Manager) ToolSpecs() []llm.ToolSpec {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.bindings))
	for name := range m.bindings {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]llm.ToolSpec, 0, len(names))
	for _, name := range names {
		item := m.bindings[name]
		description := strings.TrimSpace(item.Tool.Description)
		if description == "" {
			description = firstNonEmpty(item.Tool.Title, item.RemoteName)
		}
		out = append(out, llm.ToolSpec{Name: name, Description: fmt.Sprintf("[MCP %s] %s", item.ServerName, description), Parameters: providerSchema(item.Tool.InputSchema)})
	}
	return out
}

func (m *Manager) HasTool(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.bindings[name]
	return ok
}
func (m *Manager) ReadOnly(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	item, ok := m.bindings[name]
	return ok && item.Tool.Annotations.ReadOnlyHint && !item.Tool.Annotations.DestructiveHint
}
func (m *Manager) Destructive(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	item, ok := m.bindings[name]
	return ok && item.Tool.Annotations.DestructiveHint
}
func (m *Manager) Validate(name string, arguments map[string]any) error {
	m.mu.RLock()
	item, ok := m.bindings[name]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("unknown MCP tool %q", name)
	}
	return validateArguments(item.Tool.InputSchema, arguments)
}

func (m *Manager) Call(ctx context.Context, name string, arguments map[string]any) Result {
	m.mu.RLock()
	item, ok := m.bindings[name]
	m.mu.RUnlock()
	if !ok {
		return Result{OK: false, Error: fmt.Sprintf("unknown MCP tool %q", name)}
	}
	if err := validateArguments(item.Tool.InputSchema, arguments); err != nil {
		return Result{OK: false, Error: err.Error()}
	}
	result, err := item.Client.CallTool(ctx, item.RemoteName, arguments)
	reconnected := false
	if err != nil && ctx.Err() == nil {
		refreshed, reconnectErr := m.reconnect(ctx, name, item.Client)
		if reconnectErr == nil {
			item = refreshed
			result, err = item.Client.CallTool(ctx, item.RemoteName, arguments)
			reconnected = err == nil
		} else {
			err = fmt.Errorf("%v; reconnect failed: %w", err, reconnectErr)
		}
	}
	if err != nil {
		return Result{OK: false, Error: err.Error(), Metadata: m.metadata(item)}
	}
	output := flattenResult(result)
	runes := []rune(output)
	if len(runes) > m.maxOutputChars {
		output = string(runes[:m.maxOutputChars]) + "\n\n[MCP result truncated]"
	}
	metadata := m.metadata(item)
	metadata["structured"] = len(result.StructuredContent) > 0
	metadata["reconnected"] = reconnected
	if result.IsError {
		return Result{OK: false, Error: output, Metadata: metadata}
	}
	return Result{OK: true, Output: output, Metadata: metadata}
}

func (m *Manager) reconnect(ctx context.Context, exposed string, failed *client) (binding, error) {
	m.reconnectMu.Lock()
	defer m.reconnectMu.Unlock()
	m.mu.RLock()
	current, ok := m.bindings[exposed]
	m.mu.RUnlock()
	if !ok {
		return binding{}, fmt.Errorf("MCP tool %q disappeared during reconnect", exposed)
	}
	if current.Client != failed {
		return current, nil
	}
	server, ok := m.configs[current.ServerName]
	if !ok || server.Disabled {
		return binding{}, fmt.Errorf("MCP server %q is not configured", current.ServerName)
	}
	newClient, tools, err := m.connect(ctx, current.ServerName, server)
	if err != nil {
		return binding{}, err
	}
	remote := make(map[string]Tool, len(tools))
	for _, tool := range tools {
		remote[tool.Name] = tool
	}
	m.mu.Lock()
	old := m.clients[current.ServerName]
	m.clients[current.ServerName] = newClient
	for name, item := range m.bindings {
		if item.ServerName != current.ServerName {
			continue
		}
		tool, exists := remote[item.RemoteName]
		if !exists {
			delete(m.bindings, name)
			continue
		}
		item.Client, item.Tool = newClient, tool
		m.bindings[name] = item
		delete(remote, item.RemoteName)
	}
	used := map[string]bool{}
	for name := range m.bindings {
		used[name] = true
	}
	for remoteName, tool := range remote {
		name := exposedToolName(current.ServerName, remoteName)
		if used[name] {
			name = exposedToolName(current.ServerName, remoteName+"-"+shortHash(remoteName))
		}
		m.bindings[name] = binding{ServerName: current.ServerName, RemoteName: remoteName, Tool: tool, Client: newClient, Exposed: name}
	}
	refreshed, exists := m.bindings[exposed]
	m.mu.Unlock()
	if old != nil && old != newClient {
		_ = old.Close()
	}
	if !exists {
		_ = newClient.Close()
		return binding{}, fmt.Errorf("MCP tool %q is no longer advertised", exposed)
	}
	return refreshed, nil
}

func (m *Manager) metadata(item binding) map[string]any {
	return map[string]any{"mcp": true, "mcp_server": item.ServerName, "mcp_tool": item.RemoteName, "read_only": item.Tool.Annotations.ReadOnlyHint, "destructive": item.Tool.Annotations.DestructiveHint}
}
func (m *Manager) Errors() []error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]error(nil), m.discoveryErrors...)
}
func (m *Manager) Close() error {
	m.mu.Lock()
	clients := make([]*client, 0, len(m.clients))
	for _, c := range m.clients {
		clients = append(clients, c)
	}
	m.clients = map[string]*client{}
	m.mu.Unlock()
	var first error
	for _, c := range clients {
		if err := c.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func flattenResult(result callToolResult) string {
	var parts []string
	for _, item := range result.Content {
		switch item.Type {
		case "text":
			parts = append(parts, item.Text)
		case "resource_link":
			parts = append(parts, firstNonEmpty(item.Name, item.URI))
		case "resource":
			parts = append(parts, string(item.Resource))
		case "image", "audio":
			parts = append(parts, fmt.Sprintf("[%s %s; %d encoded bytes]", item.Type, item.MIMEType, len(item.Data)))
		default:
			if item.Text != "" {
				parts = append(parts, item.Text)
			}
		}
	}
	if len(result.StructuredContent) > 0 {
		if data, err := json.MarshalIndent(result.StructuredContent, "", "  "); err == nil {
			parts = append(parts, string(data))
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

var invalidToolName = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func exposedToolName(server, tool string) string {
	name := "mcp__" + invalidToolName.ReplaceAllString(strings.ToLower(server), "_") + "__" + invalidToolName.ReplaceAllString(strings.ToLower(tool), "_")
	name = strings.Trim(name, "_")
	if len(name) > 64 {
		name = name[:51] + "_" + shortHash(name)
	}
	return name
}
func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:6])
}
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
