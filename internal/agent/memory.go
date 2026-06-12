package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// projectMemory returns stable project instructions plus only the episodic
// memories relevant to the current request. Older versions appended the whole
// memory.json file to every model round, which made learning increasingly
// expensive and eventually drowned out the current task.
func (r Runner) projectMemory(query string) string {
	paths := []string{
		filepath.Join(r.Tools.WorkspaceRoot, ".ephemera", "instructions.md"),
		filepath.Join(r.Tools.WorkspaceRoot, ".ephemera", "preferences.json"),
		filepath.Join(r.Tools.WorkspaceRoot, "CLAUDE.md"),
		filepath.Join(r.Tools.WorkspaceRoot, "AGENTS.md"),
	}
	var sections []string
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			continue
		}
		rel, _ := filepath.Rel(r.Tools.WorkspaceRoot, path)
		sections = append(sections, "## "+filepath.ToSlash(rel)+"\n"+strings.TrimSpace(string(data)))
	}
	if learned := r.relevantEpisodes(query, 3); learned != "" {
		sections = append(sections, "## Relevant successful runs\n"+learned)
	}
	return compact(strings.Join(sections, "\n\n"), 6000)
}

type rankedEpisode struct {
	episode memoryEpisode
	score   int
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
	ranked := make([]rankedEpisode, 0, len(memory.Episodes))
	for index, episode := range memory.Episodes {
		text := episode.Goal + "\n" + episode.Summary + "\n" + strings.Join(episode.ChangedPaths, " ")
		relevance := semanticScore(text, terms)
		// Verification and recency are ranking signals, not relevance signals.
		// Otherwise every verified episode leaks into the prompt even when it
		// has no semantic overlap with the current request.
		if len(terms) > 0 && relevance <= 0 {
			continue
		}
		score := relevance
		if episode.Verified {
			score += 3
		}
		// Recency breaks otherwise-equal matches without allowing an unrelated
		// new episode to outrank a strongly relevant older one.
		score += index * 2 / maxInt(1, len(memory.Episodes))
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
