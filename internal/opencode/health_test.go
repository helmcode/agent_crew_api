package opencode

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestWaitForHealth_HealthyImmediately(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(HealthResponse{Healthy: true, Version: "1.0.0"})
	}))
	defer srv.Close()

	err := WaitForHealth(srv.URL, "opencode", "", 5*time.Second)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestWaitForHealth_HealthyAfterRetries(t *testing.T) {
	var attempts atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n < 3 {
			json.NewEncoder(w).Encode(HealthResponse{Healthy: false, Version: "1.0.0"})
			return
		}
		json.NewEncoder(w).Encode(HealthResponse{Healthy: true, Version: "1.0.0"})
	}))
	defer srv.Close()

	err := WaitForHealth(srv.URL, "opencode", "", 10*time.Second)
	if err != nil {
		t.Fatalf("expected eventual success, got: %v", err)
	}

	if attempts.Load() < 3 {
		t.Errorf("expected at least 3 attempts, got %d", attempts.Load())
	}
}

func TestWaitForHealth_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(HealthResponse{Healthy: false, Version: "1.0.0"})
	}))
	defer srv.Close()

	err := WaitForHealth(srv.URL, "opencode", "", 2*time.Second)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestWaitForHealth_ServerDown(t *testing.T) {
	err := WaitForHealth("http://127.0.0.1:1", "opencode", "", 2*time.Second)
	if err == nil {
		t.Fatal("expected error for unreachable server")
	}
}

func TestWaitForHealth_WithAuth(t *testing.T) {
	var gotAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(HealthResponse{Healthy: true, Version: "1.0.0"})
	}))
	defer srv.Close()

	err := WaitForHealth(srv.URL, "myuser", "mypass", 5*time.Second)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if gotAuth == "" {
		t.Error("expected Authorization header to be set")
	}
}
