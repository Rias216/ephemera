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
	if provider.nativeCalls != 2 || provider.portableCalls != 1 {
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
	_, err := runner.generateToolDecisionWithRetry(context.Background(), llm.Request{System: "base", Model: cfg.Model()}, []llm.ToolSpec{{Name: "read_file", Parameters: llm.ToolSchema{Type: "object"}}}, true, nil, nil)
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
	providerPortableToolModes.Delete(providerToolModeKey(firstProvider, cfg.Model()))
	t.Cleanup(func() { providerPortableToolModes.Delete(providerToolModeKey(firstProvider, cfg.Model())) })

	first := Runner{Config: cfg, Provider: firstProvider}
	_, err := first.generateToolDecisionWithRetry(context.Background(), llm.Request{System: "base", Model: cfg.Model()}, []llm.ToolSpec{{Name: "read_file", Parameters: llm.ToolSchema{Type: "object"}}}, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !prefersPortableTools(firstProvider, cfg.Model()) {
		t.Fatal("provider tool compatibility was not remembered")
	}

	laterProvider := &malformedNativeToolProvider{name: "nvidia-persistent-test"}
	later := Runner{Config: cfg, Provider: laterProvider}
	state := later.initialState(history.Session{}, time.Now())
	if !state.portableTools {
		t.Fatal("later agent did not inherit universal tool mode")
	}
	_, err = later.generateToolDecisionWithRetry(context.Background(), llm.Request{System: "base", Model: cfg.Model()}, []llm.ToolSpec{{Name: "read_file", Parameters: llm.ToolSchema{Type: "object"}}}, state.portableTools, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if laterProvider.nativeCalls != 0 || laterProvider.portableCalls != 1 {
		t.Fatalf("later native=%d portable=%d", laterProvider.nativeCalls, laterProvider.portableCalls)
	}
}

type recoveringNativeToolProvider struct {
	nativeCalls   int
	portableCalls int
}

func (p *recoveringNativeToolProvider) Name() string { return "nvidia-recovery-test" }

func (p *recoveringNativeToolProvider) Generate(_ context.Context, _ llm.Request) (string, error) {
	p.portableCalls++
	return `{"text":"unexpected portable fallback"}`, nil
}

func (p *recoveringNativeToolProvider) GenerateWithTools(_ context.Context, _ llm.Request, _ []llm.ToolSpec, _ llm.DeltaFunc) (llm.ToolDecision, error) {
	p.nativeCalls++
	if p.nativeCalls == 1 {
		return llm.ToolDecision{}, &llm.ToolProtocolError{Provider: p.Name(), Tool: "read_file", Cause: errors.New("unexpected EOF")}
	}
	return llm.ToolDecision{
		Transport: llm.ToolTransportNative,
		ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "read_file", Arguments: map[string]any{"path": "go.mod"}}},
	}, nil
}

func TestProviderRetryRecoversWithOneFreshNativeCall(t *testing.T) {
	cfg := config.Default()
	cfg.ProviderMaxRetries = 0
	provider := &recoveringNativeToolProvider{}
	providerPortableToolModes.Delete(providerToolModeKey(provider, cfg.Model()))
	t.Cleanup(func() { providerPortableToolModes.Delete(providerToolModeKey(provider, cfg.Model())) })

	runner := Runner{Config: cfg, Provider: provider}
	decision, err := runner.generateToolDecisionWithRetry(context.Background(), llm.Request{System: "base", Model: cfg.Model()}, []llm.ToolSpec{{Name: "read_file", Parameters: llm.ToolSchema{Type: "object"}}}, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if provider.nativeCalls != 2 || provider.portableCalls != 0 {
		t.Fatalf("native=%d portable=%d", provider.nativeCalls, provider.portableCalls)
	}
	if decision.Transport != llm.ToolTransportNative || len(decision.ToolCalls) != 1 {
		t.Fatalf("decision = %#v", decision)
	}
	if prefersPortableTools(provider, cfg.Model()) {
		t.Fatal("successful native recovery should not permanently force portable mode")
	}
}

type emptyThenHealthyProvider struct {
	calls int
}

func (p *emptyThenHealthyProvider) Name() string { return "nvidia-empty-test" }

func (p *emptyThenHealthyProvider) Generate(_ context.Context, _ llm.Request) (string, error) {
	p.calls++
	if p.calls == 1 {
		return "", errors.New("nvidia returned an empty streaming response")
	}
	return "recovered", nil
}

func TestProviderRetryTreatsEmptyStreamAsTransient(t *testing.T) {
	cfg := config.Default()
	cfg.ProviderMaxRetries = 1
	cfg.ProviderRetryBackoffMS = 1
	provider := &emptyThenHealthyProvider{}
	runner := Runner{Config: cfg, Provider: provider}

	decision, err := runner.generateToolDecisionWithRetry(context.Background(), llm.Request{Model: "test"}, nil, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls != 2 {
		t.Fatalf("provider calls = %d, want 2", provider.calls)
	}
	if decision.Text != "recovered" {
		t.Fatalf("decision text = %q", decision.Text)
	}
}

type countingHealthProvider struct {
	name   string
	checks int
	err    error
}

func (p *countingHealthProvider) Name() string { return p.name }
func (p *countingHealthProvider) Generate(context.Context, llm.Request) (string, error) {
	return "ok", nil
}
func (p *countingHealthProvider) HealthCheck(context.Context) error {
	p.checks++
	return p.err
}

func TestProviderHealthCheckCachesSuccessAndFailure(t *testing.T) {
	healthy := &countingHealthProvider{name: "health-cache-success"}
	providerHealthCache.Delete(providerHealthKey(healthy))
	t.Cleanup(func() { providerHealthCache.Delete(providerHealthKey(healthy)) })
	if err := checkProviderHealth(context.Background(), healthy); err != nil {
		t.Fatal(err)
	}
	if err := checkProviderHealth(context.Background(), healthy); err != nil {
		t.Fatal(err)
	}
	if healthy.checks != 1 {
		t.Fatalf("healthy checks = %d, want 1", healthy.checks)
	}

	unhealthy := &countingHealthProvider{name: "health-cache-failure", err: errors.New("offline")}
	providerHealthCache.Delete(providerHealthKey(unhealthy))
	t.Cleanup(func() { providerHealthCache.Delete(providerHealthKey(unhealthy)) })
	for range 2 {
		if err := checkProviderHealth(context.Background(), unhealthy); err == nil || !strings.Contains(err.Error(), "offline") {
			t.Fatalf("cached health error = %v", err)
		}
	}
	if unhealthy.checks != 1 {
		t.Fatalf("unhealthy checks = %d, want 1", unhealthy.checks)
	}
}

func TestPortableToolCompatibilityIsIsolatedByModel(t *testing.T) {
	provider := &malformedNativeToolProvider{name: "model-isolation-test"}
	providerPortableToolModes.Delete(providerToolModeKey(provider, "model-a"))
	providerPortableToolModes.Delete(providerToolModeKey(provider, "model-b"))
	t.Cleanup(func() {
		providerPortableToolModes.Delete(providerToolModeKey(provider, "model-a"))
		providerPortableToolModes.Delete(providerToolModeKey(provider, "model-b"))
	})

	rememberPortableTools(provider, "model-a")
	if !prefersPortableTools(provider, "model-a") {
		t.Fatal("model-a compatibility was not remembered")
	}
	if prefersPortableTools(provider, "model-b") {
		t.Fatal("model-a compatibility incorrectly poisoned model-b")
	}
}
