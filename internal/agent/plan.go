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
	Completed map[int]bool     `json:"completed,omitempty"`
	Evidence  map[int][]string `json:"evidence,omitempty"`
	RevisedAt time.Time        `json:"revised_at"`
}

type PlanStep struct {
	ID          int            `json:"id"`
	Description string         `json:"description"`
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
		plan.Steps = append(plan.Steps, PlanStep{ID: len(plan.Steps) + 1, Description: description, Status: PlanPending})
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
	for _, step := range p.Steps {
		mark := " "
		switch step.Status {
		case PlanDone:
			mark = "x"
		case PlanRunning:
			mark = ">"
		case PlanFailed:
			mark = "!"
		case PlanSkipped:
			mark = "-"
		}
		line := fmt.Sprintf("- [%s] %d. %s", mark, step.ID, step.Description)
		if len(step.ToolsUsed) > 0 {
			line += " (" + strings.Join(step.ToolsUsed, ", ") + ")"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
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
