package agent

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	workruntime "github.com/ephemera-ai/ephemera/internal/runtime"
	"github.com/ephemera-ai/ephemera/internal/tools"
	"github.com/ephemera-ai/ephemera/internal/util"
)

type AcceptanceStatus string

const (
	AcceptancePending       AcceptanceStatus = "pending"
	AcceptancePassed        AcceptanceStatus = "passed"
	AcceptanceFailed        AcceptanceStatus = "failed"
	eventAcceptanceContract                  = "acceptance_contract"
)

// AcceptanceContract is the machine-readable definition of done for a run.
// The model may enrich it, but only tool evidence can satisfy required checks.
type AcceptanceContract struct {
	Goal              string                  `json:"goal"`
	RequiredBehaviors []string                `json:"required_behaviors"`
	RequiredChecks    []AcceptanceRequirement `json:"required_checks"`
	ForbiddenChanges  []string                `json:"forbidden_changes"`
	Evidence          []AcceptanceEvidence    `json:"evidence,omitempty"`
	CreatedAt         time.Time               `json:"created_at"`
	Source            string                  `json:"source"`
}

type AcceptanceRequirement struct {
	ID          string           `json:"id"`
	Description string           `json:"description"`
	Command     string           `json:"command,omitempty"`
	Required    bool             `json:"required"`
	Status      AcceptanceStatus `json:"status"`
	Evidence    []string         `json:"evidence,omitempty"`
}

