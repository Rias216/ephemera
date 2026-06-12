package agent

import (
	"encoding/json"
	"fmt"
	"github.com/ephemera-ai/ephemera/internal/debuglog"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/llm"
	"github.com/ephemera-ai/ephemera/internal/tools"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func conversationMessages(messages []history.Message) []llm.Message {
	out := make([]llm.Message, 0, len(messages))
	for _, message := range messages {
		if message.Role != "user" && message.Role != "assistant" {
			continue
		}
		if message.Role == "assistant" && isLegacyApprovalControlMessage(message.Content) {
			continue
		}
		out = append(out, llm.Message{Role: message.Role, Content: message.Content})
	}
	return out
}

func isLegacyApprovalControlMessage(content string) bool {
	content = strings.TrimSpace(content)
	return strings.HasPrefix(content, "Approval required for `") &&
		strings.HasSuffix(content, "Run `/approve` to execute it or `/reject` to skip it.")
}

func toolResultEvent(runID string, iteration int, result tools.Result) history.Event {
	status := "done"
	content := result.Output
	if !result.OK {
		status = "error"
		content = firstNonEmpty(result.Error, result.Output)
		alreadyLogged, _ := result.Metadata["debug_logged"].(bool)
		if !alreadyLogged {
			debuglog.FailureCtx(debuglog.ContextForRun(runID), "agent", "tool result failed", content, map[string]any{
				"run_id":    runID,
				"iteration": iteration,
				"tool":      result.Tool,
			})
		}
	}
	event := runEvent(runID, iteration, "tool_result", result.Tool, content, result.Tool, status)
	if result.Metadata != nil {
		for key, value := range result.Metadata {
			event.Metadata[key] = value
		}
	}
	return event
}

func runEvent(runID string, iteration int, kind, title, content, tool, status string) history.Event {
	event := history.Event{
		ID:      fmt.Sprintf("evt-%d", time.Now().UnixNano()),
		Type:    kind,
		Title:   title,
		Content: strings.TrimSpace(content),
		Tool:    tool,
		Status:  status,
		Metadata: map[string]any{
			"run_id":    runID,
			"iteration": iteration,
		},
		CreatedAt: time.Now(),
	}
	if isFailureEventStatus(status) && !(kind == history.EventToolResult && strings.TrimSpace(tool) != "") {
		debuglog.FailureCtx(debuglog.ContextForRun(runID), "agent", firstNonEmpty(title, kind), content, map[string]any{
			"run_id":     runID,
			"iteration":  iteration,
			"event_type": kind,
			"status":     status,
			"tool":       tool,
		})
	}
	return event
}

func isFailureEventStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "error", "failed", "failure", "blocked", "timeout":
		return true
	default:
		return false
	}
}

func recentToolObservations(events []history.Event) []string {
	var out []string
	for _, event := range tailEvents(events, 16) {
		switch event.Type {
		case history.EventToolResult, history.EventVerification:
			out = append(out, fmt.Sprintf("[%s %s]\n%s", firstNonEmpty(event.Tool, event.Title), fallbackStatus(event.Status), compact(event.Content, 1400)))
		case history.EventApprovalRequest:
			if event.Status != "pending" {
				out = append(out, fmt.Sprintf("[approval %s %s]\n%s", firstNonEmpty(event.Tool, event.Title), fallbackStatus(event.Status), compact(event.Content, 700)))
			}
		}
	}
	return out
}

func tailEvents(events []history.Event, maxItems int) []history.Event {
	if len(events) <= maxItems {
		return events
	}
	return events[len(events)-maxItems:]
}

func tailStrings(values []string, maxItems int) []string {
	if len(values) <= maxItems {
		return values
	}
	return values[len(values)-maxItems:]
}

