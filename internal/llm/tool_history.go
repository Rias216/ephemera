package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

func toolArgumentsJSON(arguments map[string]any) string {
	if arguments == nil {
		return "{}"
	}
	data, err := json.Marshal(arguments)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func toolResultContent(result ToolResult) string {
	payload := map[string]any{
		"ok": result.OK,
	}
	if strings.TrimSpace(result.Output) != "" {
		payload["output"] = result.Output
	}
	if strings.TrimSpace(result.Error) != "" {
		payload["error"] = result.Error
	}
	if metadata := providerVisibleMetadata(result.Metadata); len(metadata) > 0 {
		payload["metadata"] = metadata
	}
	data, err := json.Marshal(payload)
	if err != nil {
		if result.OK {
			return firstNonEmpty(result.Output, "tool completed successfully with no output")
		}
		return firstNonEmpty(result.Error, result.Output, "tool failed without an error message")
	}
	return string(data)
}

func stableToolCallID(call ToolCall, index int) string {
	if value := strings.TrimSpace(call.ID); value != "" {
		return value
	}
	return fmt.Sprintf("ephemera_call_%d_%s", index+1, strings.ReplaceAll(call.Name, "-", "_"))
}

func providerVisibleMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	hidden := map[string]bool{
		"approval_event_id": true,
		"call_event_id":     true,
		"call_fingerprint":  true,
		"provider_call_id":  true,
		"tool_arguments":    true,
	}
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		if !hidden[key] {
			out[key] = value
		}
	}
	return out
}

func hasNativeToolHistory(messages []Message) bool {
	for _, message := range messages {
		if len(message.ToolCalls) > 0 || message.ToolResult != nil || message.Role == "tool" {
			return true
		}
	}
	return false
}
