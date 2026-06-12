package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/tools"
)

type PlanStepStatus string

const (
	PlanPending PlanStepStatus = "pending"
	PlanRunning PlanStepStatus = "running"
	PlanDone    PlanStepStatus = "done"
	PlanFailed  PlanStepStatus = "failed"
	PlanSkipped PlanStepStatus = "skipped"
)

// Plan is the explicit, persisted execution state for one agent run.
type Plan struct {
	Goal      string           `json:"goal"`
	Steps     []PlanStep       `json:"steps"`
	Tree      []TaskNode       `json:"tree,omitempty"`
	Completed map[int]bool     `json:"completed,omitempty"`
	Evidence  map[int][]string `json:"evidence,omitempty"`
	RevisedAt time.Time        `json:"revised_at"`
}

// TaskNode is the provider-neutral hierarchical plan surface. Phase nodes own
// task children; task dependencies use stable string IDs so the tree can later
// grow beyond two levels without changing the persisted format.
type TaskNode struct {
	ID          string         `json:"id"`
	Description string         `json:"description"`
	Status      PlanStepStatus `json:"status"`
	Children    []TaskNode     `json:"children,omitempty"`
	DependsOn   []string       `json:"depends_on,omitempty"`
	ToolsUsed   []string       `json:"tools_used,omitempty"`
	Evidence    []string       `json:"evidence,omitempty"`
}

type PlanStep struct {
	ID          int            `json:"id"`
	Description string         `json:"description"`
	Phase       string         `json:"phase,omitempty"`
	DependsOn   []int          `json:"depends_on,omitempty"`
	Status      PlanStepStatus `json:"status"`
	ToolsUsed   []string       `json:"tools_used,omitempty"`
	Evidence    []string       `json:"evidence,omitempty"`
}

func latestPlan(events []history.Event) *Plan {
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type != history.EventPlanUpdate || event.Metadata == nil {
			continue
		}
		raw, ok := event.Metadata["plan"]
		if !ok {
			continue
		}
		data, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		var plan Plan
		if err := json.Unmarshal(data, &plan); err != nil {
			continue
		}
		plan.rebuildMaps()
		return &plan
	}
	return nil
}

func planDescriptions(action modelAction) []string {
	if len(action.Plan) > 0 {
		return append([]string(nil), action.Plan...)
	}
	descriptions := make([]string, 0, len(action.Actions))
	for _, item := range action.Actions {
		description := firstNonEmpty(item.Purpose, item.ExpectedResult, "Run "+firstNonEmpty(item.Name, item.Tool, "tool"))
		descriptions = append(descriptions, description)
	}
	return descriptions
}

func newPlan(goal string, descriptions []string) *Plan {
	plan := &Plan{Goal: strings.TrimSpace(goal), Completed: map[int]bool{}, Evidence: map[int][]string{}, RevisedAt: time.Now()}
	for _, description := range descriptions {
		description = strings.TrimSpace(description)
		if description == "" {
			continue
		}
		plan.Steps = append(plan.Steps, PlanStep{ID: len(plan.Steps) + 1, Description: description, Phase: inferPlanPhase(description, ""), Status: PlanPending})
	}
	return plan
}

func (p *Plan) sync(goal string, descriptions []string) bool {
	if p == nil {
		return false
	}
	goal = strings.TrimSpace(goal)
	clean := make([]string, 0, len(descriptions))
	for _, description := range descriptions {
		if description = strings.TrimSpace(description); description != "" {
			clean = append(clean, description)
		}
	}
	if goal == p.Goal && samePlanDescriptions(p.Steps, clean) {
		return false
	}
	previous := make(map[string]PlanStep, len(p.Steps))
	for _, step := range p.Steps {
		previous[strings.ToLower(step.Description)] = step
	}
	p.Goal = firstNonEmpty(goal, p.Goal)
	p.Steps = p.Steps[:0]
	for _, description := range clean {
		step, ok := previous[strings.ToLower(description)]
		if !ok {
			step = PlanStep{Status: PlanPending}
		}
		step.ID = len(p.Steps) + 1
		step.Description = description
		if step.Phase == "" {
			step.Phase = inferPlanPhase(description, "")
		}
		p.Steps = append(p.Steps, step)
	}
	p.rebuildMaps()
	p.RevisedAt = time.Now()
	return true
}

