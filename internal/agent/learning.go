package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type episodicMemory struct {
	Version  int             `json:"version"`
	Episodes []memoryEpisode `json:"episodes"`
}

type memoryEpisode struct {
	CompletedAt  time.Time `json:"completed_at"`
	Goal         string    `json:"goal"`
	Summary      string    `json:"summary"`
	ChangedPaths []string  `json:"changed_paths,omitempty"`
	ToolSequence []string  `json:"tool_sequence,omitempty"`
	Evidence     []string  `json:"evidence,omitempty"`
	Verified     bool      `json:"verified"`
}

func (r Runner) learnFromRun(finalText string, state *runState) {
	if !r.Config.AgentLearnMemory || state == nil || strings.TrimSpace(finalText) == "" {
		return
	}
	dir := filepath.Join(r.Tools.WorkspaceRoot, ".ephemera")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	path := filepath.Join(dir, "memory.json")
	memory := episodicMemory{Version: 1}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &memory)
	}
	goal := "Complete the current request."
	if state.plan != nil && strings.TrimSpace(state.plan.Goal) != "" {
		goal = state.plan.Goal
	}
	evidence := tailStrings(state.observations, 4)
	for index := range evidence {
		evidence[index] = compact(evidence[index], 500)
	}
	memory.Episodes = append(memory.Episodes, memoryEpisode{
		CompletedAt:  time.Now().UTC(),
		Goal:         compact(goal, 500),
		Summary:      compact(finalText, 900),
		ChangedPaths: sortedKeys(state.changedPaths),
		ToolSequence: compactToolSequence(state.toolSequence, 12),
		Evidence:     evidence,
		Verified:     state.verified,
	})
	if len(memory.Episodes) > 100 {
		memory.Episodes = memory.Episodes[len(memory.Episodes)-100:]
	}
	data, err := json.MarshalIndent(memory, "", "  ")
	if err != nil {
		return
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

func compactToolSequence(sequence []string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	out := make([]string, 0, minInt(limit, len(sequence)))
	for _, name := range sequence {
		name = strings.TrimSpace(name)
		if name == "" || (len(out) > 0 && out[len(out)-1] == name) {
			continue
		}
		out = append(out, name)
		if len(out) >= limit {
			break
		}
	}
	return out
}
