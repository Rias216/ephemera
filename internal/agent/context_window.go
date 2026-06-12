package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/ephemera-ai/ephemera/internal/llm"
)

// ContextWindow builds a bounded request context while retaining recent turns,
// semantically relevant older turns, compact working memory, and one
// deterministic hierarchical summary of omitted conversation history.
type ContextWindow struct {
	System         string
	Budget         int
	SummaryTokens  int
	RecallMessages int
	Provider       string
	Iteration      int
	MaxIterations  int
	Query          string
	WorkingMemory  string
	Embedder       Embedder
	Messages       []llm.Message
	NativeTurns    []llm.Message
	Cache          *ContextFitCache
}

// ContextFitCache is shared across iterations of one agent run. It caches both
// complete fits and deterministic history summaries. The cache key includes
// every input that can affect selection, so message edits and working-memory
// changes cannot serve stale context.
type ContextFitCache struct {
	mu        sync.Mutex
	fitKey    [32]byte
	messages  []llm.Message
	selection messageSelection
	summaries map[[32]byte]string
}

func NewContextFitCache() *ContextFitCache {
	return &ContextFitCache{summaries: make(map[[32]byte]string)}
}

// Fit returns the provider-ready context and selection statistics.
func (w ContextWindow) Fit() ([]llm.Message, messageSelection) {
	budget := w.Budget
	if budget <= 0 {
		budget = 16_000
	}
	summaryTokens := w.adaptiveSummaryTokens(budget)
	if cached, stats, ok := w.cachedFit(budget, summaryTokens); ok {
		return cached, stats
	}
	valid := append([]llm.Message(nil), w.Messages...)
	stats := messageSelection{Total: len(valid)}
	available := budget - estimateVisibleTokensForProvider(w.System, w.Provider) - 4
	if available <= 0 || len(valid) == 0 {
		return nil, stats
	}

	prefix := make([]llm.Message, 0, 2)
	if memory := strings.TrimSpace(w.WorkingMemory); memory != "" {
		prefix = append(prefix, llm.Message{Role: "user", Content: "[Working memory — current run facts, decisions, and evidence]\n" + compact(memory, maxInt(240, available*2))})
	}
	prefixCost := messageSliceTokensForProvider(prefix, w.Provider)
	if prefixCost >= available {
		prefix = nil
		prefixCost = 0
	}

	// Reserve provider-native tool call/result turns before filling the ordinary
	// conversation budget. Providers such as OpenAI and Anthropic reject or
	// misunderstand a follow-up when the corresponding tool result is silently
	// compacted away under context pressure. Preserve at least the newest user
	// turn first, then spend the remaining capacity on complete native groups.
	conversationFloor := newestUserMessageCost(valid, w.Provider)
	nativeBudget := maxInt(0, available-prefixCost-conversationFloor)
	nativeTurns := selectNativeTurnsDeduplicatedForProvider(w.NativeTurns, nativeBudget, w.Provider)
	nativeCost := messageSliceTokensForProvider(nativeTurns, w.Provider)

	explicitBudget := maxInt(0, available-prefixCost-nativeCost)
	reserveSummary := minInt(maxInt(0, summaryTokens), explicitBudget/2)
	recentBudget := explicitBudget - reserveSummary
	selected := map[int]bool{}
	used := 0

	// Retain a coherent recent suffix first. Never split solely because the
	// newest message is larger than the nominal budget.
	for index := len(valid) - 1; index >= 0; index-- {
		cost := estimateLLMMessageTokensForProvider(valid[index], w.Provider)
		if used+cost > recentBudget && len(selected) > 0 {
			break
		}
		if cost > recentBudget && len(selected) == 0 {
			continue
		}
		selected[index] = true
		used += cost
	}

	// Recall a bounded number of older turns that overlap with the current
	// request. This prefers file paths, identifiers, and uncommon words.
	recallBudget := explicitBudget - used - reserveSummary
	for _, candidate := range w.Recall(valid, selected, recallBudget) {
		selected[candidate.Index] = true
		used += candidate.Cost
	}

	chronological := selectedMessages(valid, selected)
	for len(chronological) > 0 && chronological[0].Role == "assistant" {
		chronological = chronological[1:]
	}
	stats.Sent = len(chronological)
	stats.Dropped = stats.Total - stats.Sent

	if stats.Dropped > 0 && reserveSummary > 0 {
		omitted := omittedMessages(valid, selected)
		summary := w.compactCached(omitted, reserveSummary)
		if summary != "" {
			condensed := llm.Message{Role: "user", Content: "[Condensed earlier conversation context — preserve these facts and decisions]\n" + summary}
			for len(chronological) > 1 && prefixCost+messageSliceTokensForProvider(chronological, w.Provider)+estimateLLMMessageTokensForProvider(condensed, w.Provider) > available {
				chronological = chronological[1:]
				stats.Sent--
				stats.Dropped++
			}
			if prefixCost+messageSliceTokensForProvider(chronological, w.Provider)+estimateLLMMessageTokensForProvider(condensed, w.Provider) <= available {
				prefix = append(prefix, condensed)
			}
		}
	}

	out := append(prefix, chronological...)
	out = append(out, nativeTurns...)
	w.storeFit(budget, summaryTokens, out, stats)
	return out, stats
}

