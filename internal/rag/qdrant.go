package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// QdrantClient interacts with the Qdrant REST API.
type QdrantClient struct {
	baseURL string
	client  *http.Client
}

// NewQdrantClient creates a client targeting the given Qdrant URL.
func NewQdrantClient(baseURL string) *QdrantClient {
	return &QdrantClient{
		baseURL: baseURL,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// CollectionName returns the Qdrant collection name for an organization.
func CollectionName(orgID string) string {
	return "org_" + orgID
}

// Point represents a vector point for upsert.
type Point struct {
	ID      string                 `json:"id"`
	Vector  []float32              `json:"vector"`
	Payload map[string]interface{} `json:"payload"`
}

// SearchResult represents a single search hit.
type SearchResult struct {
	ID      string                 `json:"id"`
	Score   float64                `json:"score"`
	Payload map[string]interface{} `json:"payload"`
}

// CollectionExists checks if a Qdrant collection exists.
func (q *QdrantClient) CollectionExists(ctx context.Context, name string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, q.baseURL+"/collections/"+name, nil)
	if err != nil {
		return false, fmt.Errorf("creating request: %w", err)
	}

	resp, err := q.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("checking collection: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == http.StatusOK {
		return true, nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	return false, fmt.Errorf("unexpected status %d checking collection %s", resp.StatusCode, name)
}

// EnsureCollection creates a collection with 768-dimension cosine vectors if it doesn't exist.
func (q *QdrantClient) EnsureCollection(ctx context.Context, orgID string) error {
	name := CollectionName(orgID)

	exists, err := q.CollectionExists(ctx, name)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	body := map[string]interface{}{
		"vectors": map[string]interface{}{
			"size":     768,
			"distance": "Cosine",
		},
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshaling collection config: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, q.baseURL+"/collections/"+name, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.client.Do(req)
	if err != nil {
		return fmt.Errorf("creating collection: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to create collection %s (status %d): %s", name, resp.StatusCode, string(respBody))
	}

	return nil
}

// UpsertPoints inserts or updates points in a collection.
func (q *QdrantClient) UpsertPoints(ctx context.Context, collection string, points []Point) error {
	if len(points) == 0 {
		return nil
	}

	body := map[string]interface{}{
		"points": points,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshaling points: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, q.baseURL+"/collections/"+collection+"/points?wait=true", bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.client.Do(req)
	if err != nil {
		return fmt.Errorf("upserting points: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upsert failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// Search performs a similarity search on a collection.
func (q *QdrantClient) Search(ctx context.Context, collection string, vector []float32, limit int, minScore float64) ([]SearchResult, error) {
	body := map[string]interface{}{
		"vector":          vector,
		"limit":           limit,
		"with_payload":    true,
		"score_threshold": minScore,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshaling search request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, q.baseURL+"/collections/"+collection+"/points/search", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("searching: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("search failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Result []struct {
			ID      string                 `json:"id"`
			Score   float64                `json:"score"`
			Payload map[string]interface{} `json:"payload"`
		} `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding search response: %w", err)
	}

	hits := make([]SearchResult, len(result.Result))
	for i, r := range result.Result {
		hits[i] = SearchResult{
			ID:      r.ID,
			Score:   r.Score,
			Payload: r.Payload,
		}
	}

	return hits, nil
}

// DeleteByDocID removes all points associated with a document ID from a collection.
func (q *QdrantClient) DeleteByDocID(ctx context.Context, collection, docID string) error {
	body := map[string]interface{}{
		"filter": map[string]interface{}{
			"must": []map[string]interface{}{
				{
					"key": "doc_id",
					"match": map[string]interface{}{
						"value": docID,
					},
				},
			},
		},
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshaling delete request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, q.baseURL+"/collections/"+collection+"/points/delete?wait=true", bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.client.Do(req)
	if err != nil {
		return fmt.Errorf("deleting points: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}
