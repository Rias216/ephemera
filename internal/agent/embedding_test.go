package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalHashEmbedderCanonicalizesRelatedTerms(t *testing.T) {
	embedder := LocalHashEmbedder{Dimensions: 256}
	left, err := embedder.Embed(context.Background(), "prepare database migrations for the user schema")
	if err != nil {
		t.Fatal(err)
	}
	related, err := embedder.Embed(context.Background(), "migrate schema changes in the database")
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := embedder.Embed(context.Background(), "render a pink terminal user interface")
	if err != nil {
		t.Fatal(err)
	}
	if got, other := cosineSimilarity(left, related), cosineSimilarity(left, unrelated); got <= other {
		t.Fatalf("related similarity=%f, unrelated=%f", got, other)
	}
}

func TestRemoteEmbedderUsesOpenAICompatibleProtocol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Fatalf("authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "embed-model" || body["input"] != "hello" {
			t.Fatalf("request body = %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.25,0.75]}]}`))
	}))
	defer server.Close()

	embedder := RemoteEmbedder{Endpoint: server.URL, APIKey: "secret", Model: "embed-model", Client: server.Client()}
	vector, err := embedder.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(vector) != 2 || vector[0] != 0.25 || vector[1] != 0.75 {
		t.Fatalf("vector = %#v", vector)
	}
}
