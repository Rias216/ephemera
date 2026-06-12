package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// projectMemory returns stable project instructions, global preferences, and
// only the episodic memories relevant to the current request.
func (r Runner) projectMemory(query string) string {
	paths := []string{}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".ephemera", "global-memory.json"))
	}
	paths = append(paths,
		filepath.Join(r.Tools.WorkspaceRoot, ".ephemera", "instructions.md"),
		filepath.Join(r.Tools.WorkspaceRoot, ".ephemera", "preferences.json"),
		filepath.Join(r.Tools.WorkspaceRoot, "CLAUDE.md"),
		filepath.Join(r.Tools.WorkspaceRoot, "AGENTS.md"),
	)
	var sections []string
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			continue
		}
		label := filepath.ToSlash(path)
		if rel, err := filepath.Rel(r.Tools.WorkspaceRoot, path); err == nil && !strings.HasPrefix(rel, "..") {
			label = filepath.ToSlash(rel)
		} else if strings.HasSuffix(path, "global-memory.json") {
			label = "~/.ephemera/global-memory.json"
		}
		sections = append(sections, "## "+label+"\n"+strings.TrimSpace(string(data)))
	}
	if learned := r.relevantEpisodes(query, 3); learned != "" {
		sections = append(sections, "## Relevant successful runs\n"+learned)
	}
	return compact(strings.Join(sections, "\n\n"), 6000)
}

type rankedEpisode struct {
	episode memoryEpisode
	score   float64
	index   int
}

func (r Runner) relevantEpisodes(query string, limit int) string {
	if limit <= 0 {
		return ""
	}
	path := filepath.Join(r.Tools.WorkspaceRoot, ".ephemera", "memory.json")
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return ""
	}
	var memory episodicMemory
	if json.Unmarshal(data, &memory) != nil || len(memory.Episodes) == 0 {
		return ""
	}
	terms := semanticTerms(query)
	queryVector, vectorErr := embedText(context.Background(), r.embedder(), query)
	ranked := make([]rankedEpisode, 0, len(memory.Episodes))
	for index, episode := range memory.Episodes {
		text := episode.Goal + "\n" + episode.Summary + "\n" + strings.Join(episode.ChangedPaths, " ")
		relevance := 0.0
		if vectorErr == nil && !vectorIsZero(queryVector) {
			vector := episode.Embedding
			if len(vector) == 0 {
				vector, _ = embedText(context.Background(), r.embedder(), text)
			}
			relevance = cosineSimilarity(queryVector, vector)
		}
		lexical := semanticScore(text, terms)
		if lexical > 0 {
			relevance += float64(lexical) * 0.08
		}
		if len(terms) > 0 && relevance <= 0.05 {
			continue
		}
		score := relevance
		if episode.Verified {
			score += 0.12
		}
		score += float64(maxInt(1, episode.Reinforcement)-1) * 0.02
		score += float64(index) / float64(maxInt(1, len(memory.Episodes))) * 0.01
		ranked = append(ranked, rankedEpisode{episode: episode, score: score, index: index})
	}
	if len(ranked) == 0 {
		return ""
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].index > ranked[j].index
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	var lines []string
	for _, item := range ranked {
		episode := item.episode
		status := "completed"
		if episode.Verified {
			status = "verified"
		}
		line := fmt.Sprintf("- %s: %s", status, compact(episode.Goal, 260))
		if episode.Reinforcement > 1 {
			line += fmt.Sprintf(" (reinforced %d×)", episode.Reinforcement)
		}
		if len(episode.ToolSequence) > 0 {
			line += "\n  successful tool path: " + strings.Join(compactToolSequence(episode.ToolSequence, 10), " → ")
		}
		if len(episode.ChangedPaths) > 0 {
			line += "\n  touched: " + strings.Join(episode.ChangedPaths, ", ")
		}
		if summary := strings.TrimSpace(episode.Summary); summary != "" {
			line += "\n  outcome: " + compact(summary, 360)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
