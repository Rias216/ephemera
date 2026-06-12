package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ToolTransport identifies how a model requested local capabilities. Native is
// the provider's function/tool API. Portable is Ephemera's provider-neutral
// JSON gateway used when a model or compatible endpoint cannot reliably emit
// native tool calls. Text is a normal assistant response.
type ToolTransport string

const (
	ToolTransportText     ToolTransport = "text"
	ToolTransportNative   ToolTransport = "native"
	ToolTransportPortable ToolTransport = "portable"
)

// ToolProtocolError means the provider produced a tool call, but its transport
// or argument payload was malformed. No local tool has executed when this error
// is returned, so the agent may safely retry through the portable gateway.
type ToolProtocolError struct {
	Provider string
	Tool     string
	Raw      string
	Cause    error
}

func (e *ToolProtocolError) Error() string {
	provider := strings.TrimSpace(e.Provider)
	if provider == "" {
		provider = "provider"
	}
	tool := strings.TrimSpace(e.Tool)
	if tool == "" {
		tool = "unknown"
	}
	return fmt.Sprintf("%s returned invalid arguments for tool %q: %v", provider, tool, e.Cause)
}

func (e *ToolProtocolError) Unwrap() error { return e.Cause }

// IsToolProtocolError reports whether err is safe to recover by asking the
// model to resend the decision through Ephemera's portable tool gateway.
func IsToolProtocolError(err error) bool {
	var target *ToolProtocolError
	return errors.As(err, &target)
}

// IsTruncatedToolProtocolError reports a malformed native tool call whose JSON
// ended before the provider completed it. This class receives one fresh native
// retry before Ephemera falls back to the universal text gateway.
func IsTruncatedToolProtocolError(err error) bool {
	var target *ToolProtocolError
	if !errors.As(err, &target) {
		return false
	}
	return isUnexpectedJSONEnd(target.Cause)
}

func isUnexpectedJSONEnd(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "unexpected eof") || strings.Contains(text, "unexpected end of json")
}

// TruncatedToolDecisionError converts stream-level truncation metadata into a
// retryable protocol error before any local tool can execute.
func TruncatedToolDecisionError(provider string, decision ToolDecision) error {
	for _, call := range decision.ToolCalls {
		if call.Truncated {
			return newToolProtocolError(provider, call.Name, "", io.ErrUnexpectedEOF)
		}
	}
	return nil
}

// IsNativeToolCompatibilityError delegates transport compatibility detection
// to the active provider. A structurally malformed tool call is always safe to
// recover through the portable gateway because no local tool has executed.
func IsNativeToolCompatibilityError(provider Provider, err error) bool {
	if err == nil {
		return false
	}
	if IsToolProtocolError(err) {
		return true
	}
	classifier, ok := provider.(NativeToolCompatibilityClassifier)
	return ok && classifier.IsNativeToolCompatibilityError(err)
}

func newToolProtocolError(provider, tool, raw string, cause error) error {
	return &ToolProtocolError{Provider: provider, Tool: tool, Raw: boundedProtocolRaw(raw), Cause: cause}
}

func boundedProtocolRaw(raw string) string {
	runes := []rune(strings.TrimSpace(raw))
	if len(runes) <= 2048 {
		return string(runes)
	}
	return string(runes[:1024]) + "…" + string(runes[len(runes)-1024:])
}

// RepairTruncatedToolCall attempts a structural-only repair of a JSON tool
// argument object. It can close missing braces/brackets and remove trailing
// commas, but refuses unterminated strings or mismatched delimiters.
func RepairTruncatedToolCall(raw string) (string, bool) {
	text := normalizedToolArgumentText(raw)
	if text == "" || text == "null" {
		return "", false
	}
	candidate := firstJSONObject(text)
	if candidate == "" {
		candidate = text
	}
	candidate = removeTrailingJSONCommas(candidate)
	closed, ok := closeJSONStructures(candidate)
	if !ok || strings.TrimSpace(closed) == strings.TrimSpace(candidate) {
		return "", false
	}
	if _, err := decodeJSONObject(closed); err != nil {
		return "", false
	}
	return closed, true
}

