package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/helmcode/agent-crew/internal/provider"
)

// Config holds the configuration for the OpenCode manager.
type Config struct {
	// BaseURL is the URL of the running `opencode serve` instance (e.g. "http://127.0.0.1:4096").
	BaseURL string
	// Username for HTTP Basic Auth (default: "opencode").
	Username string
	// Password for HTTP Basic Auth (empty = no auth).
	Password string
	// SystemPrompt is injected as a system message at session start.
	SystemPrompt string
	// Model in "providerID/modelID" format (e.g. "anthropic/claude-sonnet-4-20250514").
	Model string
	// HealthTimeout is the maximum time to wait for the server to become healthy.
	// Defaults to 30s if zero.
	HealthTimeout time.Duration
}

// modelConfig is the JSON model object for OpenCode API requests.
type modelConfig struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
}

// createSessionRequest is the request body for POST /session.
type createSessionRequest struct {
	Title string `json:"title,omitempty"`
}

// createSessionResponse is the response from POST /session.
type createSessionResponse struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// promptAsyncRequest is the request body for POST /session/:id/prompt_async.
type promptAsyncRequest struct {
	Parts   []messagePart `json:"parts"`
	Model   *modelConfig  `json:"model,omitempty"`
	NoReply bool          `json:"noReply,omitempty"`
}

// systemMessageRequest is the request body for POST /session/:id/message
// used to inject system prompts.
type systemMessageRequest struct {
	Parts   []messagePart `json:"parts"`
	System  bool          `json:"system"`
	NoReply bool          `json:"noReply"`
}

// messagePart is a single part in an OpenCode message.
type messagePart struct {
	Type string `json:"type"` // "text"
	Text string `json:"text"`
}

// Manager implements provider.AgentManager for OpenCode.
// It communicates with `opencode serve` via HTTP REST for commands
// and SSE for streaming events.
type Manager struct {
	config    Config
	client    *http.Client
	sessionID string
	events    chan provider.StreamEvent
	status    string
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	mu        sync.RWMutex
}

// NewManager creates a new OpenCode Manager with the given config.
func NewManager(config Config) *Manager {
	if config.Username == "" {
		config.Username = "opencode"
	}
	return &Manager{
		config: config,
		client: &http.Client{Timeout: 30 * time.Second},
		status: "stopped",
		events: make(chan provider.StreamEvent, 256),
	}
}

// Start connects to the OpenCode server, creates a session, injects the system
// prompt, and starts the SSE event listener.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.status == "running" {
		return fmt.Errorf("manager already running")
	}

	// Wait for server to be healthy (with retry).
	healthTimeout := m.config.HealthTimeout
	if healthTimeout == 0 {
		healthTimeout = 30 * time.Second
	}
	if err := WaitForHealth(m.config.BaseURL, m.config.Username, m.config.Password, healthTimeout); err != nil {
		m.status = "error"
		return fmt.Errorf("opencode server health check failed: %w", err)
	}

	// Create a new session.
	sessionID, err := m.createSession(ctx)
	if err != nil {
		m.status = "error"
		return fmt.Errorf("creating opencode session: %w", err)
	}

	m.sessionID = sessionID
	m.status = "running"

	// Start SSE listener in background. Pass sessionID as param to avoid
	// lock contention with Stop() which holds the write lock during wg.Wait().
	sseCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.wg.Add(1)
	go m.listenSSE(sseCtx, m.sessionID)

	// Inject system prompt if configured (noReply = true, system = true).
	if m.config.SystemPrompt != "" {
		if err := m.injectSystemPrompt(ctx); err != nil {
			slog.Warn("failed to inject system prompt", "error", err)
		}
	}

	slog.Info("opencode manager started",
		"base_url", m.config.BaseURL,
		"session_id", m.sessionID,
	)

	return nil
}

// SendInput sends a message to the active OpenCode session via prompt_async.
// Returns immediately; events arrive via the SSE stream.
func (m *Manager) SendInput(input string) error {
	m.mu.RLock()
	if m.status != "running" {
		m.mu.RUnlock()
		return fmt.Errorf("manager is not running")
	}
	sessionID := m.sessionID
	m.mu.RUnlock()

	slog.Info("sending input to opencode",
		"input_length", len(input),
		"session_id", sessionID,
	)

	reqBody := promptAsyncRequest{
		Parts: []messagePart{{Type: "text", Text: input}},
		Model: m.parseModel(),
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshaling prompt request: %w", err)
	}

	url := fmt.Sprintf("%s/session/%s/prompt_async", m.config.BaseURL, sessionID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating prompt request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	m.setAuth(req)

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("sending prompt to opencode: %w", err)
	}
	defer resp.Body.Close()

	// prompt_async returns 204 No Content on success.
	if resp.StatusCode != http.StatusNoContent &&
		resp.StatusCode != http.StatusOK &&
		resp.StatusCode != http.StatusAccepted {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("opencode prompt_async failed (status %d): %s", resp.StatusCode, truncate(string(respBody), 500))
	}

	slog.Info("opencode prompt sent", "session_id", sessionID)
	return nil
}

// ReadEvents returns a channel of provider.StreamEvent from the SSE stream.
func (m *Manager) ReadEvents() <-chan provider.StreamEvent {
	return m.events
}

