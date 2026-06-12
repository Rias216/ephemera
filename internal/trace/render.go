package trace

import (
	"fmt"
	"strings"
)

func RenderTree(run Run) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s · %s/%s · %s · verified=%t\n", run.ID, run.Provider, run.Model, run.Duration.Round(1e6), run.Verified)
	for _, event := range run.Events {
		indent := "  "
		if iteration := eventIteration(event.Metadata); iteration > 0 {
			indent = strings.Repeat("  ", min(iteration, 6))
		}
		fmt.Fprintf(&b, "%s[%s] %s", indent, event.Status, event.Title)
		if event.Tool != "" {
			fmt.Fprintf(&b, " (%s)", event.Tool)
		}
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "usage: in=%d out=%d tools=%d", run.Usage.InputTokens, run.Usage.OutputTokens, run.Usage.ToolCalls)
	return strings.TrimSpace(b.String())
}

func RenderMermaid(run Run) string {
	var b strings.Builder
	b.WriteString("sequenceDiagram\n")
	b.WriteString("  participant U as User\n  participant A as Agent\n  participant T as Tools\n  participant P as Provider\n")
	for _, event := range run.Events {
		title := escapeMermaid(event.Title)
		switch event.Type {
		case "tool_call":
			fmt.Fprintf(&b, "  A->>T: %s\n", title)
		case "tool_result":
			fmt.Fprintf(&b, "  T-->>A: %s [%s]\n", title, event.Status)
		case "final":
			fmt.Fprintf(&b, "  A-->>U: %s\n", title)
		default:
			fmt.Fprintf(&b, "  P-->>A: %s\n", title)
		}
	}
	return strings.TrimSpace(b.String())
}

func escapeMermaid(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, ":", " -")
	return strings.TrimSpace(value)
}

func eventIteration(metadata map[string]any) int {
	if metadata == nil {
		return 0
	}
	switch value := metadata["iteration"].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}
