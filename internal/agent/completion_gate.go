package agent

import (
	"github.com/ephemera-ai/ephemera/internal/util"
	"strings"
	"time"
)

// CompletionGateReport explains why a run may or may not claim success.
type CompletionGateReport struct {
	Passed        bool      `json:"passed"`
	PendingChecks []string  `json:"pending_checks,omitempty"`
	Blockers      []string  `json:"blockers,omitempty"`
	Evidence      []string  `json:"evidence,omitempty"`
	CheckedAt     time.Time `json:"checked_at"`
}

func (r CompletionGateReport) Summary() string {
	if r.Passed {
		if len(r.Evidence) == 0 {
			return "Completion contract satisfied."
		}
		return "Completion contract satisfied: " + strings.Join(r.Evidence, "; ")
	}
	parts := append([]string(nil), r.Blockers...)
	for _, check := range r.PendingChecks {
		parts = append(parts, "pending: "+check)
	}
	if len(parts) == 0 {
		return "Completion contract is not satisfied."
	}
	return "Completion blocked: " + strings.Join(util.UniqueSortedStrings(parts), "; ")
}

func (s *runState) completionReport() CompletionGateReport {
	if s == nil || !s.changed {
		return CompletionGateReport{Passed: true, CheckedAt: time.Now()}
	}
	report := CompletionGateReport{Passed: s.verified, CheckedAt: time.Now()}
	if s.contract != nil {
		report = s.contract.EvaluateArtifacts(s.changedPaths, s.changedDirectories)
		if !s.verified {
			report.Passed = false
			report.Blockers = appendUnique(report.Blockers, "verification has not passed")
		}
	}
	return report
}