// DecodeToolArgumentsLenient first performs normal decoding and then permits a
// structural-only truncation repair. It never completes an unterminated string.
func DecodeToolArgumentsLenient(raw string) (map[string]any, error) {
	object, _, _, err := decodeToolArgumentsForStream(raw)
	return object, err
}

// decodeToolArgumentsString is the compatibility decoder used by portable
// envelopes. The boolean reports whether any harmless normalization or repair
// was required.
func decodeToolArgumentsString(raw string) (map[string]any, bool, error) {
	object, repaired, _, err := decodeToolArgumentsForStream(raw)
	return object, repaired, err
}

// decodeToolArgumentsForStream additionally reports whether a missing
// structural closer was repaired. Stream adapters copy that bit to ToolCall so
// the local validator and timeline can retain transport provenance.
func decodeToolArgumentsForStream(raw string) (map[string]any, bool, bool, error) {
	text := normalizedToolArgumentText(raw)
	if text == "" || text == "null" {
		return map[string]any{}, false, false, nil
	}
	candidates := []string{text}
	if object := firstJSONObject(text); object != "" && object != text {
		candidates = append(candidates, object)
	}
	for _, candidate := range candidates {
		if object, err := decodeJSONObject(candidate); err == nil {
			return object, candidate != text, false, nil
		}
	}
	for _, candidate := range candidates {
		repaired := removeTrailingJSONCommas(candidate)
		if object, err := decodeJSONObject(repaired); err == nil {
			return object, true, false, nil
		}
		closed, ok := RepairTruncatedToolCall(repaired)
		if !ok {
			continue
		}
		object, err := decodeJSONObject(closed)
		if err == nil {
			return object, true, true, nil
		}
	}
	_, err := decodeJSONObject(text)
	return nil, false, false, err
}

func normalizedToolArgumentText(raw string) string {
	text := stripJSONFence(strings.TrimSpace(raw))
	var encoded string
	if json.Unmarshal([]byte(text), &encoded) == nil {
		text = strings.TrimSpace(encoded)
	}
	return text
}

func decodeJSONObject(text string) (map[string]any, error) {
	object := map[string]any{}
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(text)))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, err
	}
	return object, nil
}

func stripJSONFence(text string) string {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		if newline := strings.IndexByte(text, '\n'); newline >= 0 {
			text = text[newline+1:]
		}
		text = strings.TrimSuffix(strings.TrimSpace(text), "```")
	}
	return strings.TrimSpace(text)
}

func firstJSONObject(text string) string {
	start := strings.IndexByte(text, '{')
	if start < 0 {
		return ""
	}
	inString := false
	escaped := false
	depth := 0
	for index := start; index < len(text); index++ {
		char := text[index]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if char == '\\' {
				escaped = true
				continue
			}
			if char == '"' {
				inString = false
			}
			continue
		}
		switch char {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return strings.TrimSpace(text[start : index+1])
			}
		}
	}
	return strings.TrimSpace(text[start:])
}

func removeTrailingJSONCommas(text string) string {
	var out strings.Builder
	inString := false
	escaped := false
	for index := 0; index < len(text); index++ {
		char := text[index]
		if inString {
			out.WriteByte(char)
			if escaped {
				escaped = false
			} else if char == '\\' {
				escaped = true
			} else if char == '"' {
				inString = false
			}
			continue
		}
		if char == '"' {
			inString = true
			out.WriteByte(char)
			continue
		}
		if char == ',' {
			next := index + 1
			for next < len(text) && (text[next] == ' ' || text[next] == '\n' || text[next] == '\r' || text[next] == '\t') {
				next++
			}
			if next < len(text) && (text[next] == '}' || text[next] == ']') {
				continue
			}
		}
		out.WriteByte(char)
	}
	return out.String()
}

func closeJSONStructures(text string) (string, bool) {
	var stack []byte
	inString := false
	escaped := false
	for index := 0; index < len(text); index++ {
		char := text[index]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if char == '\\' {
				escaped = true
				continue
			}
			if char == '"' {
				inString = false
			}
			continue
		}
		switch char {
		case '"':
			inString = true
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}', ']':
			if len(stack) == 0 || stack[len(stack)-1] != char {
				return text, false
			}
			stack = stack[:len(stack)-1]
		}
	}
	if inString || escaped {
		return text, false
	}
	var out strings.Builder
	out.WriteString(strings.TrimSpace(text))
	for index := len(stack) - 1; index >= 0; index-- {
		out.WriteByte(stack[index])
	}
	return out.String(), true
}

