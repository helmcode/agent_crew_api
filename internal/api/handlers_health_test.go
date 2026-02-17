package api

import (
	"net/http"
	"testing"
)

func TestHealthCheck_Healthy(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "GET", "/health", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	parseJSON(t, rec, &resp)

	if resp["status"] != "ok" {
		t.Errorf("status: got %q, want 'ok'", resp["status"])
	}
}

func TestHealthCheck_Unhealthy(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Close the underlying SQL connection to simulate a database failure.
	sqlDB, err := srv.db.DB()
	if err != nil {
		t.Fatalf("failed to get underlying sql.DB: %v", err)
	}
	sqlDB.Close()

	rec := doRequest(srv, "GET", "/health", nil)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want %d\nbody: %s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}

	var resp map[string]interface{}
	parseJSON(t, rec, &resp)

	if resp["status"] != "unhealthy" {
		t.Errorf("status: got %q, want 'unhealthy'", resp["status"])
	}

	errors, ok := resp["errors"].([]interface{})
	if !ok || len(errors) == 0 {
		t.Errorf("expected non-empty errors array, got %v", resp["errors"])
	}
}
