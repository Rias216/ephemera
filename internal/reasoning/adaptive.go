package reasoning

import "strings"

// Complexity is a coarse task-cost estimate used to choose a reasoning mode.
type Complexity string

const (
	ComplexitySimple  Complexity = "simple"
	ComplexityMedium  Complexity = "medium"
	ComplexityComplex Complexity = "complex"
)

// ClassifyComplexity uses deterministic, zero-token heuristics. It deliberately
// favors medium complexity so short greetings never enter an agent/tool loop,
// while multi-file implementation requests receive deeper reasoning.
func ClassifyComplexity(prompt string) Complexity {
	text := strings.ToLower(strings.TrimSpace(prompt))
	if text == "" {
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
	if complexScore >= 2 {
		return ComplexityComplex
	}
	for _, marker := range mediumMarkers {
		if strings.Contains(text, marker) {
			return ComplexityMedium
		}
	}
	if words <= 12 && !strings.ContainsAny(text, "{}[]`\n") {
		return ComplexitySimple
	}
	return ComplexityMedium
}

// AdaptiveMode respects explicit non-normal user modes. Normal mode acts as
// automatic mode when adaptive selection is enabled.
func AdaptiveMode(configured Mode, prompt string, enabled bool) Mode {
	if !enabled || configured != ModeNormal {
		if configured.Valid() {
			return configured
		}
		return ModeNormal
	}
	switch ClassifyComplexity(prompt) {
	case ComplexitySimple:
		return ModeConcise
	case ComplexityComplex:
		return ModeDeep
	default:
		return ModeNormal
	}
}
