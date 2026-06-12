package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ephemera-ai/ephemera/internal/config"
)

func TestSubprocessPluginRegistersAndExecutesThroughMiddleware(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, "echo-plugin.json")
	manifest := PluginManifest{
		SchemaVersion: PluginProtocolVersion,
		Name:          "test-echo",
		Version:       "1.2.3",
		Command:       os.Args[0],
		Args:          []string{"-test.run=TestPluginHelperProcess"},
		Env:           map[string]string{"EPHEMERA_PLUGIN_HELPER": "1"},
		Tools: []PluginToolManifest{{
			Name:        "plugin_echo",
			Description: "Echo a value from a subprocess plugin.",
			Risk:        RiskRead,
			Parameters: ToolSchema{
				Type: "object",
				Properties: map[string]ToolProperty{
					"value": {Type: "string", Description: "Value to echo."},
				},
				Required:             []string{"value"},
				AdditionalProperties: false,
			},
		}},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.WorkspaceRoot = workspace
	cfg.ApprovalPolicy = config.ApprovalAutoApprove
	cfg.PluginManifests = []string{manifestPath}
	registry := NewRegistry(cfg)
	defer registry.Close()

	definition, ok := registry.Lookup("plugin_echo")
	if !ok {
		t.Fatal("plugin tool was not registered")
	}
	if definition.ProviderHints["source"] != "plugin" || definition.Version != "1.2.3" {
		t.Fatalf("unexpected plugin definition: %#v", definition)
	}

	result := registry.Execute(context.Background(), Call{Name: "plugin_echo", Arguments: map[string]any{"value": "hello"}})
	if !result.OK || result.Output != "echo:hello" {
		t.Fatalf("plugin execution result = %#v", result)
	}
	if result.Metadata["risk"] != string(RiskRead) || result.Metadata["plugin_helper"] != true {
		t.Fatalf("plugin did not pass through shared middleware: %#v", result.Metadata)
	}
}

func TestPluginHelperProcess(t *testing.T) {
	if os.Getenv("EPHEMERA_PLUGIN_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request pluginRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			os.Exit(2)
		}
		var result any
		switch request.Method {
		case "initialize":
			result = pluginInitializeResult{ProtocolVersion: PluginProtocolVersion, Name: "test-echo", Version: "1.2.3"}
		case "call":
			arguments, _ := request.Params["arguments"].(map[string]any)
			result = pluginCallResult{OK: true, Output: fmt.Sprintf("echo:%v", arguments["value"]), Metadata: map[string]any{"plugin_helper": true}}
		default:
			_ = encoder.Encode(pluginResponse{JSONRPC: "2.0", ID: request.ID, Error: &pluginRPCError{Code: -32601, Message: "method not found"}})
			continue
		}
		raw, err := json.Marshal(result)
		if err != nil {
			os.Exit(3)
		}
		if err := encoder.Encode(pluginResponse{JSONRPC: "2.0", ID: request.ID, Result: raw}); err != nil {
			os.Exit(4)
		}
	}
	os.Exit(0)
}

func TestPluginManifestPathIsAbsoluteAndCollisionFailsBeforeStartup(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, "collision.json")
	manifest := PluginManifest{
		SchemaVersion: PluginProtocolVersion,
		Name:          "collision",
		Version:       "1.0.0",
		Command:       filepath.Join(workspace, "must-not-start"),
		Tools: []PluginToolManifest{{
			Name:        "read_file",
			Description: "This must not replace the built-in tool.",
			Risk:        RiskRead,
			Parameters:  ToolSchema{Type: "object", AdditionalProperties: false},
		}},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	loaded, err := ReadPluginManifest("collision.json")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(loaded.manifestPath) {
		t.Fatalf("manifest path is not absolute: %q", loaded.manifestPath)
	}
	cfg := config.Default()
	cfg.WorkspaceRoot = workspace
	registry := NewRegistry(cfg)
	defer registry.Close()
	err = registry.LoadPluginManifest(context.Background(), loaded.manifestPath)
	if err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("collision error = %v", err)
	}
}
