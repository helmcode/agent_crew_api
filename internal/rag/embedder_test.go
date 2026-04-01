package rag

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaEmbedder_Embed(t *testing.T) {
	// Mock Ollama server.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.Error(w, "not found", 404)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
			http.Error(w, "method not allowed", 405)
			return
		}

		var req embedRequest
		json.NewDecoder(r.Body).Decode(&req)

		if req.Model != "nomic-embed-text" {
			t.Errorf("unexpected model: %s", req.Model)
		}
		if len(req.Input) != 1 {
			t.Errorf("expected 1 input, got %d", len(req.Input))
		}

		// Return a fake 768-dim embedding.
		emb := make([]float64, 768)
		for i := range emb {
			emb[i] = float64(i) * 0.001
		}

		resp := embedResponse{Embeddings: [][]float64{emb}}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	embedder := NewOllamaEmbedder(server.URL, "nomic-embed-text")
	vec, err := embedder.Embed(context.Background(), "test text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(vec) != 768 {
		t.Errorf("expected 768-dim vector, got %d", len(vec))
	}
}

func TestOllamaEmbedder_EmbedBatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req embedRequest
		json.NewDecoder(r.Body).Decode(&req)

		embeddings := make([][]float64, len(req.Input))
		for i := range embeddings {
			emb := make([]float64, 768)
			for j := range emb {
				emb[j] = float64(i*768+j) * 0.001
			}
			embeddings[i] = emb
		}

		json.NewEncoder(w).Encode(embedResponse{Embeddings: embeddings})
	}))
	defer server.Close()

	embedder := NewOllamaEmbedder(server.URL, "nomic-embed-text")
	vectors, err := embedder.EmbedBatch(context.Background(), []string{"text1", "text2", "text3"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(vectors) != 3 {
		t.Fatalf("expected 3 vectors, got %d", len(vectors))
	}

	for i, vec := range vectors {
		if len(vec) != 768 {
			t.Errorf("vector %d: expected 768-dim, got %d", i, len(vec))
		}
	}
}

func TestOllamaEmbedder_EmptyBatch(t *testing.T) {
	embedder := NewOllamaEmbedder("http://localhost:0", "nomic-embed-text")
	vectors, err := embedder.EmbedBatch(context.Background(), []string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vectors != nil {
		t.Errorf("expected nil for empty batch, got %v", vectors)
	}
}

func TestOllamaEmbedder_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", 500)
	}))
	defer server.Close()

	embedder := NewOllamaEmbedder(server.URL, "nomic-embed-text")
	_, err := embedder.Embed(context.Background(), "test")
	if err == nil {
		t.Error("expected error for server error response")
	}
}

func TestOllamaEmbedder_MismatchedCount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return 1 embedding when 2 were requested.
		emb := make([]float64, 768)
		json.NewEncoder(w).Encode(embedResponse{Embeddings: [][]float64{emb}})
	}))
	defer server.Close()

	embedder := NewOllamaEmbedder(server.URL, "nomic-embed-text")
	_, err := embedder.EmbedBatch(context.Background(), []string{"text1", "text2"})
	if err == nil {
		t.Error("expected error for mismatched embedding count")
	}
}
