package reasoning

import "strings"

// Complexity is a coarse task-cost estimate used to choose a reasoning mode.
type Complexity string

const (
	ComplexitySimple  Complexity = "simple"
	ComplexityMedium  Complexity = "medium"
	ComplexityComplex Complexity = "complex"
)

// ToolNode describes one planned tool call and its dependencies.
type ToolNode struct {
	Name      string
	DependsOn []string
	Risk      string
	Path      string
}

// ToolGraph captures execution shape that keyword-only classification misses.
type ToolGraph struct {
	Calls          []ToolNode
	CrossFileScope int
}

// ClassifyComplexity uses deterministic, zero-token heuristics.
func ClassifyComplexity(prompt string) Complexity {
	return ClassifyComplexityWithTools(prompt, ToolGraph{})
}

// ClassifyComplexityWithTools adds call count, dependency depth, write risk,
// and cross-file scope to prompt heuristics.
func ClassifyComplexityWithTools(prompt string, graph ToolGraph) Complexity {
	text := strings.ToLower(strings.TrimSpace(prompt))
	if text == "" && len(graph.Calls) == 0 {
		return ComplexitySimple
	}
	words := len(strings.Fields(text))
	complexMarkers := []string{
		"multi-file", "multiple files", "refactor", "migration", "architecture",
		"implement", "build", "benchmark", "provider", "integration", "end-to-end",
		"tests and", "with tests", "security", "performance", "agentic", "execute plan",
	}
	mediumMarkers := []string{
		"fix", "debug", "why", "analyze", "review", "change", "update", "add ",
		"error", "issue", "code", "test", "compare", "explain",
	}
	complexScore := 0
	for _, marker := range complexMarkers {
		if strings.Contains(text, marker) {
			complexScore++
		}
	}
	if words > 120 {
		complexScore += 2
	} else if words > 55 {
		complexScore++
	}

	if len(graph.Calls) >= 5 {
		complexScore += 2
	} else if len(graph.Calls) >= 3 {
		complexScore++
	}
	if dependencyDepth(graph.Calls) >= 3 {
		complexScore += 2
	} else if dependencyDepth(graph.Calls) == 2 {
		complexScore++
	}
	writeCount := 0
	paths := map[string]bool{}
	for _, call := range graph.Calls {
		risk := strings.ToLower(strings.TrimSpace(call.Risk))
		if risk == "write" || risk == "shell" {
			writeCount++
		}
		if path := strings.TrimSpace(call.Path); path != "" {
			paths[path] = true
		}
	}
	if writeCount >= 2 {
		complexScore++
	}
	scope := graph.CrossFileScope
	if len(paths) > scope {
		scope = len(paths)
	}
	if scope >= 3 {
		complexScore += 2
	} else if scope == 2 {
		complexScore++
	}
	if complexScore >= 2 {
		return ComplexityComplex
	}
	for _, marker := range mediumMarkers {
		if strings.Contains(text, marker) {
			return ComplexityMedium
		}
	}
	if len(graph.Calls) > 1 || writeCount > 0 {
		return ComplexityMedium
	}
	if words <= 12 && !strings.ContainsAny(text, "{}[]`\n") {
		return ComplexitySimple
	}
	return ComplexityMedium
}

func dependencyDepth(calls []ToolNode) int {
	if len(calls) == 0 {
		return 0
	}
	byID := make(map[string]ToolNode, len(calls))
	for _, call := range calls {
		if id := strings.TrimSpace(call.Name); id != "" {
			byID[id] = call
		}
	}
	memo := map[string]int{}
	visiting := map[string]bool{}
	var depth func(string) int
	depth = func(id string) int {
		if value := memo[id]; value > 0 {
			return value
		}
		if visiting[id] {
			return 1
		}
		visiting[id] = true
		best := 1
		for _, dep := range byID[id].DependsOn {
			if _, ok := byID[dep]; ok {
				if candidate := 1 + depth(dep); candidate > best {
					best = candidate
				}
			}
		}
		visiting[id] = false
		memo[id] = best
		return best
	}
	best := 1
	for id := range byID {
		if value := depth(id); value > best {
			best = value
		}
	}
	return best
}

// AdaptiveMode respects explicit non-normal user modes. Normal mode acts as
// automatic mode when adaptive selection is enabled.
func AdaptiveMode(configured Mode, prompt string, enabled bool) Mode {
	return AdaptiveModeWithTools(configured, prompt, enabled, ToolGraph{})
}

// AdaptiveModeWithTools can be called after planning to deepen reasoning when
// the actual tool graph is more complex than the original prompt suggested.
func AdaptiveModeWithTools(configured Mode, prompt string, enabled bool, graph ToolGraph) Mode {
	if !enabled || configured != ModeNormal {
		if configured.Valid() {
			return configured
		}
		return ModeNormal
	}
	switch ClassifyComplexityWithTools(prompt, graph) {
	case ComplexitySimple:
		return ModeConcise
	case ComplexityComplex:
		return ModeDeep
	default:
		return ModeNormal
	}
}
