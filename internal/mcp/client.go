package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
)

type transport interface {
	Send(context.Context, rpcRequest, bool) (rpcResponse, error)
	SetProtocolVersion(string)
	Close() error
}

type client struct {
	transport transport
	nextID    atomic.Int64
	server    initializeResult
}

func newClient(wire transport) *client { return &client{transport: wire} }

func (c *client) request(ctx context.Context, method string, params any, target any) error {
	id := c.nextID.Add(1)
	response, err := c.transport.Send(ctx, rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}, true)
	if err != nil {
		return err
	}
	if response.Error != nil {
		return response.Error
	}
	if target == nil || len(response.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(response.Result, target); err != nil {
		return fmt.Errorf("decode MCP %s result: %w", method, err)
	}
	return nil
}

func (c *client) notify(ctx context.Context, method string, params any) error {
	_, err := c.transport.Send(ctx, rpcRequest{JSONRPC: "2.0", Method: method, Params: params}, false)
	return err
}

func (c *client) Initialize(ctx context.Context) error {
	var result initializeResult
	if err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      implementation{Name: "ephemera", Title: "Ephemera", Version: "0.1.0", Description: "Local autonomous coding agent"},
	}, &result); err != nil {
		return err
	}
	if !supportedProtocolVersions[result.ProtocolVersion] {
		return fmt.Errorf("MCP server selected unsupported protocol version %q", result.ProtocolVersion)
	}
	c.server = result
	c.transport.SetProtocolVersion(result.ProtocolVersion)
	return c.notify(ctx, "notifications/initialized", nil)
}

func (c *client) ListTools(ctx context.Context) ([]Tool, error) {
	var out []Tool
	cursor := ""
	for pages := 0; pages < 100; pages++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var result listToolsResult
		if err := c.request(ctx, "tools/list", params, &result); err != nil {
			return nil, err
		}
		out = append(out, result.Tools...)
		if result.NextCursor == "" {
			return out, nil
		}
		cursor = result.NextCursor
	}
	return nil, fmt.Errorf("MCP tools/list exceeded 100 pages")
}

func (c *client) CallTool(ctx context.Context, name string, arguments map[string]any) (callToolResult, error) {
	var result callToolResult
	err := c.request(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments}, &result)
	return result, err
}

func (c *client) Close() error { return c.transport.Close() }
