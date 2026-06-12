package llm

import (
	"strings"
	"testing"
)

func TestCodexBridgeUsesIsolatedModelOnlyArguments(t *testing.T) {
	provider := NewCodex(2048)
	req := Request{Model: "gpt-test", ReasoningEffort: "medium", ReasoningSummary: true}
	args := provider.execArgs(req, "/tmp/final.txt", true, true)
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"exec", "--json", "--sandbox workspace-write", "--ephemeral", "--skip-git-repo-check",
		"--ignore-rules", "--ignore-user-config", `approval_policy="never"`,
		`features.shell_tool=false`, `features.multi_agent=false`, `project_doc_max_bytes=0`,
		`model_reasoning_effort="low"`, `model_reasoning_summary="concise"`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("optimized Codex bridge args missing %q: %s", want, joined)
		}
	}
	if args[len(args)-1] != "-" {
		t.Fatalf("prompt source = %q, want stdin marker", args[len(args)-1])
	}
}

func TestCodexBridgeCompatibilityArgumentsAvoidNewFlags(t *testing.T) {
	provider := NewCodex(2048)
	args := provider.execArgs(Request{Model: "gpt-test"}, "/tmp/final.txt", false, false)
	joined := strings.Join(args, " ")

	for _, forbidden := range []string{"--ignore-rules", "--ignore-user-config", "features.shell_tool", "model_reasoning_effort"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("compatibility args unexpectedly contain %q: %s", forbidden, joined)
		}
	}
	for _, want := range []string{"exec", "--model gpt-test", "--sandbox workspace-write", "--ephemeral", "--output-last-message /tmp/final.txt"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("compatibility args missing %q: %s", want, joined)
		}
	}
}

func TestCodexBridgePromptDelegatesWorkspaceAuthorityToEphemera(t *testing.T) {
	provider := NewCodex(1200)
	prompt := provider.prompt(Request{
		System:    "Use the Ephemera tool protocol.",
		Messages:  []Message{{Role: "user", Content: "Change the file."}},
		MaxTokens: 9000,
	})

	for _, want := range []string{
		"The surrounding Ephemera process is the only agent",
		"Ephemera process is the only agent",
		"Do not inspect the current directory",
		"request it only through the Ephemera tool protocol",
		"near or below 1200 tokens",
		"EPHEMERA SYSTEM INSTRUCTIONS",
		"Change the file.",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("bridge prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestCodexBridgeReasoningEffortIsEconomical(t *testing.T) {
	cases := map[string]string{
		"":        "low",
		"medium":  "low",
		"high":    "high",
		"xhigh":   "high",
		"minimal": "minimal",
	}
	for input, want := range cases {
		if got := codexBridgeReasoningEffort(input); got != want {
			t.Fatalf("codexBridgeReasoningEffort(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCodexBridgeCompatibilityFailureDetection(t *testing.T) {
	for _, message := range []string{
		"error: unexpected argument '--ignore-rules'",
		"failed to parse config override",
		"unknown feature shell_tool",
		"unrecognized field model_verbosity",
	} {
		if !codexBridgeCompatibilityFailure([]byte(message)) {
			t.Fatalf("compatibility error was not recognized: %q", message)
		}
	}
	if codexBridgeCompatibilityFailure([]byte("authentication failed")) {
		t.Fatal("unrelated provider error should not trigger compatibility fallback")
	}
}

func TestNewCodexBoundsBridgeBudget(t *testing.T) {
	if got := NewCodex(10).bridgeMaxTokens; got != defaultCodexBridgeMaxTokens {
		t.Fatalf("small budget = %d, want default %d", got, defaultCodexBridgeMaxTokens)
	}
	if got := NewCodex(99_000).bridgeMaxTokens; got != 8_000 {
		t.Fatalf("large budget = %d, want 8000", got)
	}
}
