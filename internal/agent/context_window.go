package agent

import (
	"sort"
	"strings"
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
	Query          string
	WorkingMemory  string
	Messages       []llm.Message
	NativeTurns    []llm.Message
}

// Fit returns the provider-ready context and selection statistics.
func (w ContextWindow) Fit() ([]llm.Message, messageSelection) {
	budget := w.Budget
	if budget <= 0 {
		budget = 16_000
	}
	valid := append([]llm.Message(nil), w.Messages...)
	stats := messageSelection{Total: len(valid)}
	available := budget - estimateVisibleTokens(w.System) - 4
	if available <= 0 || len(valid) == 0 {
		return nil, stats
	}

	prefix := make([]llm.Message, 0, 2)
	if memory := strings.TrimSpace(w.WorkingMemory); memory != "" {
		prefix = append(prefix, llm.Message{Role: "user", Content: "[Working memory — current run facts, decisions, and evidence]\n" + compact(memory, maxInt(240, available*2))})
	}
	prefixCost := messageSliceTokens(prefix)
	if prefixCost >= available {
		prefix = nil
		prefixCost = 0
	}

	explicitBudget := available - prefixCost
	reserveSummary := minInt(maxInt(0, w.SummaryTokens), explicitBudget/3)
	recentBudget := explicitBudget - reserveSummary
	selected := map[int]bool{}
	used := 0

	// Retain a coherent recent suffix first. Never split solely because the
	// newest message is larger than the nominal budget.
	for index := len(valid) - 1; index >= 0; index-- {
		cost := estimateLLMMessageTokens(valid[index])
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
		summary := w.Compact(omitted, reserveSummary)
		if summary != "" {
			condensed := llm.Message{Role: "user", Content: "[Condensed earlier conversation context — preserve these facts and decisions]\n" + summary}
			for len(chronological) > 1 && prefixCost+messageSliceTokens(chronological)+estimateLLMMessageTokens(condensed) > available {
				chronological = chronological[1:]
				stats.Sent--
				stats.Dropped++
			}
			if prefixCost+messageSliceTokens(chronological)+estimateLLMMessageTokens(condensed) <= available {
				prefix = append(prefix, condensed)
			}
		}
	}

	out := append(prefix, chronological...)
	remaining := available - messageSliceTokens(out)
	if remaining > 0 && len(w.NativeTurns) > 0 {
		out = append(out, selectNativeTurnsDeduplicated(w.NativeTurns, remaining)...)
	}
	return out, stats
}

type recalledMessage struct {
	Index int
	Cost  int
	Score int
}

// Recall selects semantically relevant older messages not already retained.
func (w ContextWindow) Recall(messages []llm.Message, selected map[int]bool, budget int) []recalledMessage {
	if budget <= 0 || w.RecallMessages <= 0 {
		return nil
	}
	queryTerms := semanticTerms(w.Query)
	if len(queryTerms) == 0 {
		return nil
	}
	candidates := make([]recalledMessage, 0, len(messages))
	for index, message := range messages {
		if selected[index] {
			continue
		}
		score := semanticScore(message.Content, queryTerms)
		if score == 0 {
			continue
		}
		candidates = append(candidates, recalledMessage{Index: index, Cost: estimateLLMMessageTokens(message), Score: score})
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
	for len(chunks) > 1 && estimateVisibleTokens(strings.Join(chunks, "\n---\n")) > maxTokens {
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
	return selectNativeTurns(filtered, budget)
}