// Restart stops the current session and creates a new one.
func (m *Manager) Restart(resumePrompt string) error {
	if err := m.Stop(); err != nil {
		slog.Warn("error stopping opencode manager for restart", "error", err)
	}

	m.mu.Lock()
	// Drain existing events.
	for {
		select {
		case <-m.events:
		default:
			goto drained
		}
	}
drained:
	m.mu.Unlock()

	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		return err
	}

	// If there's a resume prompt, send it as the first message.
	if resumePrompt != "" {
		return m.SendInput(resumePrompt)
	}

	return nil
}

// Stop aborts the active session and shuts down the SSE listener.
func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	slog.Info("stopping opencode manager", "session_id", m.sessionID)

	// Cancel SSE listener.
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}

	// Abort the session if running.
	if m.sessionID != "" && m.status == "running" {
		m.abortSession(m.sessionID)
	}

	m.status = "stopped"

	// Wait for SSE goroutine to finish.
	m.wg.Wait()

	return nil
}

// Status returns the current manager status.
func (m *Manager) Status() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

// IsRunning returns true if the manager is ready to accept input.
func (m *Manager) IsRunning() bool {
	return m.Status() == "running"
}

// createSession creates a new OpenCode session via POST /session.
func (m *Manager) createSession(ctx context.Context) (string, error) {
	reqBody := createSessionRequest{
		Title: "agentcrew-session",
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("%s/session", m.config.BaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	m.setAuth(req)

	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("creating session: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("create session failed (status %d): %s", resp.StatusCode, truncate(string(respBody), 500))
	}

	var session createSessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		return "", fmt.Errorf("parsing session response: %w", err)
	}

	if session.ID == "" {
		return "", fmt.Errorf("opencode returned empty session ID")
	}

	slog.Info("opencode session created", "session_id", session.ID)
	return session.ID, nil
}

// injectSystemPrompt sends a system message to the session without expecting a reply.
func (m *Manager) injectSystemPrompt(ctx context.Context) error {
	reqBody := systemMessageRequest{
		Parts:   []messagePart{{Type: "text", Text: m.config.SystemPrompt}},
		System:  true,
		NoReply: true,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/session/%s/message", m.config.BaseURL, m.sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	m.setAuth(req)

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("injecting system prompt: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("system prompt injection failed (status %d): %s", resp.StatusCode, truncate(string(respBody), 500))
	}

	slog.Info("opencode system prompt injected", "session_id", m.sessionID, "length", len(m.config.SystemPrompt))
	return nil
}

// abortSession sends POST /session/:id/abort to stop a running session.
func (m *Manager) abortSession(sessionID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	url := fmt.Sprintf("%s/session/%s/abort", m.config.BaseURL, sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		slog.Warn("failed to create abort request", "error", err)
		return
	}
	m.setAuth(req)

	resp, err := m.client.Do(req)
	if err != nil {
		slog.Warn("failed to abort opencode session", "session_id", sessionID, "error", err)
		return
	}
	resp.Body.Close()
	slog.Info("opencode session aborted", "session_id", sessionID, "status", resp.StatusCode)
}

// listenSSE connects to the SSE endpoint and converts events to provider.StreamEvent.
// Events are filtered to only include those for the given sessionID.
func (m *Manager) listenSSE(ctx context.Context, sessionID string) {
	defer m.wg.Done()

	url := fmt.Sprintf("%s/event", m.config.BaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		slog.Error("failed to create SSE request", "error", err)
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	m.setAuth(req)

	// Use a separate client without timeout for SSE (long-lived connection).
	sseClient := &http.Client{}
	resp, err := sseClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return // Context cancelled, normal shutdown.
		}
		slog.Error("failed to connect to SSE endpoint", "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Error("SSE endpoint returned non-200", "status", resp.StatusCode)
		return
	}

	slog.Info("opencode SSE stream connected", "url", url)

	// Parse SSE events in a goroutine.
	sseEvents := make(chan SSEEvent, 256)
	go func() {
		ParseSSEStream(resp.Body, sseEvents)
		close(sseEvents)
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-sseEvents:
			if !ok {
				slog.Info("opencode SSE stream closed")
				return
			}

			// Convert and filter by session ID.
			pe := ConvertSSEToProviderEvent(evt, sessionID)
			if pe == nil {
				continue
			}

			select {
			case m.events <- *pe:
			default:
				slog.Warn("provider event channel full, dropping event", "type", pe.Type)
			}
		}
	}
}

// parseModel splits the "providerID/modelID" format into a modelConfig.
// Returns nil if no model is configured.
func (m *Manager) parseModel() *modelConfig {
	if m.config.Model == "" {
		return nil
	}
	parts := strings.SplitN(m.config.Model, "/", 2)
	if len(parts) != 2 {
		slog.Warn("invalid model format, expected 'providerID/modelID'", "model", m.config.Model)
		return nil
	}
	return &modelConfig{
		ProviderID: parts[0],
		ModelID:    parts[1],
	}
}

// setAuth applies HTTP Basic Auth to the request if credentials are configured.
func (m *Manager) setAuth(req *http.Request) {
	if m.config.Password != "" {
		req.SetBasicAuth(m.config.Username, m.config.Password)
	}
}

// truncate returns the first n characters of s, appending "..." if truncated.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
