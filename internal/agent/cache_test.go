package agent

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/llm"
	"github.com/ephemera-ai/ephemera/internal/tools"
)

func TestSemanticReadCacheCanonicalizesDefaultsAndChecksContent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	content := []byte("package main\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	runner := NewRunner(cfg, nil)
	state := &runState{
		resultCache:     map[string]cachedToolResult{},
		suppressedTools: map[string]bool{},
		completedCalls:  map[string]int{},
		inspectedPaths:  map[string]bool{},
		changedPaths:    map[string]bool{},
	}
	first := tools.Call{Name: "read_file", Arguments: map[string]any{"path": "main.go"}}
	hash := sha256.Sum256(content)
	state.observe(first, tools.Result{Tool: "read_file", OK: true, Output: "1: package main", Metadata: map[string]any{
		"risk": "read", "path": "main.go", "content_sha256": fmt.Sprintf("%x", hash[:]), "semantic_cache_key": semanticToolFingerprint(first),
	}})

	equivalent := tools.Call{Name: "read_file", Arguments: map[string]any{"path": "main.go", "start_line": 1, "end_line": 0}}
	if _, ok := runner.cachedReadResult(state, equivalent); !ok {
		t.Fatal("equivalent read_file call missed semantic cache")
	}
	state.workspaceRevision++ // An unrelated workspace write should not invalidate unchanged file content.
	if _, ok := runner.cachedReadResult(state, equivalent); !ok {
		t.Fatal("unchanged file content was not reused across workspace revision")
	}
	if err := os.WriteFile(path, []byte("package changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := runner.cachedReadResult(state, equivalent); ok {
		t.Fatal("changed file content returned a stale cached read")
	}
}

func BenchmarkContextWindowFitCached(b *testing.B) {
	messages := make([]llm.Message, 0, 100)
	for index := 0; index < 100; index++ {
		messages = append(messages, llm.Message{Role: "user", Content: fmt.Sprintf("message %d about internal/agent/context_window.go and caching", index)})
	}
	window := ContextWindow{
		System:         "system",
		Budget:         16_000,
		SummaryTokens:  2_000,
		RecallMessages: 8,
		Provider:       "openai",
		Iteration:      4,
		MaxIterations:  10,
		Query:          "optimize context_window.go caching",
		WorkingMemory:  "preserve current evidence",
		Messages:       messages,
		Cache:          NewContextFitCache(),
	}
	window.Fit()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		window.Fit()
	}
}
