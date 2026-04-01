package api

import (
	"testing"
)

func TestGetOllamaStatus_NotRunning(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "GET", "/api/ollama/status", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	var resp OllamaStatusResponse
	parseJSON(t, rec, &resp)

	if resp.Running {
		t.Error("expected running=false when ollama is not running")
	}
	if resp.ModelsPulled == nil {
		t.Error("models_pulled should be an empty array, not nil")
	}
}

func TestGetOllamaStatus_Running(t *testing.T) {
	srv, mock := setupTestServer(t)
	mock.mu.Lock()
	mock.ollamaRunning = true
	mock.mu.Unlock()

	rec := doRequest(srv, "GET", "/api/ollama/status", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	var resp OllamaStatusResponse
	parseJSON(t, rec, &resp)

	if !resp.Running {
		t.Error("expected running=true when mock reports running")
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
	if resp.GPUAvailable {
		t.Error("default GPUAvailable should be false")
	}
}