// GeneratePortableToolDecision makes every Ephemera tool usable by models whose
// native function-calling implementation is missing, schema-limited, or emits
// malformed streamed arguments. The provider only needs ordinary text output.
func GeneratePortableToolDecision(ctx context.Context, provider Provider, req Request, specs []ToolSpec, failure error, onDelta DeltaFunc) (ToolDecision, error) {
	portable := req
	portable.System = strings.TrimSpace(req.System) + "\n\n" + portableToolInstructions(specs, failure)
	portable.Messages = portableTextMessages(req.Messages)
	text, err := GenerateStreaming(ctx, provider, portable, onDelta)
	if err != nil {
		return ToolDecision{}, err
	}
	decision, err := ParsePortableToolDecision(text)
	if err != nil {
		return ToolDecision{}, newToolProtocolError(provider.Name(), "portable_gateway", text, err)
	}
	decision.Transport = ToolTransportPortable
	return decision, nil
}

func portableTextMessages(messages []Message) []Message {
	out := make([]Message, 0, len(messages))
	for _, message := range messages {
		switch message.Role {
		case "assistant":
			content := strings.TrimSpace(message.Content)
			if len(message.ToolCalls) > 0 {
				var calls []string
				for _, call := range message.ToolCalls {
					calls = append(calls, fmt.Sprintf("%s %s", call.Name, toolArgumentsJSON(call.Arguments)))
				}
				content = strings.TrimSpace(content + "\n[previous tool calls] " + strings.Join(calls, "; "))
			}
			if content != "" {
				out = append(out, Message{Role: "assistant", Content: content})
			}
		case "tool":
			if message.ToolResult == nil {
				continue
			}
			result := *message.ToolResult
			out = append(out, Message{Role: "user", Content: fmt.Sprintf("[tool result: %s]\n%s", result.Name, toolResultContent(result))})
		default:
			out = append(out, Message{Role: message.Role, Content: message.Content})
		}
	}
	return out
}

func portableToolInstructions(specs []ToolSpec, failure error) string {
	catalog := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		catalog = append(catalog, map[string]any{
			"name":        spec.Name,
			"description": spec.Description,
			"parameters":  spec.Parameters,
		})
	}
	encoded, _ := json.Marshal(catalog)
	var b strings.Builder
	b.WriteString("UNIVERSAL TOOL GATEWAY — this section overrides native-tool instructions for this response.\n")
	b.WriteString("Every listed capability is available even when this provider's native tool API is not. Return exactly one JSON object and no Markdown:\n")
	b.WriteString(`{"text":"optional user-visible text","tool_calls":[{"id":"call_1","name":"tool_name","arguments":{}}]}`)
	b.WriteString("\nUse an empty tool_calls array only for a direct final answer. For workspace mutation, git, or verification requests, prose-only completion is invalid until the required tool calls have succeeded. Arguments must be one complete JSON object matching the selected schema. Never describe a tool call in prose.\n")
	b.WriteString("For large edits, prefer replace_in_file or one apply_patch call per file. apply_multi_patch remains available, but do not repeat a payload that was truncated.\n")
	if failure != nil {
		b.WriteString("The previous native transport failed before any tool executed: ")
		b.WriteString(compactProtocolText(failure.Error(), 320))
		b.WriteString("\nResend the intended action through this gateway, using a smaller equivalent call when necessary.\n")
	}
	b.WriteString("TOOL CATALOG JSON:\n")
	b.Write(encoded)
	return b.String()
}

