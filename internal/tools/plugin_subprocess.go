package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type subprocessPlugin struct {
	manifest  PluginManifest
	workspace string
	timeout   time.Duration

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	stderr  synchronizedBuffer
	counter atomic.Uint64
	closed  bool
}

func newSubprocessPlugin(ctx context.Context, manifest PluginManifest, workspace string, timeout time.Duration) (*subprocessPlugin, error) {
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	plugin := &subprocessPlugin{manifest: manifest, workspace: workspace, timeout: timeout}
	if err := plugin.start(ctx); err != nil {
		return nil, err
	}
	return plugin, nil
}

func (plugin *subprocessPlugin) start(ctx context.Context) error {
	command := resolvePluginCommand(plugin.manifest)
	args := make([]string, len(plugin.manifest.Args))
	for index, value := range plugin.manifest.Args {
		args[index] = expandPluginValue(value)
	}
	cmd := exec.Command(command, args...)
	cwd := strings.TrimSpace(plugin.manifest.Cwd)
	if cwd == "" {
		cwd = plugin.workspace
	} else if !filepath.IsAbs(cwd) {
		cwd = filepath.Join(filepath.Dir(plugin.manifest.manifestPath), cwd)
	}
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	for key, value := range plugin.manifest.Env {
		cmd.Env = append(cmd.Env, key+"="+expandPluginValue(value))
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return err
	}
	cmd.Stderr = &plugin.stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return err
	}
	plugin.cmd = cmd
	plugin.stdin = stdin
	plugin.stdout = bufio.NewReader(stdout)

	initCtx, cancel := context.WithTimeout(ctx, minDuration(plugin.timeout, 15*time.Second))
	defer cancel()
	var initialized pluginInitializeResult
	if err := plugin.requestLocked(initCtx, "initialize", map[string]any{
		"protocol_version": PluginProtocolVersion,
		"workspace":        plugin.workspace,
		"host":             map[string]any{"name": "ephemera", "os": runtime.GOOS, "arch": runtime.GOARCH},
	}, &initialized); err != nil {
		_ = plugin.closeLocked()
		return fmt.Errorf("initialize plugin %s: %w", plugin.manifest.Name, err)
	}
	if initialized.ProtocolVersion != PluginProtocolVersion {
		_ = plugin.closeLocked()
		return fmt.Errorf("plugin %s negotiated unsupported protocol %q", plugin.manifest.Name, initialized.ProtocolVersion)
	}
	return nil
}

func (plugin *subprocessPlugin) call(ctx context.Context, tool string, arguments map[string]any, emit func(string)) Result {
	plugin.mu.Lock()
	defer plugin.mu.Unlock()
	if plugin.closed {
		return fail(tool, "plugin process is closed")
	}
	callCtx, cancel := context.WithTimeout(ctx, plugin.timeout)
	defer cancel()
	var response pluginCallResult
	err := plugin.requestLocked(callCtx, "call", map[string]any{"tool": tool, "arguments": arguments}, &response)
	if err != nil {
		message := err.Error()
		if stderr := strings.TrimSpace(plugin.stderr.String()); stderr != "" {
			message += "; stderr: " + compactPluginText(stderr, 2048)
		}
		return fail(tool, message)
	}
	if emit != nil && response.Output != "" {
		emit(response.Output)
	}
	return Result{Tool: tool, OK: response.OK, Output: response.Output, Error: response.Error, Metadata: response.Metadata}
}

