package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/llm"
)

type streamingFakeProvider struct {
	chunks    []string
	reasoning []string
}

func (p streamingFakeProvider) Name() string { return "fake" }
func (p streamingFakeProvider) Generate(context.Context, llm.Request) (string, error) {
	return strings.Join(p.chunks, ""), nil
}
func (p streamingFakeProvider) GenerateStream(_ context.Context, _ llm.Request, emit llm.DeltaFunc) (string, error) {
	var out strings.Builder
	for _, chunk := range p.reasoning {
		if err := emit(llm.Delta{Kind: llm.DeltaReasoning, Text: chunk}); err != nil {
			return "", err
		}
	}
	for _, chunk := range p.chunks {
		out.WriteString(chunk)
		if err := emit(llm.Delta{Kind: llm.DeltaText, Text: chunk}); err != nil {
			return "", err
		}
	}
	return out.String(), nil
}

func TestRunStreamPublishesDecisionAndCompletion(t *testing.T) {
	cfg := config.Default()
	cfg.Provider = "ollama"
	cfg.AgentEnabled = true
	provider := streamingFakeProvider{
		reasoning: []string{"Inspecting the current state. "},
		chunks: []string{
			`{"reasoning":{"goal":"repair renderer","assumptions":[],`,
			`"approach":["inspect","patch"],"tool_rationale":"none","verification":"test"},`,
			`"summary":"done","plan":["inspect"],"actions":[],"final":"complete"}`,
		},
	}
	session := history.New("stream-test", cfg.Provider, cfg.Model(), cfg.Mode)
	session.Append("user", "fix it")

	var updates []StreamUpdate
	result := NewRunner(cfg, provider).RunStream(context.Background(), session, func(update StreamUpdate) {
		updates = append(updates, update)
	})
	if result.Text != "complete" {
		t.Fatalf("unexpected result: %q", result.Text)
	}
	var deltas, liveReasoning, reasoningEvent, done bool
	for _, update := range updates {
		switch update.Kind {
		case StreamDelta:
			deltas = true
		case StreamReasoning:
			liveReasoning = true
		case StreamEvent:
			if update.Event != nil && update.Event.Type == "reasoning_trace" {
				reasoningEvent = true
			}
		case StreamDone:
			done = true
		}
	}
	if !deltas || !liveReasoning || !reasoningEvent || !done {
		t.Fatalf("missing live updates: deltas=%t live_reasoning=%t reasoning_event=%t done=%t", deltas, liveReasoning, reasoningEvent, done)
	}
}

func TestSelectAgentMessagesHonorsBudget(t *testing.T) {
	var messages []history.Message
	for index := 0; index < 12; index++ {
		role := "user"
		if index%2 == 1 {
			role = "assistant"
		}
		messages = append(messages, history.Message{Role: role, Content: strings.Repeat("x", 400)})
	}
	selected, stats := selectAgentMessages(messages, strings.Repeat("s", 200), 700)
	if len(selected) == 0 {
		t.Fatal("expected recent messages to be retained")
	}
	if stats.Dropped == 0 {
		t.Fatalf("expected context trimming, got %+v", stats)
	}
	if selected[0].Role == "assistant" {
		t.Fatal("selection must begin with a user message")
	}
}

func TestSelectAgentMessagesAddsDeterministicCompactionSummary(t *testing.T) {
	var messages []history.Message
	for index := 0; index < 16; index++ {
		role := "user"
		if index%2 == 1 {
			role = "assistant"
		}
		messages = append(messages, history.Message{Role: role, Content: strings.Repeat(string(rune('a'+index%20)), 360)})
	}
	selected, stats := selectAgentMessagesWithSummary(messages, strings.Repeat("s", 200), 900, 180)
	if stats.Dropped == 0 || len(selected) < 2 {
		t.Fatalf("selection=%d stats=%+v", len(selected), stats)
	}
	if selected[0].Role != "user" || !strings.Contains(selected[0].Content, "Condensed earlier conversation") {
		t.Fatalf("first message = %#v", selected[0])
	}
	if estimateVisibleTokens(strings.Repeat("s", 200))+messageSliceTokens(selected) > 920 {
		t.Fatalf("compacted context exceeded budget: %d", estimateVisibleTokens(strings.Repeat("s", 200))+messageSliceTokens(selected))
	}
}

type cancellationProvider struct{}

func (c cancellationProvider) Name() string { return "cancel-test" }
func (c cancellationProvider) Generate(ctx context.Context, _ llm.Request) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func TestRunStreamTreatsInFlightCancellationAsCancelled(t *testing.T) {
	cfg := config.Default()
	cfg.AgentEnabled = true
	cfg.ProviderMaxRetries = 2
	session := history.New("cancel-test", cfg.Provider, cfg.Model(), cfg.Mode)
	session.Append("user", "create a folder")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	var done StreamUpdate
	result := NewRunner(cfg, cancellationProvider{}).RunStream(ctx, session, func(update StreamUpdate) {
		if update.Kind == StreamDone {
			done = update
		}
	})
	if done.Phase != "cancelled" || done.Err != nil {
		t.Fatalf("done update = %#v", done)
	}
	if !strings.Contains(strings.ToLower(result.Text), "cancel") {
		t.Fatalf("result = %#v", result)
	}
}