func newestUserMessageCost(messages []llm.Message, provider string) int {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == "user" {
			return estimateLLMMessageTokensForProvider(messages[index], provider)
		}
	}
	return 0
}

func (w ContextWindow) adaptiveSummaryTokens(budget int) int {
	configured := maxInt(0, w.SummaryTokens)
	if configured == 0 || budget <= 0 {
		return 0
	}
	ratio := 0.0
	if w.MaxIterations > 1 && w.Iteration > 0 {
		ratio = float64(minInt(w.Iteration-1, w.MaxIterations-1)) / float64(w.MaxIterations-1)
	}
	// Grow the summary reserve as the run progresses, while treating the user's
	// configured summary budget as a hard cap. This avoids starving early tool
	// rounds and preserves more accumulated evidence near completion.
	dynamicCap := int(float64(budget) * (0.10 + 0.30*ratio))
	return minInt(configured, maxInt(64, dynamicCap))
}

func (w ContextWindow) cachedFit(budget, summaryTokens int) ([]llm.Message, messageSelection, bool) {
	if w.Cache == nil {
		return nil, messageSelection{}, false
	}
	key := w.fitKey(budget, summaryTokens)
	w.Cache.mu.Lock()
	defer w.Cache.mu.Unlock()
	if w.Cache.fitKey != key || w.Cache.messages == nil {
		return nil, messageSelection{}, false
	}
	return cloneLLMMessages(w.Cache.messages), w.Cache.selection, true
}

func (w ContextWindow) storeFit(budget, summaryTokens int, messages []llm.Message, selection messageSelection) {
	if w.Cache == nil {
		return
	}
	key := w.fitKey(budget, summaryTokens)
	w.Cache.mu.Lock()
	w.Cache.fitKey = key
	w.Cache.messages = cloneLLMMessages(messages)
	w.Cache.selection = selection
	w.Cache.mu.Unlock()
}

func (w ContextWindow) fitKey(budget, summaryTokens int) [32]byte {
	payload := struct {
		System, Provider, Query, WorkingMemory string
		Budget, SummaryTokens, RecallMessages  int
		Messages, NativeTurns                  []llm.Message
	}{w.System, w.Provider, w.Query, w.WorkingMemory, budget, summaryTokens, w.RecallMessages, w.Messages, w.NativeTurns}
	data, _ := json.Marshal(payload)
	return sha256.Sum256(data)
}

func (w ContextWindow) compactCached(messages []llm.Message, maxTokens int) string {
	if w.Cache == nil {
		return w.Compact(messages, maxTokens)
	}
	data, _ := json.Marshal(struct {
		Provider  string
		MaxTokens int
		Messages  []llm.Message
	}{w.Provider, maxTokens, messages})
	key := sha256.Sum256(data)
	w.Cache.mu.Lock()
	if summary, ok := w.Cache.summaries[key]; ok {
		w.Cache.mu.Unlock()
		return summary
	}
	w.Cache.mu.Unlock()
	summary := w.Compact(messages, maxTokens)
	w.Cache.mu.Lock()
	if w.Cache.summaries == nil {
		w.Cache.summaries = make(map[[32]byte]string)
	}
	w.Cache.summaries[key] = summary
	w.Cache.mu.Unlock()
	return summary
}

type recalledMessage struct {
	Index int
	Cost  int
	Score float64
}

// Recall selects semantically relevant older messages not already retained.
func (w ContextWindow) Recall(messages []llm.Message, selected map[int]bool, budget int) []recalledMessage {
	if budget <= 0 || w.RecallMessages <= 0 {
		return nil
	}
	queryTerms := semanticTerms(w.Query)
	queryVector, embedErr := embedText(context.Background(), w.Embedder, w.Query)
	if len(queryTerms) == 0 && (embedErr != nil || vectorIsZero(queryVector)) {
		return nil
	}
	candidates := make([]recalledMessage, 0, len(messages))
	for index, message := range messages {
		if selected[index] {
			continue
		}
		score := 0.0
		if embedErr == nil && !vectorIsZero(queryVector) {
			if vector, err := embedText(context.Background(), w.Embedder, message.Content); err == nil {
				score = cosineSimilarity(queryVector, vector)
			}
		}
		lexical := semanticScore(message.Content, queryTerms)
		if lexical > 0 {
			score += float64(lexical) * 0.08
		}
		if score <= 0.05 {
			continue
		}
		candidates = append(candidates, recalledMessage{Index: index, Cost: estimateLLMMessageTokensForProvider(message, w.Provider), Score: score})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].Index > candidates[j].Index
	})
	picked := make([]recalledMessage, 0, minInt(w.RecallMessages, len(candidates)))
	used := 0
	for _, candidate := range candidates {
		if len(picked) >= w.RecallMessages {
			break
		}
		if used+candidate.Cost > budget {
			continue
		}
		picked = append(picked, candidate)
		used += candidate.Cost
	}
	sort.Slice(picked, func(i, j int) bool { return picked[i].Index < picked[j].Index })
	return picked
}

