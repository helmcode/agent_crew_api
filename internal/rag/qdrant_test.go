package rag

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQdrantClient_CollectionExists(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/collections/org_test123" && r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"result": {"status": "green"}}`))
			return
		}
		if r.URL.Path == "/collections/org_missing" && r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		http.Error(w, "unexpected request", 500)
	}))
	defer server.Close()

	client := NewQdrantClient(server.URL)

	exists, err := client.CollectionExists(context.Background(), "org_test123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("expected collection to exist")
	}

	exists, err = client.CollectionExists(context.Background(), "org_missing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Error("expected collection to not exist")
	}
}

func TestQdrantClient_EnsureCollection_Creates(t *testing.T) {
	created := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/collections/org_new" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodPut && r.URL.Path == "/collections/org_new" {
			created = true
			var body map[string]interface{}
			json.NewDecoder(r.Body).Decode(&body)

			vectors, ok := body["vectors"].(map[string]interface{})
			if !ok {
				t.Error("expected vectors config in body")
			}
			if size, ok := vectors["size"].(float64); !ok || size != 768 {
				t.Errorf("expected size 768, got %v", vectors["size"])
			}
			if dist, ok := vectors["distance"].(string); !ok || dist != "Cosine" {
				t.Errorf("expected Cosine distance, got %v", vectors["distance"])
			}

			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"result": true}`))
			return
		}
		http.Error(w, "unexpected", 500)
	}))
	defer server.Close()

	client := NewQdrantClient(server.URL)
	err := client.EnsureCollection(context.Background(), "new")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !created {
		t.Error("expected collection to be created")
	}
}

func TestQdrantClient_EnsureCollection_AlreadyExists(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"result": {"status": "green"}}`))
			return
		}
		t.Error("should not try to create existing collection")
		http.Error(w, "unexpected", 500)
	}))
	defer server.Close()

	client := NewQdrantClient(server.URL)
	err := client.EnsureCollection(context.Background(), "existing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestQdrantClient_UpsertPoints(t *testing.T) {
	var receivedPoints int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		pts, ok := body["points"].([]interface{})
		if ok {
			receivedPoints = len(pts)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result": {"status": "completed"}}`))
	}))
	defer server.Close()

	client := NewQdrantClient(server.URL)
	points := []Point{
		{ID: "p1", Vector: make([]float32, 768), Payload: map[string]interface{}{"doc_id": "d1"}},
		{ID: "p2", Vector: make([]float32, 768), Payload: map[string]interface{}{"doc_id": "d1"}},
	}

	err := client.UpsertPoints(context.Background(), "org_test", points)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedPoints != 2 {
		t.Errorf("expected 2 points, got %d", receivedPoints)
	}
}

func TestQdrantClient_UpsertPoints_Empty(t *testing.T) {
	client := NewQdrantClient("http://localhost:0")
	err := client.UpsertPoints(context.Background(), "org_test", nil)
	if err != nil {
		t.Fatalf("unexpected error for empty points: %v", err)
	}
}

func TestQdrantClient_Search(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"result": []map[string]interface{}{
				{"id": "p1", "score": 0.95, "payload": map[string]interface{}{"content": "test chunk", "doc_id": "d1"}},
				{"id": "p2", "score": 0.85, "payload": map[string]interface{}{"content": "another chunk", "doc_id": "d1"}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewQdrantClient(server.URL)
	results, err := client.Search(context.Background(), "org_test", make([]float32, 768), 5, 0.7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Score != 0.95 {
		t.Errorf("expected score 0.95, got %f", results[0].Score)
	}
	if results[0].Payload["content"] != "test chunk" {
		t.Errorf("unexpected payload: %v", results[0].Payload)
	}
}

func TestQdrantClient_DeleteByDocID(t *testing.T) {
	var receivedFilter map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		json.NewDecoder(r.Body).Decode(&receivedFilter)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result": {"status": "completed"}}`))
	}))
	defer server.Close()

	client := NewQdrantClient(server.URL)
	err := client.DeleteByDocID(context.Background(), "org_test", "doc-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the filter structure.
	filter, ok := receivedFilter["filter"].(map[string]interface{})
	if !ok {
		t.Fatal("expected filter in request body")
	}
	must, ok := filter["must"].([]interface{})
	if !ok || len(must) != 1 {
		t.Fatal("expected exactly one must condition")
	}
}

func TestCollectionName(t *testing.T) {
	name := CollectionName("abc-123")
	if name != "org_abc-123" {
		t.Errorf("expected org_abc-123, got %s", name)
	}
}
