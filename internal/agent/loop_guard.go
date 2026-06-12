package agent

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
)

type LoopAction string

const (
	LoopContinue LoopAction = "continue"
	LoopReplan   LoopAction = "replan"
	LoopStop     LoopAction = "stop"
)

type ProgressSnapshot struct {
	Goal              string
	WorkspaceRevision int
	PlanDigest        string
	EvidenceDigest    string
	InspectedPaths    int
	ChangedPaths      int
	Verified          bool
}

type LoopDecision struct {
	Action      LoopAction
	Fingerprint string
	Reason      string
	Repeats     int
}

// ProgressGuard detects semantic stalls even when the model changes wording or
// tool arguments. The first stall forces a strategy change; a repeated stall
// stops the run so rollback/recovery can take over.
type ProgressGuard struct {
	counts       map[string]int
	replans      map[string]int
	lastProgress string
}

func NewProgressGuard() *ProgressGuard {
	return &ProgressGuard{counts: map[string]int{}, replans: map[string]int{}}
}

func (g *ProgressGuard) Record(snapshot ProgressSnapshot, madeProgress bool) LoopDecision {
	if g == nil {
		return LoopDecision{Action: LoopContinue}
	}
	fingerprint := progressFingerprint(snapshot)
	if madeProgress {
		g.lastProgress = fingerprint
		g.counts = map[string]int{fingerprint: 1}
		return LoopDecision{Action: LoopContinue, Fingerprint: fingerprint, Repeats: 1}
	}
	g.counts[fingerprint]++
	repeats := g.counts[fingerprint]
	if repeats < 2 {
		return LoopDecision{Action: LoopContinue, Fingerprint: fingerprint, Repeats: repeats}
	}
	if g.replans[fingerprint] == 0 {
		g.replans[fingerprint] = 1
		return LoopDecision{
			Action:      LoopReplan,
			Fingerprint: fingerprint,
			Repeats:     repeats,
			Reason:      "workspace, evidence, plan state, and verification state repeated without measurable progress",
		}
	}
	return LoopDecision{
		Action:      LoopStop,
		Fingerprint: fingerprint,
		Repeats:     repeats,
		Reason:      "the run remained in the same semantic state after a forced strategy change",
	}
}

func progressSnapshot(state *runState) ProgressSnapshot {
	if state == nil {
		return ProgressSnapshot{}
	}
	goal := ""
	if state.plan != nil {
		goal = state.plan.Goal
	}
	if goal == "" && state.contract != nil {
		goal = state.contract.Goal
	}
	return ProgressSnapshot{
		Goal:              goal,
		WorkspaceRevision: state.workspaceRevision,
		PlanDigest:        digestPlan(state.plan),
		EvidenceDigest:    digestUniqueEvidence(state.observations),
		InspectedPaths:    len(state.inspectedPaths),
		ChangedPaths:      len(state.changedPaths),
		Verified:          state.verified,
	}
}

func progressFingerprint(snapshot ProgressSnapshot) string {
	value := fmt.Sprintf("%s|%d|%s|%s|%d|%d|%t",
		strings.TrimSpace(snapshot.Goal),
		snapshot.WorkspaceRevision,
		snapshot.PlanDigest,
		snapshot.EvidenceDigest,
		snapshot.InspectedPaths,
		snapshot.ChangedPaths,
		snapshot.Verified,
	)
	hash := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", hash[:12])
}

func digestPlan(plan *Plan) string {
	if plan == nil {
		return ""
	}
	var parts []string
	for _, step := range plan.Steps {
		parts = append(parts, fmt.Sprintf("%d:%s:%s", step.ID, step.Status, strings.TrimSpace(step.Description)))
	}
	return digestStrings(parts)
}

func digestUniqueEvidence(observations []string) string {
	seen := map[string]bool{}
	var unique []string
	for _, observation := range observations {
		observation = compact(strings.TrimSpace(observation), 800)
		if observation == "" || seen[observation] {
			continue
		}
		seen[observation] = true
		unique = append(unique, observation)
	}
	sort.Strings(unique)
	return digestStrings(unique)
}

func digestStrings(values []string) string {
	hash := sha256.Sum256([]byte(strings.Join(values, "\n")))
	return fmt.Sprintf("%x", hash[:12])
}
