package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type HistoryEntry struct {
	ID               string        `json:"id"`
	Timestamp        time.Time     `json:"timestamp"`
	Task             string        `json:"task"`
	Provider         string        `json:"provider,omitempty"`
	Model            string        `json:"model,omitempty"`
	Passed           bool          `json:"passed"`
	Duration         time.Duration `json:"duration"`
	InputTokens      int           `json:"input_tokens,omitempty"`
	OutputTokens     int           `json:"output_tokens,omitempty"`
	TokenCost        int           `json:"token_cost,omitempty"`
	ToolCalls        int           `json:"tool_calls,omitempty"`
	ReasoningQuality int           `json:"reasoning_quality,omitempty"`
}

type History struct {
	Version int            `json:"version"`
	Runs    []HistoryEntry `json:"runs"`
}

func HistoryPath(root string) string { return filepath.Join(root, "evals", "history.json") }

func LoadHistory(root string) (History, error) {
	data, err := os.ReadFile(HistoryPath(root))
	if os.IsNotExist(err) {
		return History{Version: 1}, nil
	}
	if err != nil {
		return History{}, err
	}
	var history History
	if err := json.Unmarshal(data, &history); err != nil {
		return History{}, err
	}
	if history.Version == 0 {
		history.Version = 1
	}
	return history, nil
}

func AppendHistory(root string, entry HistoryEntry) (HistoryEntry, error) {
	history, err := LoadHistory(root)
	if err != nil {
		return HistoryEntry{}, err
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	if strings.TrimSpace(entry.ID) == "" {
		entry.ID = fmt.Sprintf("eval-%s-%d", sanitizeID(entry.Task), entry.Timestamp.UnixNano())
	}
	history.Runs = append(history.Runs, entry)
	if len(history.Runs) > 1000 {
		history.Runs = history.Runs[len(history.Runs)-1000:]
	}
	path := HistoryPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return HistoryEntry{}, err
	}
	data, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return HistoryEntry{}, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return HistoryEntry{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return HistoryEntry{}, err
	}
	return entry, nil
}

func FormatHistory(history History) string {
	if len(history.Runs) == 0 {
		return "No evaluation history."
	}
	runs := append([]HistoryEntry(nil), history.Runs...)
	sort.Slice(runs, func(i, j int) bool { return runs[i].Timestamp.After(runs[j].Timestamp) })
	if len(runs) > 30 {
		runs = runs[:30]
	}
	var b strings.Builder
	b.WriteString("Evaluation history\n")
	for _, run := range runs {
		status := "FAIL"
		if run.Passed {
			status = "PASS"
		}
		fmt.Fprintf(&b, "- %s [%s] %s · %s · tokens=%d · quality=%d · %s\n", run.ID, status, run.Task, run.Duration.Round(time.Millisecond), run.TokenCost, run.ReasoningQuality, run.Timestamp.Local().Format("2006-01-02 15:04"))
	}
	return strings.TrimSpace(b.String())
}

func DiffHistory(history History, fromID, toID string) (string, error) {
	var from, to *HistoryEntry
	for i := range history.Runs {
		run := &history.Runs[i]
		if run.ID == fromID {
			from = run
		}
		if run.ID == toID {
			to = run
		}
	}
	if from == nil || to == nil {
		return "", fmt.Errorf("eval run not found (from=%q to=%q)", fromID, toID)
	}
	regression := from.Passed && !to.Passed
	return fmt.Sprintf("Eval diff %s -> %s\npass: %t -> %t · regression=%t\ntokens: %d -> %d (%+d)\nlatency: %s -> %s\nreasoning quality: %d -> %d (%+d)",
		from.ID, to.ID, from.Passed, to.Passed, regression,
		from.TokenCost, to.TokenCost, to.TokenCost-from.TokenCost,
		from.Duration.Round(time.Millisecond), to.Duration.Round(time.Millisecond),
		from.ReasoningQuality, to.ReasoningQuality, to.ReasoningQuality-from.ReasoningQuality,
	), nil
}

func sanitizeID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
