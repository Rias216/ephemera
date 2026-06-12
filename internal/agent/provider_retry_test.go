package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/llm"
)

type malformedNativeToolProvider struct {
	name          string
	nativeCalls   int
	portableCalls int
	lastRequest   llm.Request
}

func (p *malformedNativeToolProvider) Name() string {
	if strings.TrimSpace(p.name) != "" {
		return p.name
	}
	return "nvidia-test"
}

func (p *malformedNativeToolProvider) Generate(_ context.Context, req llm.Request) (string, error) {
	p.portableCalls++
	p.lastRequest = req
	return `{"text":"recover","tool_calls":[{"name":"read_file","arguments":{"path":"go.mod"}}]}`, nil
}

func (p *malformedNativeToolProvider) GenerateWithTools(_ context.Context, _ llm.Request, _ []llm.ToolSpec, emit llm.DeltaFunc) (llm.ToolDecision, error) {
	p.nativeCalls++
	if emit != nil {
		_ = emit(llm.Delta{Kind: llm.DeltaActivity, Text: "Preparing apply_patch"})
	}
	return llm.ToolDecision{}, &llm.ToolProtocolError{Provider: p.Name(), Tool: "apply_patch", Cause: errors.New("unexpected EOF")}
}

func TestProviderRetryFallsBackToUniversalToolsWithoutConsumingRetryBudget(t *testing.T) {
	cfg := config.Default()
	cfg.ProviderMaxRetries = 0
	provider := &malformedNativeToolProvider{}
	runner := Runner{Config: cfg, Provider: provider}
	decision, err := runner.generateToolDecisionWithRetry(context.Background(), llm.Request{
		System:   "base",
		Messages: []llm.Message{{Role: "user", Content: "inspect"}},
	}, []llm.ToolSpec{{Name: "read_file", Parameters: llm.ToolSchema{Type: "object"}}}, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if provider.nativeCalls != 1 || provider.portableCalls != 1 {
		t.Fatalf("native=%d portable=%d", provider.nativeCalls, provider.portableCalls)
	}
	if decision.Transport != llm.ToolTransportPortable || len(decision.ToolCalls) != 1 || decision.ToolCalls[0].Name != "read_file" {
		t.Fatalf("decision = %#v", decision)
	}
	if !strings.Contains(provider.lastRequest.System, "UNIVERSAL TOOL GATEWAY") {
		t.Fatal("portable gateway prompt was not used")
	}
}

func TestProviderRetryCanStayInPortableModeForLaterIterations(t *testing.T) {
	cfg := config.Default()
	cfg.ProviderMaxRetries = 0
	provider := &malformedNativeToolProvider{}
	runner := Runner{Config: cfg, Provider: provider}
	_, err := runner.generateToolDecisionWithRetry(context.Background(), llm.Request{System: "base"}, []llm.ToolSpec{{Name: "read_file", Parameters: llm.ToolSchema{Type: "object"}}}, true, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if provider.nativeCalls != 0 || provider.portableCalls != 1 {
		t.Fatalf("native=%d portable=%d", provider.nativeCalls, provider.portableCalls)
	}
}

func TestProviderToolFallbackIsSharedByLaterAgents(t *testing.T) {
	cfg := config.Default()
	cfg.ProviderMaxRetries = 0
	firstProvider := &malformedNativeToolProvider{name: "nvidia-persistent-test"}
	providerPortableToolModes.Delete(providerToolModeKey(firstProvider))
	t.Cleanup(func() { providerPortableToolModes.Delete(providerToolModeKey(firstProvider)) })

	first := Runner{Config: cfg, Provider: firstProvider}
	_, err := first.generateToolDecisionWithRetry(context.Background(), llm.Request{System: "base"}, []llm.ToolSpec{{Name: "read_file", Parameters: llm.ToolSchema{Type: "object"}}}, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !prefersPortableTools(firstProvider) {
		t.Fatal("provider tool compatibility was not remembered")
	}

	laterProvider := &malformedNativeToolProvider{name: "nvidia-persistent-test"}
	later := Runner{Config: cfg, Provider: laterProvider}
	state := later.initialState(history.Session{}, time.Now())
	if !state.portableTools {
		t.Fatal("later agent did not inherit universal tool mode")
	}
	_, err = later.generateToolDecisionWithRetry(context.Background(), llm.Request{System: "base"}, []llm.ToolSpec{{Name: "read_file", Parameters: llm.ToolSchema{Type: "object"}}}, state.portableTools, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if laterProvider.nativeCalls != 0 || laterProvider.portableCalls != 1 {
		t.Fatalf("later native=%d portable=%d", laterProvider.nativeCalls, laterProvider.portableCalls)
	}
}