func formatToolObservation(result tools.Result) string {
	status := "ok"
	content := result.Output
	if !result.OK {
		status = "error"
		content = firstNonEmpty(result.Error, result.Output)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[%s %s]", result.Tool, status)
	if evidence := formatResultMetadata(result.Metadata); evidence != "" {
		b.WriteString("\nmetadata: ")
		b.WriteString(evidence)
	}
	if body := compact(content, 1800); body != "" {
		b.WriteString("\n")
		b.WriteString(body)
	}
	return b.String()
}

func formatResultMetadata(metadata map[string]any) string {
	if len(metadata) == 0 {
		return ""
	}
	preferred := []string{"path", "start_line", "end_line", "changed", "replacements", "duration_ms", "risk"}
	parts := make([]string, 0, len(metadata))
	used := map[string]struct{}{}
	for _, key := range preferred {
		if value, ok := metadata[key]; ok {
			parts = append(parts, key+"="+compactMetadataValue(value))
			used[key] = struct{}{}
		}
	}
	var rest []string
	for key := range metadata {
		if _, ok := used[key]; ok || key == "ok" {
			continue
		}
		rest = append(rest, key)
	}
	sort.Strings(rest)
	for _, key := range rest {
		parts = append(parts, key+"="+compactMetadataValue(metadata[key]))
	}
	return strings.Join(parts, " ")
}

func compactMetadataValue(value any) string {
	switch v := value.(type) {
	case string:
		if v == "" {
			return `""`
		}
		return compact(v, 160)
	case fmt.Stringer:
		return compact(v.String(), 160)
	default:
		return compact(fmt.Sprint(v), 160)
	}
}

func approvalText(call tools.Call, reason string) string {
	return fmt.Sprintf("Approval required for `%s`: %s\n\nRun `/approve` to execute it or `/reject` to skip it.", call.Name, reason)
}

func formatToolCall(action modelToolAction) string {
	var b strings.Builder
	if strings.TrimSpace(action.Purpose) != "" {
		b.WriteString("purpose: ")
		b.WriteString(strings.TrimSpace(action.Purpose))
		b.WriteString("\n")
	}
	if strings.TrimSpace(action.ExpectedResult) != "" {
		b.WriteString("expected: ")
		b.WriteString(strings.TrimSpace(action.ExpectedResult))
		b.WriteString("\n")
	}
	b.WriteString(marshalArgs(action.Arguments))
	return strings.TrimSpace(b.String())
}

func marshalArgs(args map[string]any) string {
	data, err := json.MarshalIndent(args, "", "  ")
	if err != nil {
		return fmt.Sprint(args)
	}
	return string(data)
}

func toolFingerprint(call tools.Call) string {
	return tools.Fingerprint(call)
}

func eventFingerprint(event history.Event) string {
	if event.Metadata == nil {
		return ""
	}
	value, _ := event.Metadata["call_fingerprint"].(string)
	return strings.TrimSpace(value)
}

func attachCallMetadata(result *tools.Result, fingerprint string) {
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	if strings.TrimSpace(fingerprint) != "" {
		result.Metadata["call_fingerprint"] = fingerprint
	}
}

func metadataBool(metadata map[string]any, key string) bool {
	if metadata == nil {
		return false
	}
	value, _ := metadata[key].(bool)
	return value
}

func (s *runState) toolRisk(name string) tools.Risk {
	tool, ok := s.toolRegistry.Lookup(name)
	if !ok {
		return ""
	}
	return tool.Risk
}

func (s *runState) isWorkspaceMutation(name string) bool {
	if s.toolRisk(name) == tools.RiskWrite {
		return true
	}
	switch name {
	case "run_formatter", "git_merge", "git_checkout":
		return true
	default:
		return false
	}
}

func normalizePath(value string) string {
	value = filepath.ToSlash(filepath.Clean(strings.TrimSpace(value)))
	return strings.ToLower(strings.TrimPrefix(value, "./"))
}

func sortedTrueKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key, enabled := range values {
		if enabled {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func sortedKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func compact(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= limit {
		return value
	}
	return value[:limit-3] + "..."
}

func fallbackStatus(value string) string {
	if strings.TrimSpace(value) == "" {
		return "done"
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func boolMetric(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
