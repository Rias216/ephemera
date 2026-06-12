package llm

import (
	"strings"
	"unicode/utf8"
)

// NormalizeRequestUTF8 returns a deep copy whose user/provider-visible string
// fields are valid UTF-8. This is required by process-backed providers such as
// Codex, which reject stdin containing arbitrary filesystem bytes.
func NormalizeRequestUTF8(req Request) (Request, int) {
	out := req
	replacements := 0
	out.Model = normalizeUTF8String(req.Model, &replacements)
	out.System = normalizeUTF8String(req.System, &replacements)
	out.ReasoningEffort = normalizeUTF8String(req.ReasoningEffort, &replacements)
	out.Messages = make([]Message, len(req.Messages))
	for index, message := range req.Messages {
		copyMessage := message
		copyMessage.Role = normalizeUTF8String(message.Role, &replacements)
		copyMessage.Content = normalizeUTF8String(message.Content, &replacements)
		copyMessage.ToolCalls = make([]ToolCall, len(message.ToolCalls))
		for toolIndex, call := range message.ToolCalls {
			copyCall := call
			copyCall.ID = normalizeUTF8String(call.ID, &replacements)
			copyCall.Name = normalizeUTF8String(call.Name, &replacements)
			copyCall.Arguments = normalizeUTF8Map(call.Arguments, &replacements)
			copyMessage.ToolCalls[toolIndex] = copyCall
		}
		if message.ToolResult != nil {
			result := *message.ToolResult
			result.ID = normalizeUTF8String(result.ID, &replacements)
			result.Name = normalizeUTF8String(result.Name, &replacements)
			result.Output = normalizeUTF8String(result.Output, &replacements)
			result.Error = normalizeUTF8String(result.Error, &replacements)
			result.Metadata = normalizeUTF8Map(result.Metadata, &replacements)
			copyMessage.ToolResult = &result
		}
		out.Messages[index] = copyMessage
	}
	return out, replacements
}

func normalizeUTF8String(value string, replacements *int) string {
	if utf8.ValidString(value) {
		return value
	}
	(*replacements)++
	return strings.ToValidUTF8(value, "�")
}

func normalizeUTF8Map(values map[string]any, replacements *int) map[string]any {
	if values == nil {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		normalizedKey := normalizeUTF8String(key, replacements)
		out[normalizedKey] = normalizeUTF8Value(value, replacements)
	}
	return out
}

func normalizeUTF8Value(value any, replacements *int) any {
	switch typed := value.(type) {
	case string:
		return normalizeUTF8String(typed, replacements)
	case map[string]any:
		return normalizeUTF8Map(typed, replacements)
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = normalizeUTF8Value(item, replacements)
		}
		return out
	case []string:
		out := make([]string, len(typed))
		for index, item := range typed {
			out[index] = normalizeUTF8String(item, replacements)
		}
		return out
	default:
		return value
	}
}