type AcceptanceEvidence struct {
	Kind      string    `json:"kind"`
	Summary   string    `json:"summary"`
	Tool      string    `json:"tool,omitempty"`
	Path      string    `json:"path,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func newAcceptanceContract(goal string, manifest workruntime.ProjectManifest, source string) *AcceptanceContract {
	contract := &AcceptanceContract{
		Goal:              strings.TrimSpace(goal),
		RequiredBehaviors: []string{strings.TrimSpace(goal)},
		ForbiddenChanges:  append([]string(nil), manifest.ProtectedPaths...),
		CreatedAt:         time.Now(),
		Source:            source,
	}
	if contract.Goal == "" {
		contract.Goal = "Complete the current user request without regressions."
		contract.RequiredBehaviors = []string{contract.Goal}
	}
	contract.RequiredChecks = append(contract.RequiredChecks,
		AcceptanceRequirement{ID: "changed-files-readable", Description: "Every changed file can be read back after the edit.", Required: true, Status: AcceptancePending},
		AcceptanceRequirement{ID: "diff-reviewed", Description: "The final workspace diff or status is inspected before completion.", Required: true, Status: AcceptancePending},
	)
	for _, check := range manifest.AcceptanceChecks {
		contract.RequiredChecks = append(contract.RequiredChecks, AcceptanceRequirement{
			ID:          check.ID,
			Description: check.Description,
			Command:     check.Command,
			Required:    check.Required,
			Status:      AcceptancePending,
		})
	}
	contract.deduplicateRequirements()
	return contract
}

func (c *AcceptanceContract) deduplicateRequirements() {
	if c == nil {
		return
	}
	seen := map[string]bool{}
	out := c.RequiredChecks[:0]
	for _, requirement := range c.RequiredChecks {
		id := strings.TrimSpace(requirement.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, requirement)
	}
	c.RequiredChecks = out
}

func (c *AcceptanceContract) Observe(call tools.Call, result tools.Result) {
	if c == nil {
		return
	}
	path := metadataString(result.Metadata, "path")
	if path == "" && call.Arguments != nil {
		path = strings.TrimSpace(fmt.Sprint(call.Arguments["path"]))
	}
	summary := compact(firstNonEmpty(result.Output, result.Error), 300)
	if summary == "" {
		summary = fmt.Sprintf("%s returned ok=%t", call.Name, result.OK)
	}
	c.Evidence = append(c.Evidence, AcceptanceEvidence{Kind: "tool_result", Summary: summary, Tool: call.Name, Path: normalizePath(path), CreatedAt: time.Now()})
	if len(c.Evidence) > 80 {
		c.Evidence = append([]AcceptanceEvidence(nil), c.Evidence[len(c.Evidence)-80:]...)
	}
	for index := range c.RequiredChecks {
		requirement := &c.RequiredChecks[index]
		switch requirement.ID {
		case "changed-files-readable":
			if call.Name == "read_file" && result.OK && path != "" {
				requirement.Evidence = appendUnique(requirement.Evidence, "read back "+normalizePath(path))
			}
			if call.Name == "list_files" && result.OK && path != "" {
				requirement.Evidence = appendUnique(requirement.Evidence, "read back directory "+normalizePath(path))
			}
		case "diff-reviewed":
			if call.Name == "git_diff" || call.Name == "git_status" {
				requirement.Status = AcceptancePassed
				evidence := call.Name + " inspected"
				if !result.OK {
					evidence += " (git metadata unavailable)"
				}
				requirement.Evidence = appendUnique(requirement.Evidence, evidence)
			}
		default:
			if strings.TrimSpace(requirement.Command) != "" && call.Name == "go_test" {
				command := metadataString(result.Metadata, "command")
				if command == "" {
					command = strings.TrimSpace(requirement.Command)
				}
				scope := metadataString(result.Metadata, "verification_scope")
				label := "verification command"
				if scope == "task" {
					label = "task-scoped verification"
				}
				if result.OK {
					requirement.Status = AcceptancePassed
					requirement.Evidence = removeSupersededVerificationFailures(requirement.Evidence)
					requirement.Evidence = appendUnique(requirement.Evidence, label+" passed: "+command)
				} else {
					requirement.Status = AcceptanceFailed
					requirement.Evidence = appendUnique(requirement.Evidence, label+" failed: "+command+" — "+compact(firstNonEmpty(result.Error, result.Output), 180))
				}
			}
		}
	}
}

func (c *AcceptanceContract) Evaluate(changedPaths map[string]bool) CompletionGateReport {
	return c.EvaluateArtifacts(changedPaths, nil)
}

func (c *AcceptanceContract) EvaluateArtifacts(changedPaths, changedDirectories map[string]bool) CompletionGateReport {
	report := CompletionGateReport{Passed: true, CheckedAt: time.Now()}
	if c == nil {
		return report
	}
	for path := range changedPaths {
		if protectedPath(path, c.ForbiddenChanges) {
			report.Passed = false
			report.Blockers = append(report.Blockers, "protected path changed: "+path)
		}
	}
	for path := range changedDirectories {
		if protectedPath(path, c.ForbiddenChanges) {
			report.Passed = false
			report.Blockers = append(report.Blockers, "protected path changed: "+path)
		}
	}
	for index := range c.RequiredChecks {
		requirement := &c.RequiredChecks[index]
		if requirement.ID == "changed-files-readable" {
			missingFiles := unreadChangedPaths(changedPaths, requirement.Evidence)
			missingDirectories := unreadChangedDirectories(changedDirectories, requirement.Evidence)
			if len(missingFiles) == 0 && len(missingDirectories) == 0 {
				requirement.Status = AcceptancePassed
			} else {
				requirement.Status = AcceptancePending
				if len(missingFiles) > 0 {
					report.Blockers = append(report.Blockers, "changed files not read back: "+strings.Join(missingFiles, ", "))
				}
				if len(missingDirectories) > 0 {
					report.Blockers = append(report.Blockers, "changed directories not inspected: "+strings.Join(missingDirectories, ", "))
				}
			}
		}
		if requirement.Required && requirement.Status != AcceptancePassed {
			report.Passed = false
			report.PendingChecks = append(report.PendingChecks, requirement.Description)
		}
		if requirement.Status == AcceptancePassed {
			report.Evidence = append(report.Evidence, requirement.Evidence...)
		}
	}
	report.Blockers = util.UniqueSortedStrings(report.Blockers)
	report.PendingChecks = util.UniqueSortedStrings(report.PendingChecks)
	report.Evidence = util.UniqueSortedStrings(report.Evidence)
	return report
}

func (c *AcceptanceContract) MarkVerificationNotApplicable(reason string) {
	if c == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	for index := range c.RequiredChecks {
		requirement := &c.RequiredChecks[index]
		if strings.TrimSpace(requirement.Command) == "" {
			continue
		}
		requirement.Status = AcceptancePassed
		requirement.Evidence = appendUnique(requirement.Evidence, "verification not applicable: "+reason)
	}
}

func (c *AcceptanceContract) Render() string {
	if c == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Goal: %s\n", c.Goal)
	b.WriteString("Required checks:\n")
	for _, check := range c.RequiredChecks {
		mark := "[ ]"
		if check.Status == AcceptancePassed {
			mark = "[x]"
		} else if check.Status == AcceptanceFailed {
			mark = "[!]"
		}
		fmt.Fprintf(&b, "- %s %s", mark, check.Description)
		if check.Command != "" {
			fmt.Fprintf(&b, " (`%s`)", check.Command)
		}
		b.WriteByte('\n')
	}
	if len(c.ForbiddenChanges) > 0 {
		fmt.Fprintf(&b, "Forbidden changes: %s\n", strings.Join(c.ForbiddenChanges, ", "))
	}
	return strings.TrimSpace(b.String())
}

func protectedPath(path string, patterns []string) bool {
	path = normalizePath(path)
	for _, pattern := range patterns {
		pattern = normalizePath(pattern)
		if pattern == "" {
			continue
		}
		if matched, _ := filepath.Match(pattern, path); matched {
			return true
		}
		trimmed := strings.TrimSuffix(pattern, "/*")
		if path == trimmed || strings.HasPrefix(path, trimmed+"/") {
			return true
		}
	}
	return false
}

func unreadChangedPaths(changed map[string]bool, evidence []string) []string {
	var missing []string
	for path := range changed {
		needle := "read back " + normalizePath(path)
		found := false
		for _, item := range evidence {
			if item == needle {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, normalizePath(path))
		}
	}
	sort.Strings(missing)
	return missing
}

func unreadChangedDirectories(changed map[string]bool, evidence []string) []string {
	var missing []string
	for path := range changed {
		needle := "read back directory " + normalizePath(path)
		found := false
		for _, item := range evidence {
			if item == needle {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, normalizePath(path))
		}
	}
	sort.Strings(missing)
	return missing
}

func removeSupersededVerificationFailures(values []string) []string {
	out := values[:0]
	for _, value := range values {
		lower := strings.ToLower(value)
		if strings.Contains(lower, "verification") && strings.Contains(lower, " failed:") {
			continue
		}
		out = append(out, value)
	}
	return out
}

func appendUnique(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
