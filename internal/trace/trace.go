// Package trace records structured agent timelines and renders them for humans.
package trace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/reasoning"
)

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	ToolCalls    int `json:"tool_calls"`
}

type Run struct {
	Version   int                       `json:"version"`
	ID        string                    `json:"id"`
	StartedAt time.Time                 `json:"started_at"`
	Duration  time.Duration             `json:"duration"`
	Provider  string                    `json:"provider"`
	Model     string                    `json:"model"`
	Mode      reasoning.Mode            `json:"mode"`
	Verified  bool                      `json:"verified"`
	Usage     Usage                     `json:"usage"`
	Reasoning []reasoning.ReasoningStep `json:"reasoning,omitempty"`
	Events    []history.Event           `json:"events"`
	FinalText string                    `json:"final_text,omitempty"`
}

func Directory(root string) string { return filepath.Join(root, ".ephemera", "traces") }

func Path(root, runID string) string {
	runID = filepath.Base(strings.TrimSpace(runID))
	return filepath.Join(Directory(root), runID+".json")
}

func Write(root string, run Run) (string, error) {
	if strings.TrimSpace(run.ID) == "" {
		return "", fmt.Errorf("trace run id is required")
	}
	run.Version = 1
	path := Path(root, run.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return path, nil
}

func Load(root, runID string) (Run, error) {
	data, err := os.ReadFile(Path(root, runID))
	if err != nil {
		return Run{}, err
	}
	var run Run
	if err := json.Unmarshal(data, &run); err != nil {
		return Run{}, err
	}
	return run, nil
}

func List(root string) ([]string, error) {
	entries, err := os.ReadDir(Directory(root))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		ids = append(ids, strings.TrimSuffix(entry.Name(), ".json"))
	}
	sort.Strings(ids)
	return ids, nil
}