func (plugin *subprocessPlugin) requestLocked(ctx context.Context, method string, params map[string]any, out any) error {
	id := fmt.Sprintf("%s-%d", plugin.manifest.Name, plugin.counter.Add(1))
	request := pluginRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = deadline // pipes cannot set deadlines portably; cancellation kills the process below.
	}
	if _, err := plugin.stdin.Write(append(data, '\n')); err != nil {
		return err
	}
	type readResult struct {
		line []byte
		err  error
	}
	read := make(chan readResult, 1)
	go func() {
		line, err := plugin.stdout.ReadBytes('\n')
		read <- readResult{line: line, err: err}
	}()
	select {
	case <-ctx.Done():
		_ = plugin.closeLocked()
		return ctx.Err()
	case item := <-read:
		if item.err != nil {
			return item.err
		}
		var response pluginResponse
		if err := json.Unmarshal(item.line, &response); err != nil {
			return fmt.Errorf("invalid JSON response: %w", err)
		}
		if response.JSONRPC != "2.0" {
			return fmt.Errorf("response uses unsupported jsonrpc version %q", response.JSONRPC)
		}
		if response.ID != id {
			return fmt.Errorf("response id %q does not match request %q", response.ID, id)
		}
		if response.Error != nil {
			return fmt.Errorf("plugin RPC %d: %s", response.Error.Code, response.Error.Message)
		}
		if out != nil && len(response.Result) > 0 {
			if err := json.Unmarshal(response.Result, out); err != nil {
				return fmt.Errorf("decode plugin result: %w", err)
			}
		}
		return nil
	}
}

func (plugin *subprocessPlugin) Close() error {
	plugin.mu.Lock()
	defer plugin.mu.Unlock()
	return plugin.closeLocked()
}

func (plugin *subprocessPlugin) closeLocked() error {
	if plugin.closed {
		return nil
	}
	plugin.closed = true
	if plugin.stdin != nil {
		_ = plugin.stdin.Close()
	}
	if plugin.cmd == nil || plugin.cmd.Process == nil {
		return nil
	}
	_ = plugin.cmd.Process.Kill()
	_ = plugin.cmd.Wait()
	return nil
}

func (r Registry) LoadPluginManifest(ctx context.Context, path string) error {
	manifest, err := ReadPluginManifest(path)
	if err != nil {
		return err
	}
	for _, declaration := range manifest.Tools {
		name := strings.TrimSpace(declaration.Name)
		if existing, exists := r.Lookup(name); exists {
			source, _ := existing.ProviderHints["source"].(string)
			return fmt.Errorf("register plugin %s tool %s: name collides with existing %s tool", manifest.Name, name, firstPluginValue(source, "built-in"))
		}
	}
	plugin, err := newSubprocessPlugin(ctx, manifest, r.WorkspaceRoot, r.CommandTimeout)
	if err != nil {
		return err
	}
	registered := make([]string, 0, len(manifest.Tools))
	rollback := func() {
		for index := len(registered) - 1; index >= 0; index-- {
			r.Unregister(registered[index])
		}
		_ = plugin.Close()
	}
	for _, declaration := range manifest.Tools {
		declaration := declaration
		name := strings.TrimSpace(declaration.Name)
		definition := Tool{
			Name:        name,
			Description: declaration.Description,
			Risk:        declaration.Risk,
			Parameters:  declaration.Parameters,
			Version:     firstPluginValue(declaration.Version, manifest.Version, "1.0.0"),
			ProviderHints: map[string]any{
				"source": "plugin", "plugin": manifest.Name,
			},
			Execute: func(ctx context.Context, _ Registry, call Call, emit func(string)) Result {
				return plugin.call(ctx, name, call.Arguments, emit)
			},
		}
		if err := r.Register(definition); err != nil {
			rollback()
			return fmt.Errorf("register plugin %s tool %s: %w", manifest.Name, name, err)
		}
		registered = append(registered, name)
	}
	if len(registered) == 0 {
		rollback()
		return fmt.Errorf("plugin %s registered no tools", manifest.Name)
	}
	r.resources.add(plugin)
	return nil
}

func (r Registry) LoadConfiguredPlugins(ctx context.Context, directories, explicit []string) []error {
	explicit = append(append([]string(nil), explicit...), queuedPluginManifests()...)
	paths, errs := DiscoverPluginManifests(r.WorkspaceRoot, directories, explicit)
	for _, path := range paths {
		if err := r.LoadPluginManifest(ctx, path); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

func expandPluginValue(value string) string { return os.ExpandEnv(strings.TrimSpace(value)) }

func resolvePluginCommand(manifest PluginManifest) string {
	command := expandPluginValue(manifest.Command)
	if filepath.IsAbs(command) || (!strings.Contains(command, "/") && !strings.Contains(command, `\`)) {
		return command
	}
	return filepath.Clean(filepath.Join(filepath.Dir(manifest.manifestPath), command))
}

func compactPluginText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

func firstPluginValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
