// Package eval defines reproducible, repository-local agent tasks and
// deterministic graders. It intentionally does not import the agent package so
// production runs can emit traces into the evaluator without dependency cycles.
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	workruntime "github.com/ephemera-ai/ephemera/internal/runtime"
)

type Task struct {
	Name             string            `json:"name"`
	Prompt           string            `json:"prompt"`
	Setup            []FileFixture     `json:"setup,omitempty"`
	Checks           []Check           `json:"checks"`
	ForbiddenChanges []string          `json:"forbidden_changes,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
}

type FileFixture struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type Check struct {
	Name         string `json:"name"`
	Command      string `json:"command,omitempty"`
	Path         string `json:"path,omitempty"`
	Contains     string `json:"contains,omitempty"`
	MustNotExist bool   `json:"must_not_exist,omitempty"`
	Required     bool   `json:"required"`
}

type CheckResult struct {
	Name     string        `json:"name"`
	Passed   bool          `json:"passed"`
	Evidence string        `json:"evidence,omitempty"`
	Duration time.Duration `json:"duration,omitempty"`
}

type Report struct {
	Task      string        `json:"task"`
	StartedAt time.Time     `json:"started_at"`
	Duration  time.Duration `json:"duration"`
	Results   []CheckResult `json:"results"`
}

func FormatReport(report Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Evaluation: %s\n", report.Task)
	fmt.Fprintf(&b, "Passed: %t · Duration: %s\n", report.Passed(), report.Duration.Round(time.Millisecond))
	for _, result := range report.Results {
		mark := "PASS"
		if !result.Passed {
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "- [%s] %s", mark, result.Name)
		if strings.TrimSpace(result.Evidence) != "" {
			fmt.Fprintf(&b, ": %s", strings.TrimSpace(result.Evidence))
		}
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

func (r Report) Passed() bool {
	for _, result := range r.Results {
		if !result.Passed {
			return false
		}
	}
	return true
}

func LoadTask(path string) (Task, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Task{}, err
	}
	var task Task
	if err := json.Unmarshal(data, &task); err != nil {
		return Task{}, err
	}
	if strings.TrimSpace(task.Name) == "" || strings.TrimSpace(task.Prompt) == "" {
		return Task{}, fmt.Errorf("eval task requires name and prompt")
	}
	return task, nil
}

func PrepareWorkspace(root string, task Task) error {
	for _, fixture := range task.Setup {
		path, err := safeJoin(root, fixture.Path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(fixture.Content), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func Grade(ctx context.Context, root string, task Task, timeout time.Duration) Report {
	started := time.Now()
	report := Report{Task: task.Name, StartedAt: started}
	supervisor := workruntime.ProcessSupervisor{Root: root, Timeout: timeout, MaxOutput: 96 << 10}
	for _, check := range task.Checks {
		result := CheckResult{Name: check.Name, Passed: true}
		if strings.TrimSpace(result.Name) == "" {
			result.Name = firstNonEmpty(check.Command, check.Path, "unnamed check")
		}
		switch {
		case strings.TrimSpace(check.Command) != "":
			command := supervisor.Run(ctx, check.Command)
			result.Duration = command.Duration
			result.Passed = command.ExitCode == 0 && !command.TimedOut
			result.Evidence = command.Output
		case strings.TrimSpace(check.Path) != "":
			path, err := safeJoin(root, check.Path)
			if err != nil {
				result.Passed = false
				result.Evidence = err.Error()
				break
			}
			data, readErr := os.ReadFile(path)
			if check.MustNotExist {
				result.Passed = os.IsNotExist(readErr)
				if !result.Passed {
					result.Evidence = "forbidden path exists"
				}
				break
			}
			result.Passed = readErr == nil
			if readErr != nil {
				result.Evidence = readErr.Error()
				break
			}
			if check.Contains != "" {
				result.Passed = strings.Contains(string(data), check.Contains)
				result.Evidence = fmt.Sprintf("contains %q: %t", check.Contains, result.Passed)
			}
		default:
			result.Passed = !check.Required
			result.Evidence = "check has no command or path"
		}
		if !check.Required && !result.Passed {
			result.Evidence = "optional check failed: " + result.Evidence
			result.Passed = true
		}
		report.Results = append(report.Results, result)
	}
	for _, path := range task.ForbiddenChanges {
		full, err := safeJoin(root, path)
		result := CheckResult{Name: "forbidden path " + path, Passed: err == nil}
		if err == nil {
			_, statErr := os.Stat(full)
			result.Passed = os.IsNotExist(statErr)
			if !result.Passed {
				result.Evidence = "path exists"
			}
		} else {
			result.Evidence = err.Error()
		}
		report.Results = append(report.Results, result)
	}
	report.Duration = time.Since(started)
	return report
}

func safeJoin(root, relative string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	path, err := filepath.Abs(filepath.Join(root, filepath.Clean(relative)))
	if err != nil {
		return "", err
	}
	if path != root && !strings.HasPrefix(path, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes eval workspace: %s", relative)
	}
	return path, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