// mergeStreamFragment accepts both true deltas and cumulative/replayed chunks.
// Several OpenAI-compatible servers resend the complete function name or JSON
// prefix on each event. Blind concatenation turns valid calls into names such
// as read_fileread_file or duplicated JSON. The longest suffix/prefix overlap
// preserves genuine deltas without duplicating retransmitted content.
func mergeStreamFragment(current, incoming string) string {
	if incoming == "" {
		return current
	}
	if current == "" {
		return incoming
	}
	if incoming == current || strings.HasPrefix(current, incoming) {
		return current
	}
	if strings.HasPrefix(incoming, current) {
		return incoming
	}
	maxOverlap := len(current)
	if len(incoming) < maxOverlap {
		maxOverlap = len(incoming)
	}
	for overlap := maxOverlap; overlap > 0; overlap-- {
		if strings.HasSuffix(current, incoming[:overlap]) {
			return current + incoming[overlap:]
		}
	}
	return current + incoming
}

func compactProtocolText(text string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max-1]) + "…"
}

// ParsePortableToolDecision accepts the canonical gateway envelope plus common
// OpenAI-style and Ephemera action variants so weaker models are not locked out
// by superficial formatting differences.
func ParsePortableToolDecision(text string) (ToolDecision, error) {
	raw := stripJSONFence(strings.TrimSpace(text))
	if raw == "" {
		return ToolDecision{}, fmt.Errorf("empty response")
	}
	candidate := firstJSONObject(raw)
	if candidate == "" {
		return ToolDecision{Text: strings.TrimSpace(text), Transport: ToolTransportText}, nil
	}
	object, _, err := decodeToolArgumentsString(candidate)
	if err != nil {
		return ToolDecision{}, err
	}
	visible := firstString(object, "text", "final", "answer", "summary")
	calls, err := portableCalls(object)
	if err != nil {
		return ToolDecision{}, err
	}
	if visible == "" && len(calls) == 0 {
		return ToolDecision{}, fmt.Errorf("JSON contained neither text nor tool calls")
	}
	return ToolDecision{Text: visible, ToolCalls: calls, Transport: ToolTransportPortable}, nil
}

func portableCalls(object map[string]any) ([]ToolCall, error) {
	values, found := firstSlice(object, "tool_calls", "calls", "actions")
	if !found {
		if firstString(object, "name", "tool") != "" {
			values = []any{object}
		} else {
			return nil, nil
		}
	}
	calls := make([]ToolCall, 0, len(values))
	for index, value := range values {
		entry, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("tool call %d is not an object", index+1)
		}
		name := firstString(entry, "name", "tool")
		id := firstString(entry, "id", "tool_call_id", "call_id")
		argumentsValue, hasArguments := entry["arguments"]
		if function, ok := entry["function"].(map[string]any); ok {
			if name == "" {
				name = firstString(function, "name")
			}
			if !hasArguments {
				argumentsValue, hasArguments = function["arguments"]
			}
		}
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("tool call %d has no name", index+1)
		}
		arguments := map[string]any{}
		if hasArguments && argumentsValue != nil {
			switch typed := argumentsValue.(type) {
			case map[string]any:
				arguments = typed
			case string:
				decoded, _, decodeErr := decodeToolArgumentsString(typed)
				if decodeErr != nil {
					return nil, fmt.Errorf("tool call %d (%s) arguments: %w", index+1, name, decodeErr)
				}
				arguments = decoded
			default:
				encoded, _ := json.Marshal(typed)
				decoded, _, decodeErr := decodeToolArgumentsString(string(encoded))
				if decodeErr != nil {
					return nil, fmt.Errorf("tool call %d (%s) arguments: %w", index+1, name, decodeErr)
				}
				arguments = decoded
			}
		}
		if id == "" {
			id = fmt.Sprintf("portable_call_%d_%s", index+1, strings.ReplaceAll(name, "-", "_"))
		}
		calls = append(calls, ToolCall{ID: id, Name: name, Arguments: arguments})
	}
	return calls, nil
}

func firstString(object map[string]any, names ...string) string {
	for _, name := range names {
		if value, ok := object[name].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstSlice(object map[string]any, names ...string) ([]any, bool) {
	for _, name := range names {
		if value, ok := object[name].([]any); ok {
			return value, true
		}
	}
	return nil, false
}

func decodeRawToolArguments(raw json.RawMessage) (map[string]any, bool, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, false, nil
	}
	return decodeToolArgumentsString(string(raw))
}