func samePlanDescriptions(steps []PlanStep, descriptions []string) bool {
	if len(steps) != len(descriptions) {
		return false
	}
	for index := range steps {
		if strings.TrimSpace(steps[index].Description) != strings.TrimSpace(descriptions[index]) {
			return false
		}
	}
	return true
}

func (p *Plan) rebuildMaps() {
	p.Completed = map[int]bool{}
	p.Evidence = map[int][]string{}
	for _, step := range p.Steps {
		if step.Status == PlanDone || step.Status == PlanSkipped {
			p.Completed[step.ID] = true
		}
		if len(step.Evidence) > 0 {
			p.Evidence[step.ID] = append([]string(nil), step.Evidence...)
		}
	}
	p.rebuildTree()
}

func (p *Plan) rebuildTree() {
	if p == nil {
		return
	}
	type phaseGroup struct {
		index int
		name  string
	}
	groups := map[string]phaseGroup{}
	var tree []TaskNode
	for _, step := range p.Steps {
		phase := firstNonEmpty(step.Phase, "Execution")
		key := strings.ToLower(strings.ReplaceAll(phase, " ", "-"))
		group, ok := groups[key]
		if !ok {
			group = phaseGroup{index: len(tree), name: phase}
			groups[key] = group
			tree = append(tree, TaskNode{ID: "phase-" + key, Description: phase, Status: PlanPending})
		}
		dependencies := make([]string, 0, len(step.DependsOn))
		for _, dependency := range step.DependsOn {
			dependencies = append(dependencies, fmt.Sprintf("task-%d", dependency))
		}
		tree[group.index].Children = append(tree[group.index].Children, TaskNode{
			ID:          fmt.Sprintf("task-%d", step.ID),
			Description: step.Description,
			Status:      step.Status,
			DependsOn:   dependencies,
			ToolsUsed:   append([]string(nil), step.ToolsUsed...),
			Evidence:    append([]string(nil), step.Evidence...),
		})
	}
	for index := range tree {
		tree[index].Status = aggregateTaskStatus(tree[index].Children)
	}
	p.Tree = tree
}

func aggregateTaskStatus(children []TaskNode) PlanStepStatus {
	if len(children) == 0 {
		return PlanPending
	}
	allComplete := true
	hasRunning := false
	for _, child := range children {
		switch child.Status {
		case PlanFailed:
			return PlanFailed
		case PlanRunning:
			hasRunning = true
			allComplete = false
		case PlanDone, PlanSkipped:
		default:
			allComplete = false
		}
	}
	if hasRunning {
		return PlanRunning
	}
	if allComplete {
		return PlanDone
	}
	return PlanPending
}

func (p *Plan) applyDependencies(actions []modelToolAction) {
	if p == nil || len(actions) == 0 {
		return
	}
	ids := make(map[string]int, len(actions))
	for index, action := range actions {
		ids[action.ID] = index + 1
	}
	for index := range p.Steps {
		p.Steps[index].DependsOn = nil
		if index >= len(actions) {
			continue
		}
		for _, dependency := range actions[index].DependsOn {
			if id := ids[strings.TrimSpace(dependency)]; id > 0 {
				p.Steps[index].DependsOn = append(p.Steps[index].DependsOn, id)
			}
		}
		p.Steps[index].Phase = inferPlanPhase(p.Steps[index].Description, firstNonEmpty(actions[index].Name, actions[index].Tool))
	}
	p.RevisedAt = time.Now()
}

