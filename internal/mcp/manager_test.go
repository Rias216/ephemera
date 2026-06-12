package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ephemera-ai/ephemera/internal/config"
)

func TestStdioManagerDiscoversValidatesAndCallsTool(t *testing.T) {
	manager := NewManager(map[string]config.MCPServerConfig{
		"demo": {Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=TestMCPHelperProcess"}, Env: map[string]string{"GO_WANT_MCP_HELPER": "1"}, TimeoutSec: 10},
	}, t.TempDir(), 4_000)
	if errs := manager.Discover(t.Context()); len(errs) != 0 {
		t.Fatalf("discover errors = %v", errs)
	}
	defer manager.Close()

	specs := manager.ToolSpecs()
	if len(specs) != 1 || specs[0].Name != "mcp__demo__echo" {
		t.Fatalf("specs = %#v", specs)
	}
	if !manager.ReadOnly(specs[0].Name) {
		t.Fatal("readOnlyHint was not preserved")
	}
	if err := manager.Validate(specs[0].Name, map[string]any{}); err == nil {
		t.Fatal("missing required argument was accepted")
	}
	result := manager.Call(t.Context(), specs[0].Name, map[string]any{"message": "frontier"})
	if !result.OK || !strings.Contains(result.Output, "echo: frontier") {
		t.Fatalf("result = %#v", result)
	}
	if result.Metadata["mcp_server"] != "demo" || result.Metadata["mcp_tool"] != "echo" {
		t.Fatalf("metadata = %#v", result.Metadata)
	}
}

func TestStdioTransportCorrelatesConcurrentResponses(t *testing.T) {
	manager := NewManager(map[string]config.MCPServerConfig{
		"demo": {Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=TestMCPHelperProcess"}, Env: map[string]string{"GO_WANT_MCP_HELPER": "1", "MCP_REVERSE_RESPONSES": "1"}, TimeoutSec: 10},
	}, t.TempDir(), 4_000)
	if errs := manager.Discover(t.Context()); len(errs) != 0 {
		t.Fatalf("discover errors = %v", errs)
	}
	defer manager.Close()

	var wg sync.WaitGroup
	outputs := make([]string, 2)
	for index, message := range []string{"slow", "fast"} {
		index, message := index, message
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := manager.Call(t.Context(), "mcp__demo__echo", map[string]any{"message": message})
			if !result.OK {
				t.Errorf("call %s failed: %#v", message, result)
			}
			outputs[index] = result.Output
		}()
	}
	wg.Wait()
	if outputs[0] != "echo: slow" || outputs[1] != "echo: fast" {
		t.Fatalf("outputs = %#v", outputs)
	}
}

func TestStreamableHTTPManagerSupportsSessionAndSSE(t *testing.T) {
	var mu sync.Mutex
	initialized := false
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			mu.Lock()
			deleted = true
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var request rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.Method != "initialize" && (r.Header.Get("MCP-Session-Id") != "session-1" || r.Header.Get("MCP-Protocol-Version") != ProtocolVersion) {
			t.Errorf("missing MCP session/version headers: %#v", r.Header)
		}
		switch request.Method {
		case "initialize":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("MCP-Session-Id", "session-1")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"%s","capabilities":{"tools":{}},"serverInfo":{"name":"http-test","version":"1"}}}`, request.ID, ProtocolVersion)
		case "notifications/initialized":
			mu.Lock()
			initialized = true
			mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"sum","description":"sum values","inputSchema":{"type":"object","properties":{"a":{"type":"integer"},"b":{"type":"integer"}},"required":["a","b"],"additionalProperties":false}}]}}`, request.ID)
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n")
			fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"3\"}],\"structuredContent\":{\"value\":3}}}\n\n", request.ID)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	manager := NewManager(map[string]config.MCPServerConfig{"remote": {Transport: "http", URL: server.URL, TimeoutSec: 10}}, t.TempDir(), 4_000)
	if errs := manager.Discover(t.Context()); len(errs) != 0 {
		t.Fatalf("discover errors = %v", errs)
	}
	result := manager.Call(t.Context(), "mcp__remote__sum", map[string]any{"a": 1, "b": 2})
	if !result.OK || !strings.Contains(result.Output, `"value": 3`) {
		t.Fatalf("result = %#v", result)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !initialized || !deleted {
		t.Fatalf("initialized=%t deleted=%t", initialized, deleted)
	}
}

func TestManagerReconnectsAndRetriesAfterTransportFailure(t *testing.T) {
	var initializeCalls atomic.Int32
	var toolCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var request rpcRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch request.Method {
		case "initialize":
			count := initializeCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("MCP-Session-Id", fmt.Sprintf("session-%d", count))
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"%s","capabilities":{"tools":{}},"serverInfo":{"name":"reconnect-test","version":"1"}}}`, request.ID, ProtocolVersion)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"ping","inputSchema":{"type":"object","properties":{},"additionalProperties":false},"annotations":{"readOnlyHint":true}}]}}`, request.ID)
		case "tools/call":
			if toolCalls.Add(1) == 1 {
				http.Error(w, "connection reset", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"pong"}]}}`, request.ID)
		}
	}))
	defer server.Close()

	manager := NewManager(map[string]config.MCPServerConfig{"recovering": {Transport: "http", URL: server.URL, TimeoutSec: 10}}, t.TempDir(), 4_000)
	if errs := manager.Discover(t.Context()); len(errs) != 0 {
		t.Fatalf("discover errors = %v", errs)
	}
	defer manager.Close()
	result := manager.Call(t.Context(), "mcp__recovering__ping", map[string]any{})
	if !result.OK || result.Output != "pong" || result.Metadata["reconnected"] != true {
		t.Fatalf("result = %#v", result)
	}
	if initializeCalls.Load() != 2 || toolCalls.Load() != 2 {
		t.Fatalf("initialize calls=%d tool calls=%d", initializeCalls.Load(), toolCalls.Load())
	}
}

func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	var writeMu sync.Mutex
	var calls sync.WaitGroup
	for scanner.Scan() {
		var request rpcRequest
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		switch request.Method {
		case "initialize":
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"protocolVersion": ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "stdio-test", "version": "1"}}})
		case "notifications/initialized":
		case "tools/list":
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"tools": []any{map[string]any{"name": "echo", "description": "echo a message", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"message": map[string]any{"type": "string"}}, "required": []string{"message"}, "additionalProperties": false}, "annotations": map[string]any{"readOnlyHint": true}}}}})
		case "tools/call":
			params, _ := request.Params.(map[string]any)
			arguments, _ := params["arguments"].(map[string]any)
			message := fmt.Sprint(arguments["message"])
			write := func() {
				writeMu.Lock()
				defer writeMu.Unlock()
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "echo: " + message}}, "isError": false}})
			}
			if os.Getenv("MCP_REVERSE_RESPONSES") == "1" {
				calls.Add(1)
				go func(message string) {
					defer calls.Done()
					if message == "slow" {
						time.Sleep(120 * time.Millisecond)
					} else {
						time.Sleep(10 * time.Millisecond)
					}
					write()
				}(message)
			} else {
				write()
			}
		}
	}
	calls.Wait()
	os.Exit(0)
}
