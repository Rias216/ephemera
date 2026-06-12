package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ephemera-ai/ephemera/internal/util"
)

const episodicMemoryVersion = 2

type episodicMemory struct {
	Version  int             `json:"version"`
	Runs     int             `json:"runs,omitempty"`
	Episodes []memoryEpisode `json:"episodes"`
}

type memoryEpisode struct {
	CompletedAt    time.Time `json:"completed_at"`
	LastReinforced time.Time `json:"last_reinforced,omitempty"`
	Goal           string    `json:"goal"`
	Summary        string    `json:"summary"`
	ChangedPaths   []string  `json:"changed_paths,omitempty"`
	ToolSequence   []string  `json:"tool_sequence,omitempty"`
	Evidence       []string  `json:"evidence,omitempty"`
	Embedding      []float32 `json:"embedding,omitempty"`
	EmbeddingModel string    `json:"embedding_model,omitempty"`
	Reinforcement  int       `json:"reinforcement,omitempty"`
	Verified       bool      `json:"verified"`
}

func (r Runner) learnFromRun(finalText string, state *runState) {
	if !r.Config.AgentLearnMemory || state == nil || strings.TrimSpace(finalText) == "" {
		return
	}
	dir := filepath.Join(r.Tools.WorkspaceRoot, ".ephemera")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	path := filepath.Join(dir, "memory.json")
	memory := episodicMemory{Version: episodicMemoryVersion}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &memory)
	}
	memory.Version = episodicMemoryVersion
	memory.Runs++
	goal := "Complete the current request."
	if state.plan != nil && strings.TrimSpace(state.plan.Goal) != "" {
		goal = state.plan.Goal
	}
	evidence := tailStrings(state.observations, 4)
	for index := range evidence {
		evidence[index] = compact(evidence[index], 500)
	}
	now := time.Now().UTC()
	text := goal + "\n" + finalText + "\n" + strings.Join(changedArtifactPaths(state), " ")
	vector, _ := embedText(context.Background(), r.embedder(), text)
	candidate := memoryEpisode{
		CompletedAt:    now,
		LastReinforced: now,
		Goal:           compact(goal, 500),
		Summary:        compact(finalText, 900),
		ChangedPaths:   changedArtifactPaths(state),
		ToolSequence:   compactToolSequence(state.toolSequence, 12),
		Evidence:       evidence,
		Embedding:      vector,
		EmbeddingModel: r.embedder().Name(),
		Reinforcement:  1,
		Verified:       state.verified,
	}

	merged := false
	bestIndex, bestScore := -1, 0.0
	for index := range memory.Episodes {
		existing := memory.Episodes[index]
		existingVector := existing.Embedding
		if len(existingVector) == 0 {
			existingText := existing.Goal + "\n" + existing.Summary + "\n" + strings.Join(existing.ChangedPaths, " ")
			existingVector, _ = embedText(context.Background(), r.embedder(), existingText)
		}
		if score := cosineSimilarity(vector, existingVector); score > bestScore {
			bestIndex, bestScore = index, score
		}
	}
	if bestIndex >= 0 && bestScore >= 0.88 {
		existing := &memory.Episodes[bestIndex]
		existing.CompletedAt = now
		existing.LastReinforced = now
		existing.Goal = candidate.Goal
		existing.Summary = candidate.Summary
		existing.ChangedPaths = util.UniqueSortedStrings(append(existing.ChangedPaths, candidate.ChangedPaths...))
		existing.ToolSequence = compactToolSequence(append(existing.ToolSequence, candidate.ToolSequence...), 16)
		existing.Evidence = util.DedupStrings(append(existing.Evidence, candidate.Evidence...))
		if len(existing.Evidence) > 8 {
			existing.Evidence = existing.Evidence[len(existing.Evidence)-8:]
		}
		existing.Embedding = candidate.Embedding
		existing.EmbeddingModel = candidate.EmbeddingModel
		existing.Reinforcement = maxInt(1, existing.Reinforcement) + 1
		existing.Verified = existing.Verified || candidate.Verified
		merged = true
	}
	if !merged {
		memory.Episodes = append(memory.Episodes, candidate)
	}
	if memory.Runs%10 == 0 {
		memory.Episodes = consolidateEpisodes(memory.Episodes)
	}
	memory.Episodes = pruneEpisodes(memory.Episodes, 100, now)
	data, err := json.MarshalIndent(memory, "", "  ")
	if err != nil {
		return
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

func consolidateEpisodes(episodes []memoryEpisode) []memoryEpisode {
	for i := 0; i < len(episodes); i++ {
		for j := i + 1; j < len(episodes); {
			if cosineSimilarity(episodes[i].Embedding, episodes[j].Embedding) < 0.93 {
				j++
				continue
			}
			newer, older := episodes[i], episodes[j]
			if older.CompletedAt.After(newer.CompletedAt) {
				newer, older = older, newer
			}
			newer.ChangedPaths = util.UniqueSortedStrings(append(newer.ChangedPaths, older.ChangedPaths...))
			newer.ToolSequence = compactToolSequence(append(older.ToolSequence, newer.ToolSequence...), 16)
			newer.Evidence = util.DedupStrings(append(older.Evidence, newer.Evidence...))
			newer.Reinforcement = maxInt(1, newer.Reinforcement) + maxInt(1, older.Reinforcement)
			newer.Verified = newer.Verified || older.Verified
			episodes[i] = newer
			episodes = append(episodes[:j], episodes[j+1:]...)
		}
	}
	return episodes
}

func pruneEpisodes(episodes []memoryEpisode, limit int, now time.Time) []memoryEpisode {
	if len(episodes) <= limit {
		return episodes
	}
	type scored struct {
		episode memoryEpisode
		score   float64
	}
	items := make([]scored, 0, len(episodes))
	for _, episode := range episodes {
		ageDays := now.Sub(episode.CompletedAt).Hours() / 24
		if ageDays < 0 {
			ageDays = 0
		}
		score := float64(maxInt(1, episode.Reinforcement))*2 - ageDays/30
		if episode.Verified {
			score += 2
		}
		items = append(items, scored{episode: episode, score: score})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].score != items[j].score {
			return items[i].score > items[j].score
		}
		return items[i].episode.CompletedAt.After(items[j].episode.CompletedAt)
	})
	items = items[:limit]
	sort.Slice(items, func(i, j int) bool { return items[i].episode.CompletedAt.Before(items[j].episode.CompletedAt) })
	out := make([]memoryEpisode, len(items))
	for index := range items {
		out[index] = items[index].episode
	}
	return out
}

func compactToolSequence(sequence []string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	out := make([]string, 0, minInt(limit, len(sequence)))
	for _, name := range sequence {
		name = strings.TrimSpace(name)
		if name == "" || (len(out) > 0 && out[len(out)-1] == name) {
			continue
		}
		out = append(out, name)
		if len(out) >= limit {
			break
		}
	}
	return out
}
