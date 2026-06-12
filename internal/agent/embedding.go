package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"
)

// Embedder is the provider-neutral semantic vector boundary. Remote embedding
// adapters can implement it without coupling memory to a specific LLM vendor.
type Embedder interface {
	Name() string
	Embed(context.Context, string) ([]float32, error)
}

// LocalHashEmbedder is a zero-network semantic fallback. It uses canonicalized
// terms and signed feature hashing, so related phrasing remains comparable even
// when no remote embedding provider is configured.
type LocalHashEmbedder struct{ Dimensions int }

func (e LocalHashEmbedder) Name() string { return "local-hash-v1" }

func (e LocalHashEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	dimensions := e.Dimensions
	if dimensions < 64 {
		dimensions = 256
	}
	vector := make([]float32, dimensions)
	terms := embeddingTerms(text)
	if len(terms) == 0 {
		return vector, nil
	}
	for term, weight := range terms {
		h := fnv.New64a()
		_, _ = h.Write([]byte(term))
		value := h.Sum64()
		index := int(value % uint64(dimensions))
		sign := float32(1)
		if value&(1<<63) != 0 {
			sign = -1
		}
		vector[index] += sign * float32(weight)
	}
	normalizeVector(vector)
	return vector, nil
}

// RemoteEmbedder speaks the OpenAI-compatible embeddings protocol. It is
// enabled only through explicit EPHEMERA_EMBEDDING_* environment variables so
// semantic recall never creates surprise network cost.
type RemoteEmbedder struct {
	Endpoint string
	APIKey   string
	Model    string
	Client   *http.Client
}

func (e RemoteEmbedder) Name() string {
	if model := strings.TrimSpace(e.Model); model != "" {
		return "remote:" + model
	}
	return "remote-embedding"
}

func (e RemoteEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	endpoint := strings.TrimSpace(e.Endpoint)
	if endpoint == "" {
		return nil, errors.New("embedding endpoint is empty")
	}
	model := strings.TrimSpace(e.Model)
	if model == "" {
		model = "text-embedding-3-small"
	}
	body, err := json.Marshal(map[string]any{"model": model, "input": text})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(e.APIKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client := e.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("embedding endpoint returned %s", resp.Status)
	}
	var payload struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if len(payload.Data) == 0 || len(payload.Data[0].Embedding) == 0 {
		return nil, errors.New("embedding endpoint returned no vector")
	}
	return payload.Data[0].Embedding, nil
}

func defaultEmbedder() Embedder { return LocalHashEmbedder{Dimensions: 256} }

func configuredEmbedder() Embedder {
	if endpoint := strings.TrimSpace(os.Getenv("EPHEMERA_EMBEDDING_URL")); endpoint != "" {
		return RemoteEmbedder{
			Endpoint: endpoint,
			APIKey:   os.Getenv("EPHEMERA_EMBEDDING_API_KEY"),
			Model:    os.Getenv("EPHEMERA_EMBEDDING_MODEL"),
		}
	}
	return defaultEmbedder()
}

func (r Runner) embedder() Embedder {
	if r.Embedder != nil {
		return r.Embedder
	}
	return defaultEmbedder()
}

func embedText(ctx context.Context, embedder Embedder, text string) ([]float32, error) {
	if embedder == nil {
		return nil, errors.New("embedder unavailable")
	}
	return embedder.Embed(ctx, text)
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		av, bv := float64(a[i]), float64(b[i])
		dot += av * bv
		normA += av * av
		normB += bv * bv
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

func normalizeVector(values []float32) {
	var norm float64
	for _, value := range values {
		norm += float64(value * value)
	}
	if norm == 0 {
		return
	}
	scale := float32(1 / math.Sqrt(norm))
	for index := range values {
		values[index] *= scale
	}
}

func embeddingTerms(text string) map[string]int {
	terms := map[string]int{}
	var token strings.Builder
	flush := func() {
		value := canonicalEmbeddingTerm(strings.ToLower(strings.TrimSpace(token.String())))
		token.Reset()
		if len(value) < 3 || commonContextTerm(value) {
			return
		}
		weight := 1
		if strings.ContainsAny(value, "/\\._-") {
			weight = 3
		}
		terms[value] += weight
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

func canonicalEmbeddingTerm(value string) string {
	value = strings.TrimSpace(value)
	switch value {
	case "db", "database", "databases", "schema", "schemas":
		return "database-schema"
	case "migration", "migrations", "migrate", "migrating":
		return "database-migration"
	case "auth", "authentication", "login", "signin", "session":
		return "authentication-session"
	case "failure", "failures", "failed", "error", "errors":
		return "failure-error"
	case "test", "tests", "testing", "verification", "verify", "verified":
		return "test-verification"
	case "refactor", "refactoring", "cleanup", "restructure":
		return "refactor-cleanup"
	case "provider", "providers", "model", "models", "llm":
		return "llm-provider"
	}
	return value
}
