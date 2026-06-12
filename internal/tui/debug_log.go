package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ephemera-ai/ephemera/internal/debuglog"
)

func (m Model) debugFields(extra map[string]any) map[string]any {
	fields := map[string]any{
		"session":  m.session.Name,
		"provider": firstNonEmpty(m.liveAgent.RunProvider, m.cfg.Provider),
		"model":    firstNonEmpty(m.liveAgent.RunModel, m.cfg.Model()),
	}
	if m.liveAgent.RunID != "" {
		fields["run_id"] = m.liveAgent.RunID
	}
	if m.liveAgent.Phase != "" {
		fields["phase"] = m.liveAgent.Phase
	}
	if m.liveAgent.Iteration > 0 {
		fields["iteration"] = m.liveAgent.Iteration
	}
	for key, value := range extra {
		fields[key] = value
	}
	return fields
}

func (m Model) recordError(event string, err error, extra map[string]any) {
	debuglog.Error("tui", event, err, m.debugFields(extra))
}

func (m Model) recordFailure(event, message string, extra map[string]any) {
	debuglog.Failure("tui", event, message, m.debugFields(extra))
}

func (m Model) debugLogNotice(limit int) string {
	sessionEntries, sessionErr := debuglog.RecentSession(m.session.Name, limit)
	globalEntries, globalErr := debuglog.Recent(limit)
	var b strings.Builder
	fmt.Fprintf(&b, "### Session diagnostics\n\n- Session: `%s`\n- Snapshot: `%s`\n- Debug events: `%s`\n- Provider context: `%s`\n- Global debug log: `%s`\n- Rotation: debug 5 MiB × 4 · context 32 MiB × 6\n- Secrets: automatically redacted\n",
		escapeMarkdown(m.session.Name),
		escapeMarkdown(filepath.Join(debuglog.SessionDirectory(m.session.Name), "session.json")),
		escapeMarkdown(debuglog.SessionDebugPath(m.session.Name)),
		escapeMarkdown(debuglog.SessionContextPath(m.session.Name)),
		escapeMarkdown(debuglog.Path()),
	)
	if sessionErr != nil {
		fmt.Fprintf(&b, "\nSession log read failed: `%s`", escapeMarkdown(sessionErr.Error()))
	} else if len(sessionEntries) == 0 {
		b.WriteString("\nNo session diagnostic events have been recorded yet.")
	} else {
		b.WriteString("\n#### Recent session events\n")
		for _, entry := range sessionEntries {
			fmt.Fprintf(&b, "\n- `%s` **%s / %s** — %s", entry.Time.Local().Format("2006-01-02 15:04:05"), escapeMarkdown(entry.Component), escapeMarkdown(entry.Event), escapeMarkdown(compact(entry.Message, 260)))
			if summary := debugFieldSummary(entry.Fields); summary != "" {
				fmt.Fprintf(&b, "\n  `%s`", escapeMarkdown(summary))
			}
		}
	}
	if globalErr != nil {
		fmt.Fprintf(&b, "\n\nGlobal log read failed: `%s`", escapeMarkdown(globalErr.Error()))
	} else if len(globalEntries) > 0 {
		b.WriteString("\n\n#### Recent global failures\n")
		shown := 0
		for index := len(globalEntries) - 1; index >= 0 && shown < min(5, limit); index-- {
			entry := globalEntries[index]
			if entry.Level != "error" && entry.Level != "warning" {
				continue
			}
			fmt.Fprintf(&b, "\n- `%s` **%s / %s** — %s", entry.Time.Local().Format("2006-01-02 15:04:05"), escapeMarkdown(entry.Component), escapeMarkdown(entry.Event), escapeMarkdown(compact(entry.Message, 220)))
			shown++
		}
	}
	return strings.TrimSpace(b.String())
}

func debugFieldSummary(fields map[string]any) string {
	if len(fields) == 0 {
		return ""
	}
	preferred := []string{"provider", "model", "session", "run_id", "iteration", "tool", "status", "workspace"}
	seen := map[string]bool{}
	parts := make([]string, 0, len(preferred))
	for _, key := range preferred {
		value, ok := fields[key]
		if !ok || strings.TrimSpace(fmt.Sprint(value)) == "" {
			continue
		}
		seen[key] = true
		parts = append(parts, key+"="+compact(fmt.Sprint(value), 100))
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if len(parts) >= 8 {
			break
		}
		parts = append(parts, key+"="+compact(fmt.Sprint(fields[key]), 100))
	}
	return strings.Join(parts, " · ")
}

func compact(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return strings.Repeat(".", max(0, limit))
	}
	return value[:limit-3] + "..."
}

func clearDebugLog(session string) error {
	if err := debuglog.Clear(); err != nil {
		return err
	}
	return debuglog.ClearSession(session)
}

func debugLogPath() string { return debuglog.Path() }
