package api

import (
	"testing"

	"github.com/helmcode/agent-crew/internal/models"
)

func TestGetOllamaStatus_NoRecord(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "GET", "/api/ollama/status", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	var resp OllamaStatusResponse
	parseJSON(t, rec, &resp)

	if resp.Running {
		t.Error("expected running=false when no SharedInfra record exists")
	}
	if resp.ContainerID != "" {
		t.Errorf("expected empty container_id, got %q", resp.ContainerID)
	}
	if resp.RefCount != 0 {
		t.Errorf("expected ref_count=0, got %d", resp.RefCount)
	}
	if resp.ModelsPulled == nil {
		t.Error("models_pulled should be an empty array, not nil")
	}
}

func TestGetOllamaStatus_WithRecord_NotRunning(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Create a SharedInfra record with stopped status.
	srv.db.Create(&models.SharedInfra{
		ID:           "infra-status-1",
		ResourceType: "ollama",
		ContainerID:  "ollama-cid-123",
		Status:       "stopped",
		RefCount:     0,
	})

	rec := doRequest(srv, "GET", "/api/ollama/status", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	var resp OllamaStatusResponse
	parseJSON(t, rec, &resp)

	if resp.Running {
		t.Error("expected running=false when container is not running")
	}
	if resp.ContainerID != "ollama-cid-123" {
		t.Errorf("container_id: got %q, want %q", resp.ContainerID, "ollama-cid-123")
	}
	if resp.RefCount != 0 {
		t.Errorf("ref_count: got %d, want 0", resp.RefCount)
	}
}

func TestGetOllamaStatus_WithRecord_Running(t *testing.T) {
	srv, mock := setupTestServer(t)
	mock.mu.Lock()
	mock.ollamaRunning = true
	mock.mu.Unlock()

	srv.db.Create(&models.SharedInfra{
		ID:           "infra-status-2",
		ResourceType: "ollama",
		ContainerID:  "ollama-cid-456",
		Status:       "running",
		RefCount:     2,
	})

	rec := doRequest(srv, "GET", "/api/ollama/status", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	var resp OllamaStatusResponse
	parseJSON(t, rec, &resp)

	if !resp.Running {
		t.Error("expected running=true when mock reports running")
	}
	if resp.ContainerID != "ollama-cid-456" {
		t.Errorf("container_id: got %q, want %q", resp.ContainerID, "ollama-cid-456")
	}
	if resp.RefCount != 2 {
		t.Errorf("ref_count: got %d, want 2", resp.RefCount)
	}
}

func TestOllamaStatusResponse_DTO(t *testing.T) {
	// Verify the DTO fields exist and have correct zero values.
	resp := OllamaStatusResponse{}
	if resp.Running {
		t.Error("default Running should be false")
	}
	if resp.ContainerID != "" {
		t.Error("default ContainerID should be empty")
	}
	if resp.RefCount != 0 {
		t.Error("default RefCount should be 0")
	}
	if resp.GPUAvailable {
		t.Error("default GPUAvailable should be false")
	}
}
