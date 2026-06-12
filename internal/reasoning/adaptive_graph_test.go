package reasoning

import "testing"

func TestToolGraphEscalatesSimplePrompt(t *testing.T) {
	graph := ToolGraph{Calls: []ToolNode{
		{Name: "read", Path: "a.go"},
		{Name: "edit", DependsOn: []string{"read"}, Risk: "write", Path: "a.go"},
		{Name: "test", DependsOn: []string{"edit"}, Risk: "shell", Path: "b.go"},
	}, CrossFileScope: 3}
	if got := ClassifyComplexityWithTools("fix it", graph); got != ComplexityComplex {
		t.Fatalf("complexity = %q", got)
	}
	if got := AdaptiveModeWithTools(ModeNormal, "fix it", true, graph); got != ModeDeep {
		t.Fatalf("mode = %q", got)
	}
}
