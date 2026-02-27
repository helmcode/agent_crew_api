package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// mockOpenCodeServer creates a test HTTP server that mimics `opencode serve`.
func mockOpenCodeServer(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc("GET /global/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(HealthResponse{Healthy: true, Version: "1.0.0-test"})
	})

	mux.HandleFunc("POST /session", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(CreateSessionResponse{
			ID:    "test-session-123",
			Title: "agentcrew-session",
		})
	})

	mux.HandleFunc("POST /session/{id}/message", func(w http.ResponseWriter, r *http.Request) {
		var req SendMessageRequest
		json.NewDecoder(r.Body).Decode(&req)
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("POST /session/{id}/abort", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("GET /global/event", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		// Send a server.connected event.
		fmt.Fprintf(w, "event: server.connected\ndata: {}\n\n")
		flusher.Flush()

		// Send a message part event.
		partPayload, _ := json.Marshal(MessagePartPayload{
			SessionID: "test-session-123",
			MessageID: "msg-1",
			Part: Part{
				Type:    "text",
				Content: json.RawMessage(`{"text":"Hello from OpenCode"}`),
			},
		})
		fmt.Fprintf(w, "event: message.part.updated\ndata: %s\n\n", partPayload)
		flusher.Flush()

		// Send a session.idle event.
		idlePayload, _ := json.Marshal(SessionIdlePayload{
			SessionID: "test-session-123",
		})
		fmt.Fprintf(w, "event: session.idle\ndata: %s\n\n", idlePayload)
		flusher.Flush()

		// Keep connection open until client disconnects.
		<-r.Context().Done()
	})

	return httptest.NewServer(mux)
}

func TestManager_StartAndStop(t *testing.T) {
	srv := mockOpenCodeServer(t)
	defer srv.Close()

	mgr := NewManager(Config{BaseURL: srv.URL})

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if !mgr.IsRunning() {
		t.Error("expected IsRunning() to be true")
	}
	if mgr.Status() != "running" {
		t.Errorf("Status: got %q, want 'running'", mgr.Status())
	}

	if err := mgr.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	if mgr.IsRunning() {
		t.Error("expected IsRunning() to be false after Stop")
	}
	if mgr.Status() != "stopped" {
		t.Errorf("Status: got %q, want 'stopped'", mgr.Status())
	}
}

func TestManager_DoubleStart(t *testing.T) {
	srv := mockOpenCodeServer(t)
	defer srv.Close()

	mgr := NewManager(Config{BaseURL: srv.URL})

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer mgr.Stop()

	err := mgr.Start(ctx)
	if err == nil {
		t.Fatal("expected error on double Start")
	}
	if err.Error() != "manager already running" {
		t.Errorf("error: got %q, want 'manager already running'", err.Error())
	}
}

func TestManager_SendInput(t *testing.T) {
	srv := mockOpenCodeServer(t)
	defer srv.Close()

	mgr := NewManager(Config{BaseURL: srv.URL})

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer mgr.Stop()

	if err := mgr.SendInput("Hello OpenCode"); err != nil {
		t.Fatalf("SendInput failed: %v", err)
	}
}

func TestManager_SendInputWhenStopped(t *testing.T) {
	mgr := NewManager(Config{BaseURL: "http://localhost:99999"})

	err := mgr.SendInput("should fail")
	if err == nil {
		t.Fatal("expected error when sending input to stopped manager")
	}
}

func TestManager_SSEEvents(t *testing.T) {
	srv := mockOpenCodeServer(t)
	defer srv.Close()

	mgr := NewManager(Config{BaseURL: srv.URL})

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer mgr.Stop()

	events := mgr.ReadEvents()

	// Collect events with a timeout.
	var collected []string
	timeout := time.After(3 * time.Second)
	for {
		select {
		case evt, ok := <-events:
			if !ok {
				goto done
			}
			collected = append(collected, evt.Type)
			// We expect at least "assistant" and "result" from the mock.
			if len(collected) >= 2 {
				goto done
			}
		case <-timeout:
			goto done
		}
	}
done:

	if len(collected) < 2 {
		t.Fatalf("expected at least 2 events, got %d: %v", len(collected), collected)
	}
	if collected[0] != "assistant" {
		t.Errorf("event[0]: got %q, want 'assistant'", collected[0])
	}
	if collected[1] != "result" {
		t.Errorf("event[1]: got %q, want 'result'", collected[1])
	}
}

func TestManager_Restart(t *testing.T) {
	var mu sync.Mutex
	sessionCount := 0

	mux := http.NewServeMux()
	mux.HandleFunc("GET /global/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(HealthResponse{Healthy: true, Version: "1.0.0"})
	})
	mux.HandleFunc("POST /session", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sessionCount++
		id := fmt.Sprintf("session-%d", sessionCount)
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(CreateSessionResponse{ID: id})
	})
	mux.HandleFunc("POST /session/{id}/message", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /session/{id}/abort", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /global/event", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprintf(w, "event: server.connected\ndata: {}\n\n")
		flusher.Flush()
		<-r.Context().Done()
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	mgr := NewManager(Config{BaseURL: srv.URL})

	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Restart should create a new session.
	if err := mgr.Restart("continue from here"); err != nil {
		t.Fatalf("Restart failed: %v", err)
	}
	defer mgr.Stop()

	mu.Lock()
	count := sessionCount
	mu.Unlock()

	if count < 2 {
		t.Errorf("expected at least 2 sessions after restart, got %d", count)
	}

	if !mgr.IsRunning() {
		t.Error("expected manager to be running after restart")
	}
}

func TestManager_HealthCheckFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /global/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(HealthResponse{Healthy: false, Version: "1.0.0"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mgr := NewManager(Config{BaseURL: srv.URL})
	err := mgr.Start(context.Background())
	if err == nil {
		mgr.Stop()
		t.Fatal("expected error for unhealthy server")
	}
	if mgr.Status() != "error" {
		t.Errorf("Status: got %q, want 'error'", mgr.Status())
	}
}

func TestManager_ServerUnreachable(t *testing.T) {
	mgr := NewManager(Config{BaseURL: "http://127.0.0.1:1"})
	err := mgr.Start(context.Background())
	if err == nil {
		mgr.Stop()
		t.Fatal("expected error for unreachable server")
	}
}

func TestManager_BasicAuth(t *testing.T) {
	var authHeader string

	mux := http.NewServeMux()
	mux.HandleFunc("GET /global/health", func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(HealthResponse{Healthy: true, Version: "1.0.0"})
	})
	mux.HandleFunc("POST /session", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(CreateSessionResponse{ID: "auth-session"})
	})
	mux.HandleFunc("GET /global/event", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		<-r.Context().Done()
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	mgr := NewManager(Config{
		BaseURL:  srv.URL,
		Username: "myuser",
		Password: "mypass",
	})

	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer mgr.Stop()

	if authHeader == "" {
		t.Error("expected Authorization header to be set")
	}
}

func TestManager_DefaultUsername(t *testing.T) {
	mgr := NewManager(Config{
		BaseURL:  "http://localhost:4096",
		Password: "secret",
	})

	if mgr.config.Username != "opencode" {
		t.Errorf("default username: got %q, want 'opencode'", mgr.config.Username)
	}
}

// Verify Manager implements provider.AgentManager at compile time.
var _ interface {
	Start(ctx context.Context) error
	SendInput(input string) error
	Stop() error
	Status() string
	IsRunning() bool
} = (*Manager)(nil)
