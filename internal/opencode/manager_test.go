package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/helmcode/agent-crew/internal/provider"
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
		json.NewEncoder(w).Encode(createSessionResponse{
			ID:    "test-session-123",
			Title: "agentcrew-session",
		})
	})

	mux.HandleFunc("POST /session/{id}/prompt_async", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /session/{id}/message", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("POST /session/{id}/abort", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("GET /event", func(w http.ResponseWriter, r *http.Request) {
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

		// Send a text message part event.
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

	if err := mgr.Start(context.Background()); err != nil {
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
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer mgr.Stop()

	err := mgr.Start(context.Background())
	if err == nil {
		t.Fatal("expected error on double Start")
	}
	if err.Error() != "manager already running" {
		t.Errorf("error: got %q, want 'manager already running'", err.Error())
	}
}

func TestManager_SessionIDStored(t *testing.T) {
	srv := mockOpenCodeServer(t)
	defer srv.Close()

	mgr := NewManager(Config{BaseURL: srv.URL})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer mgr.Stop()

	mgr.mu.RLock()
	sid := mgr.sessionID
	mgr.mu.RUnlock()

	if sid != "test-session-123" {
		t.Errorf("sessionID: got %q, want 'test-session-123'", sid)
	}
}

func TestManager_SendInput(t *testing.T) {
	srv := mockOpenCodeServer(t)
	defer srv.Close()

	mgr := NewManager(Config{BaseURL: srv.URL})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer mgr.Stop()

	if err := mgr.SendInput("Hello OpenCode"); err != nil {
		t.Fatalf("SendInput failed: %v", err)
	}
}

func TestManager_SendInputRequestBody(t *testing.T) {
	var receivedBody []byte

	mux := http.NewServeMux()
	mux.HandleFunc("GET /global/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(HealthResponse{Healthy: true, Version: "1.0.0"})
	})
	mux.HandleFunc("POST /session", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(createSessionResponse{ID: "s1"})
	})
	mux.HandleFunc("POST /session/{id}/prompt_async", func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /session/{id}/message", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /session/{id}/abort", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /event", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		<-r.Context().Done()
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	mgr := NewManager(Config{
		BaseURL: srv.URL,
		Model:   "anthropic/claude-sonnet-4-20250514",
	})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer mgr.Stop()

	if err := mgr.SendInput("test prompt"); err != nil {
		t.Fatalf("SendInput failed: %v", err)
	}

	// Verify request body format.
	var req promptAsyncRequest
	if err := json.Unmarshal(receivedBody, &req); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}
	if len(req.Parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(req.Parts))
	}
	if req.Parts[0].Type != "text" {
		t.Errorf("part type: got %q, want 'text'", req.Parts[0].Type)
	}
	if req.Parts[0].Text != "test prompt" {
		t.Errorf("part text: got %q, want 'test prompt'", req.Parts[0].Text)
	}
	if req.Model == nil {
		t.Fatal("expected model to be set")
	}
	if req.Model.ProviderID != "anthropic" {
		t.Errorf("model providerID: got %q, want 'anthropic'", req.Model.ProviderID)
	}
	if req.Model.ModelID != "claude-sonnet-4-20250514" {
		t.Errorf("model modelID: got %q, want 'claude-sonnet-4-20250514'", req.Model.ModelID)
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
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer mgr.Stop()

	events := mgr.ReadEvents()

	var collected []string
	timeout := time.After(3 * time.Second)
	for {
		select {
		case evt, ok := <-events:
			if !ok {
				goto done
			}
			collected = append(collected, evt.Type)
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

func TestManager_SSEFiltersOtherSessions(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /global/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(HealthResponse{Healthy: true, Version: "1.0.0"})
	})
	mux.HandleFunc("POST /session", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(createSessionResponse{ID: "my-session"})
	})
	mux.HandleFunc("POST /session/{id}/abort", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /event", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		// Event from a different session — should be filtered.
		other, _ := json.Marshal(MessagePartPayload{
			SessionID: "other-session",
			Part:      Part{Type: "text", Content: json.RawMessage(`{"text":"not for me"}`)},
		})
		fmt.Fprintf(w, "event: message.part.updated\ndata: %s\n\n", other)
		flusher.Flush()

		// Event from our session — should pass through.
		mine, _ := json.Marshal(MessagePartPayload{
			SessionID: "my-session",
			Part:      Part{Type: "text", Content: json.RawMessage(`{"text":"for me"}`)},
		})
		fmt.Fprintf(w, "event: message.part.updated\ndata: %s\n\n", mine)
		flusher.Flush()

		// Idle for our session.
		idle, _ := json.Marshal(SessionIdlePayload{SessionID: "my-session"})
		fmt.Fprintf(w, "event: session.idle\ndata: %s\n\n", idle)
		flusher.Flush()

		<-r.Context().Done()
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	mgr := NewManager(Config{BaseURL: srv.URL})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer mgr.Stop()

	var collected []provider.StreamEvent
	timeout := time.After(3 * time.Second)
	for {
		select {
		case evt, ok := <-mgr.ReadEvents():
			if !ok {
				goto done
			}
			collected = append(collected, evt)
			if len(collected) >= 2 {
				goto done
			}
		case <-timeout:
			goto done
		}
	}
done:

	if len(collected) != 2 {
		t.Fatalf("expected 2 events (filtered), got %d", len(collected))
	}
	// The "other-session" event should have been filtered out.
	if collected[0].Type != "assistant" {
		t.Errorf("event[0]: got %q, want 'assistant'", collected[0].Type)
	}
	if collected[1].Type != "result" {
		t.Errorf("event[1]: got %q, want 'result'", collected[1].Type)
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
		json.NewEncoder(w).Encode(createSessionResponse{ID: id})
	})
	mux.HandleFunc("POST /session/{id}/prompt_async", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /session/{id}/message", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /session/{id}/abort", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /event", func(w http.ResponseWriter, r *http.Request) {
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

func TestManager_SystemPromptInjected(t *testing.T) {
	var systemMessageReceived bool
	var systemBody []byte

	mux := http.NewServeMux()
	mux.HandleFunc("GET /global/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(HealthResponse{Healthy: true, Version: "1.0.0"})
	})
	mux.HandleFunc("POST /session", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(createSessionResponse{ID: "sys-prompt-session"})
	})
	mux.HandleFunc("POST /session/{id}/message", func(w http.ResponseWriter, r *http.Request) {
		systemMessageReceived = true
		systemBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /session/{id}/abort", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /event", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		<-r.Context().Done()
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	mgr := NewManager(Config{
		BaseURL:      srv.URL,
		SystemPrompt: "You are a helpful assistant.",
	})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer mgr.Stop()

	if !systemMessageReceived {
		t.Fatal("expected system message to be sent")
	}

	var req systemMessageRequest
	if err := json.Unmarshal(systemBody, &req); err != nil {
		t.Fatalf("failed to parse system message body: %v", err)
	}
	if !req.System {
		t.Error("expected system=true")
	}
	if !req.NoReply {
		t.Error("expected noReply=true")
	}
	if len(req.Parts) != 1 || req.Parts[0].Text != "You are a helpful assistant." {
		t.Errorf("unexpected parts: %+v", req.Parts)
	}
}

func TestManager_HealthCheckFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /global/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(HealthResponse{Healthy: false, Version: "1.0.0"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mgr := NewManager(Config{BaseURL: srv.URL, HealthTimeout: 2 * time.Second})
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
	mgr := NewManager(Config{BaseURL: "http://127.0.0.1:1", HealthTimeout: 2 * time.Second})
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
		json.NewEncoder(w).Encode(createSessionResponse{ID: "auth-session"})
	})
	mux.HandleFunc("POST /session/{id}/abort", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /event", func(w http.ResponseWriter, r *http.Request) {
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

func TestManager_ParseModel(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		wantNil    bool
		providerID string
		modelID    string
	}{
		{"valid", "anthropic/claude-sonnet-4-20250514", false, "anthropic", "claude-sonnet-4-20250514"},
		{"empty", "", true, "", ""},
		{"no-slash", "justmodel", true, "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := &Manager{config: Config{Model: tt.model}}
			mc := mgr.parseModel()
			if tt.wantNil {
				if mc != nil {
					t.Errorf("expected nil, got %+v", mc)
				}
				return
			}
			if mc == nil {
				t.Fatal("expected non-nil modelConfig")
			}
			if mc.ProviderID != tt.providerID {
				t.Errorf("ProviderID: got %q, want %q", mc.ProviderID, tt.providerID)
			}
			if mc.ModelID != tt.modelID {
				t.Errorf("ModelID: got %q, want %q", mc.ModelID, tt.modelID)
			}
		})
	}
}

func TestManager_SendInputQueuesWhenBusy(t *testing.T) {
	var mu sync.Mutex
	promptCount := 0

	mux := http.NewServeMux()
	mux.HandleFunc("GET /global/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(HealthResponse{Healthy: true, Version: "1.0.0"})
	})
	mux.HandleFunc("POST /session", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(createSessionResponse{ID: "queue-session"})
	})
	mux.HandleFunc("POST /session/{id}/prompt_async", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		promptCount++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /session/{id}/abort", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /event", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		<-r.Context().Done()
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	mgr := NewManager(Config{BaseURL: srv.URL})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer mgr.Stop()

	// First SendInput: should go through immediately (sets busy=true).
	if err := mgr.SendInput("message A"); err != nil {
		t.Fatalf("SendInput A failed: %v", err)
	}

	// Second and third: should be queued (busy=true).
	if err := mgr.SendInput("message B"); err != nil {
		t.Fatalf("SendInput B failed: %v", err)
	}
	if err := mgr.SendInput("message C"); err != nil {
		t.Fatalf("SendInput C failed: %v", err)
	}

	// Only 1 prompt should have been sent to the server.
	mu.Lock()
	count := promptCount
	mu.Unlock()
	if count != 1 {
		t.Errorf("expected 1 prompt sent, got %d", count)
	}

	// Queue should have 2 pending messages.
	mgr.queueMu.Lock()
	pending := len(mgr.pendingInputs)
	mgr.queueMu.Unlock()
	if pending != 2 {
		t.Errorf("expected 2 pending messages, got %d", pending)
	}
}

