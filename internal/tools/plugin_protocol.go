package tools

import "encoding/json"

const PluginProtocolVersion = "1.0"

type pluginRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      string         `json:"id"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params,omitempty"`
}

type pluginResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *pluginRPCError `json:"error,omitempty"`
}

type pluginRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type pluginInitializeResult struct {
	ProtocolVersion string `json:"protocol_version"`
	Name            string `json:"name,omitempty"`
	Version         string `json:"version,omitempty"`
}

type pluginCallResult struct {
	OK       bool           `json:"ok"`
	Output   string         `json:"output,omitempty"`
	Error    string         `json:"error,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}