// Compact hierarchically condenses omitted messages. It first compacts bounded
// chunks, then recursively compacts the chunk summaries until the token target
// is met. This avoids keeping only the beginning or end of long histories.
func (w ContextWindow) Compact(messages []llm.Message, maxTokens int) string {
	if maxTokens <= 0 || len(messages) == 0 {
		return ""
	}
	chunks := make([]string, 0, (len(messages)+5)/6)
	for start := 0; start < len(messages); start += 6 {
		end := minInt(len(messages), start+6)
		var lines []string
		for _, message := range messages[start:end] {
			content := strings.TrimSpace(message.Content)
			if content == "" {
				continue
			}
			role := strings.ToUpper(firstNonEmpty(message.Role, "context"))
			lines = append(lines, role+": "+compact(content, 420))
		}
		if len(lines) > 0 {
			chunks = append(chunks, strings.Join(lines, "\n"))
		}
	}
	for len(chunks) > 1 && estimateVisibleTokensForProvider(strings.Join(chunks, "\n---\n"), w.Provider) > maxTokens {
		next := make([]string, 0, (len(chunks)+1)/2)
		for index := 0; index < len(chunks); index += 2 {
			end := minInt(len(chunks), index+2)
			next = append(next, compact(strings.Join(chunks[index:end], "\n---\n"), maxInt(160, maxTokens*2)))
		}
		chunks = next
	}
	return compact(strings.Join(chunks, "\n---\n"), maxTokens*4)
}

func selectedMessages(messages []llm.Message, selected map[int]bool) []llm.Message {
	out := make([]llm.Message, 0, len(selected))
	for index, message := range messages {
		if selected[index] {
			out = append(out, message)
		}
	}
	return out
}

func omittedMessages(messages []llm.Message, selected map[int]bool) []llm.Message {
	out := make([]llm.Message, 0, len(messages)-len(selected))
	for index, message := range messages {
		if !selected[index] {
			out = append(out, message)
		}
	}
	return out
}

func vectorIsZero(values []float32) bool {
	for _, value := range values {
		if value != 0 {
			return false
		}
	}
	return true
}

func semanticTerms(text string) map[string]int {
	terms := map[string]int{}
	var token strings.Builder
	flush := func() {
		value := strings.ToLower(strings.TrimSpace(token.String()))
		token.Reset()
		if len(value) < 3 || commonContextTerm(value) {
			return
		}
		weight := 1
		if strings.ContainsAny(value, "/\\._-") {
			weight = 3
		}
		terms[value] = maxInt(terms[value], weight)
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("/\\._-", r) {
			token.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return terms
}

func semanticScore(text string, terms map[string]int) int {
	lower := strings.ToLower(text)
	score := 0
	for term, weight := range terms {
		if strings.Contains(lower, term) {
			score += weight
		}
	}
	return score
}

func commonContextTerm(value string) bool {
	switch value {
	case "the", "and", "for", "with", "that", "this", "from", "into", "then", "our", "your", "you", "are", "was", "were", "have", "has", "will", "can", "could", "should", "would", "about", "continue", "plan":
		return true
	default:
		return false
	}
}

func selectNativeTurnsDeduplicated(turns []llm.Message, budget int) []llm.Message {
	return selectNativeTurnsDeduplicatedForProvider(turns, budget, "")
}

func selectNativeTurnsDeduplicatedForProvider(turns []llm.Message, budget int, provider string) []llm.Message {
	seenResults := map[string]bool{}
	filtered := make([]llm.Message, 0, len(turns))
	for _, message := range turns {
		if message.ToolResult != nil {
			key := message.ToolResult.ID + "\x00" + message.ToolResult.Name
			if seenResults[key] {
				continue
			}
			seenResults[key] = true
		}
		filtered = append(filtered, message)
	}
	return selectNativeTurnsForProvider(filtered, budget, provider)
}

func cloneLLMMessages(messages []llm.Message) []llm.Message {
	cloned := make([]llm.Message, len(messages))
	for index, message := range messages {
		cloned[index] = message
		cloned[index].ToolCalls = append([]llm.ToolCall(nil), message.ToolCalls...)
		for callIndex := range cloned[index].ToolCalls {
			cloned[index].ToolCalls[callIndex].Arguments = cloneArguments(cloned[index].ToolCalls[callIndex].Arguments)
		}
		if message.ToolResult != nil {
			result := *message.ToolResult
			result.Metadata = cloneMetadata(result.Metadata)
			cloned[index].ToolResult = &result
		}
	}
	return cloned
}