func (p *Plan) stepForAction(actionIndex int) *PlanStep {
	if p == nil || len(p.Steps) == 0 {
		return nil
	}
	if actionIndex >= 0 && actionIndex < len(p.Steps) && p.Steps[actionIndex].Status != PlanDone {
		return &p.Steps[actionIndex]
	}
	for index := range p.Steps {
		if p.Steps[index].Status == PlanPending || p.Steps[index].Status == PlanRunning || p.Steps[index].Status == PlanFailed {
			return &p.Steps[index]
		}
	}
	return &p.Steps[len(p.Steps)-1]
}

func (p *Plan) markStarted(actionIndex int, tool string) {
	step := p.stepForAction(actionIndex)
	if step == nil {
		return
	}
	step.Status = PlanRunning
	if tool != "" && !containsString(step.ToolsUsed, tool) {
		step.ToolsUsed = append(step.ToolsUsed, tool)
	}
	p.RevisedAt = time.Now()
	p.rebuildMaps()
}

func (p *Plan) markResult(actionIndex int, call tools.Call, result tools.Result) {
	step := p.stepForAction(actionIndex)
	if step == nil {
		return
	}
	if !containsString(step.ToolsUsed, call.Name) {
		step.ToolsUsed = append(step.ToolsUsed, call.Name)
	}
	evidence := strings.TrimSpace(result.Output)
	if !result.OK {
		step.Status = PlanFailed
		evidence = firstNonEmpty(strings.TrimSpace(result.Error), evidence, "tool failed")
	} else {
		step.Status = PlanDone
	}
	if evidence != "" {
		step.Evidence = append(step.Evidence, compact(evidence, 360))
	}
	p.RevisedAt = time.Now()
	p.rebuildMaps()
}

// Render returns a compact, user-visible execution plan.
func (p *Plan) Render() string {
	return p.render()
}

func (p *Plan) render() string {
	if p == nil {
		return ""
	}
	var lines []string
	if p.Goal != "" {
		lines = append(lines, "Goal: "+p.Goal)
	}
	if len(p.Tree) == 0 && len(p.Steps) > 0 {
		p.rebuildTree()
	}
	for _, phase := range p.Tree {
		lines = append(lines, fmt.Sprintf("  [%s] %s", planStatusMark(phase.Status), phase.Description))
		for childIndex, task := range phase.Children {
			branch := "├─"
			if childIndex == len(phase.Children)-1 {
				branch = "└─"
			}
			line := fmt.Sprintf("  %s [%s] %s. %s", branch, planStatusMark(task.Status), strings.TrimPrefix(task.ID, "task-"), task.Description)
			if len(task.DependsOn) > 0 {
				line += " ← " + strings.Join(task.DependsOn, ", ")
			}
			if len(task.ToolsUsed) > 0 {
				line += " (" + strings.Join(task.ToolsUsed, ", ") + ")"
			}
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func planStatusMark(status PlanStepStatus) string {
	switch status {
	case PlanDone:
		return "x"
	case PlanRunning:
		return ">"
	case PlanFailed:
		return "!"
	case PlanSkipped:
		return "-"
	default:
		return " "
	}
}

func inferPlanPhase(description, tool string) string {
	text := strings.ToLower(strings.TrimSpace(description + " " + tool))
	switch {
	case strings.Contains(text, "verify"), strings.Contains(text, "test"), strings.Contains(text, "lint"), strings.Contains(text, "audit"), strings.Contains(text, "diff"):
		return "Verification"
	case strings.Contains(text, "write"), strings.Contains(text, "patch"), strings.Contains(text, "replace"), strings.Contains(text, "implement"), strings.Contains(text, "fix"), strings.Contains(text, "format"):
		return "Implementation"
	case strings.Contains(text, "review"), strings.Contains(text, "summar"), strings.Contains(text, "report"), strings.Contains(text, "deliver"):
		return "Review"
	default:
		return "Investigation"
	}
}

func (p *Plan) event(runID string, iteration int, status string) history.Event {
	event := runEvent(runID, iteration, history.EventPlanUpdate, "Plan", p.render(), "", status)
	event.Metadata["plan"] = p
	return event
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
