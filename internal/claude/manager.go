// Package claude manages Claude Code CLI processes within the agent sidecar.
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
)

// ProcessConfig holds the configuration for spawning a Claude Code process.
type ProcessConfig struct {
	SystemPrompt string
	AllowedTools []string
	WorkDir      string
	MaxTokens    int
}

// Manager manages the lifecycle of Claude Code CLI invocations.
// Each SendInput call spawns a new `claude -p` process. Conversation continuity
// is maintained via --resume <session_id>.
type Manager struct {
	config    ProcessConfig
	sessionID string           // captured from the first invocation
	events    chan StreamEvent  // bridge reads from this
	status    string
	mu        sync.RWMutex
}

// NewManager creates a new Manager with the given config.
func NewManager(config ProcessConfig) *Manager {
	return &Manager{
		config: config,
		status: "stopped",
		events: make(chan StreamEvent, 256),
	}
}

// Start initializes the manager and runs the system prompt to establish a session.
// The system prompt is sent as the first message to `claude -p`, and the
// session_id from the response is saved for subsequent --resume calls.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.status == "running" {
		return fmt.Errorf("manager already running")
	}

	m.status = "running"

	// If there's a system prompt, run it now to establish the session.
	if m.config.SystemPrompt != "" {
		slog.Info("initializing claude session with system prompt",
			"prompt_length", len(m.config.SystemPrompt),
			"workdir", m.config.WorkDir,
		)

		sessionID, err := m.runInitialPrompt(ctx, m.config.SystemPrompt)
		if err != nil {
			m.status = "error"
			return fmt.Errorf("initializing claude session: %w", err)
		}
		m.sessionID = sessionID
		slog.Info("claude session established", "session_id", m.sessionID)
	} else {
		slog.Info("manager started without system prompt, session will be created on first SendInput")
	}

	return nil
}

// runInitialPrompt runs `claude -p "<prompt>" --output-format json` to establish
// a session. Returns the session_id from the JSON response.
func (m *Manager) runInitialPrompt(ctx context.Context, prompt string) (string, error) {
	args := []string{
		"-p", prompt,
		"--output-format", "json",
		"--verbose",
	}
	for _, tool := range m.config.AllowedTools {
		args = append(args, "--allowedTools", tool)
	}

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = m.config.WorkDir
	cmd.Env = m.buildEnv()
	cmd.Stderr = os.Stderr

	slog.Info("running initial claude prompt",
		"command", "claude",
		"args", args,
		"has_api_key", os.Getenv("ANTHROPIC_API_KEY") != "",
		"has_oauth_token", os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != "",
	)

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("claude initial prompt failed: %w", err)
	}

	slog.Info("initial prompt completed", "output_length", len(output))

	// Parse session_id from JSON response.
	var result struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return "", fmt.Errorf("parsing claude response: %w (output: %s)", err, truncate(string(output), 500))
	}
	if result.SessionID == "" {
		return "", fmt.Errorf("claude response missing session_id (output: %s)", truncate(string(output), 500))
	}

	return result.SessionID, nil
}

// SendInput sends a message to Claude by spawning a new process with --resume.
// Stream events are emitted to the events channel for the bridge to consume.
func (m *Manager) SendInput(input string) error {
	m.mu.Lock()
	if m.status != "running" {
		m.mu.Unlock()
		return fmt.Errorf("process is not running")
	}

	sessionID := m.sessionID
	m.mu.Unlock()

	slog.Info("sending input to claude",
		"input_length", len(input),
		"has_session", sessionID != "",
		"session_id", sessionID,
	)

	// Build args for this invocation.
	args := []string{
		"-p", input,
		"--output-format", "stream-json",
		"--verbose",
	}
	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	}
	for _, tool := range m.config.AllowedTools {
		args = append(args, "--allowedTools", tool)
	}

	ctx := context.Background()
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = m.config.WorkDir
	cmd.Env = m.buildEnv()

	// Capture stderr for debugging.
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("creating stdout pipe: %w", err)
	}

	slog.Info("starting claude process for input",
		"command", "claude",
		"args", args,
	)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting claude process: %w", err)
	}

	slog.Info("claude process started", "pid", cmd.Process.Pid)

	// Parse stream output in current goroutine — SendInput blocks until done.
	// This is intentional: the bridge calls SendInput from handleUserMessage
	// and the events channel delivers events to forwardEvents.
	resultSessionID := ParseStreamOutput(stdout, m.events)

	// Wait for process to finish.
	if err := cmd.Wait(); err != nil {
		stderrStr := stderrBuf.String()
		exitCode := -1
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
		slog.Error("claude process exited with error",
			"error", err,
			"exit_code", exitCode,
			"stderr", truncate(stderrStr, 1000),
		)
		// Don't return error — the stream events already went through.
		// The bridge handles result/error events.
	} else {
		exitCode := 0
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
		slog.Info("claude process completed", "pid", cmd.Process.Pid, "exit_code", exitCode)
	}

	// Log stderr if non-empty (even on success).
	if stderrStr := stderrBuf.String(); stderrStr != "" {
		slog.Info("claude stderr output", "stderr", truncate(stderrStr, 2000))
	}

	// Capture session_id from the stream result event. This ensures conversation
	// continuity even when the manager started without a system prompt (no
	// initial session_id). It also handles session rotation by Claude CLI.
	if resultSessionID != "" {
		m.mu.Lock()
		if m.sessionID != resultSessionID {
			slog.Info("session_id updated from stream result",
				"old_session_id", m.sessionID,
				"new_session_id", resultSessionID,
			)
			m.sessionID = resultSessionID
		}
		m.mu.Unlock()
	}

	return nil
}

// ReadEvents returns a read-only channel that emits parsed stdout events.
func (m *Manager) ReadEvents() <-chan StreamEvent {
	return m.events
}

// Restart stops the current manager and starts a new one with a resumption prompt.
func (m *Manager) Restart(resumePrompt string) error {
	if err := m.Stop(); err != nil {
		slog.Warn("error stopping manager for restart", "error", err)
	}

	m.mu.Lock()
	m.config.SystemPrompt = resumePrompt
	m.sessionID = "" // new session
	m.events = make(chan StreamEvent, 256)
	m.mu.Unlock()

	return m.Start(context.Background())
}

// Stop marks the manager as stopped.
func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	slog.Info("stopping claude manager", "session_id", m.sessionID)
	m.status = "stopped"
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

// buildEnv inherits the full parent environment and overrides specific vars.
// A minimal env breaks Node.js (missing NODE_VERSION, npm paths, etc.).
func (m *Manager) buildEnv() []string {
	env := os.Environ()
	env = append(env, "CLAUDE_HEADLESS=1")
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		env = append(env, "ANTHROPIC_API_KEY="+key)
	}
	if token := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"); token != "" {
		env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+token)
	}
	return env
}

// truncate returns the first n characters of s, appending "..." if truncated.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