func TestManager_QueueDrainsOnResult(t *testing.T) {
	var mu sync.Mutex
	var prompts []string

	// Channel to control SSE event delivery.
	sendIdle := make(chan struct{}, 5)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /global/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(HealthResponse{Healthy: true, Version: "1.0.0"})
	})
	mux.HandleFunc("POST /session", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(createSessionResponse{ID: "drain-session"})
	})
	mux.HandleFunc("POST /session/{id}/prompt_async", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req promptAsyncRequest
		json.Unmarshal(body, &req)
		mu.Lock()
		if len(req.Parts) > 0 {
			prompts = append(prompts, req.Parts[0].Text)
		}
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /session/{id}/abort", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /event", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		for {
			select {
			case <-sendIdle:
				idle, _ := json.Marshal(SessionIdlePayload{SessionID: "drain-session"})
				fmt.Fprintf(w, "event: session.idle\ndata: %s\n\n", idle)
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	mgr := NewManager(Config{BaseURL: srv.URL})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer mgr.Stop()

	// Drain the ReadEvents channel to prevent blocking.
	go func() {
		for range mgr.ReadEvents() {
		}
	}()

	// Send message A (goes through), then B and C (queued).
	mgr.SendInput("msg-A")
	mgr.SendInput("msg-B")
	mgr.SendInput("msg-C")

	// Trigger session.idle → should drain msg-B.
	sendIdle <- struct{}{}
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	count := len(prompts)
	mu.Unlock()
	if count != 2 {
		t.Fatalf("expected 2 prompts sent after first idle, got %d", count)
	}

	// Trigger session.idle → should drain msg-C.
	sendIdle <- struct{}{}
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	count = len(prompts)
	mu.Unlock()
	if count != 3 {
		t.Fatalf("expected 3 prompts sent after second idle, got %d", count)
	}

	// Verify order.
	mu.Lock()
	defer mu.Unlock()
	expected := []string{"msg-A", "msg-B", "msg-C"}
	for i, want := range expected {
		if i >= len(prompts) {
			t.Errorf("missing prompt[%d]: want %q", i, want)
			continue
		}
		if prompts[i] != want {
			t.Errorf("prompt[%d]: got %q, want %q", i, prompts[i], want)
		}
	}
}

func TestManager_StopClearsQueue(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /global/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(HealthResponse{Healthy: true, Version: "1.0.0"})
	})
	mux.HandleFunc("POST /session", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(createSessionResponse{ID: "stop-session"})
	})
	mux.HandleFunc("POST /session/{id}/prompt_async", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /session/{id}/abort", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /event", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		<-r.Context().Done()
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	mgr := NewManager(Config{BaseURL: srv.URL})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Send first message and queue two more.
	mgr.SendInput("msg-1")
	mgr.SendInput("msg-2")
	mgr.SendInput("msg-3")

	// Stop should clear the queue.
	mgr.Stop()

	mgr.queueMu.Lock()
	pending := len(mgr.pendingInputs)
	busy := mgr.busy
	mgr.queueMu.Unlock()

	if pending != 0 {
		t.Errorf("expected 0 pending messages after Stop, got %d", pending)
	}
	if busy {
		t.Error("expected busy=false after Stop")
	}
}

// Verify Manager implements provider.AgentManager at compile time.
var _ provider.AgentManager = (*Manager)(nil)
