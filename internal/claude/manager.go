// Package claude manages Claude Code CLI processes within the agent sidecar.
package claude

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// ProcessConfig holds the configuration for spawning a Claude Code process.
type ProcessConfig struct {
	SystemPrompt string
	AllowedTools []string
	WorkDir      string
	MaxTokens    int
}

// Manager manages the lifecycle of a Claude Code headless process.
type Manager struct {
	config ProcessConfig
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	events chan StreamEvent
	done   chan struct{} // closed when process exits and output is drained
	status string
	mu     sync.RWMutex
}

// NewManager creates a new Manager with the given config.
func NewManager(config ProcessConfig) *Manager {
	return &Manager{
		config: config,
		status: "stopped",
		events: make(chan StreamEvent, 256),
	}
}

// Start spawns the Claude Code CLI process in headless stream-json mode.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.status == "running" {
		return fmt.Errorf("process already running")
	}

	args := []string{
		"-p", m.config.SystemPrompt,
		"--output-format", "stream-json",
		"--verbose",
	}

	for _, tool := range m.config.AllowedTools {
		args = append(args, "--allowedTools", tool)
	}

	m.cmd = exec.CommandContext(ctx, "claude", args...)
	m.cmd.Dir = m.config.WorkDir
	// Construct minimal environment — do not inherit all env vars.
	m.cmd.Env = []string{
		"CLAUDE_HEADLESS=1",
		"HOME=" + os.Getenv("HOME"),
		"PATH=" + os.Getenv("PATH"),
		"ANTHROPIC_API_KEY=" + os.Getenv("ANTHROPIC_API_KEY"),
	}

	var err error
	m.stdin, err = m.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("creating stdin pipe: %w", err)
	}

	m.stdout, err = m.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := m.cmd.Start(); err != nil {
		return fmt.Errorf("starting claude process: %w", err)
	}

	m.status = "running"
	m.done = make(chan struct{})
	slog.Info("claude process started", "pid", m.cmd.Process.Pid, "workdir", m.config.WorkDir)

	// parseOutput reads stdout and writes to events channel.
	// When stdout is exhausted (process exits), it closes the events channel.
	// monitor then waits for the process and updates status.
	var parseWg sync.WaitGroup
	parseWg.Add(1)

	go func() {
		defer parseWg.Done()
		ParseStreamOutput(m.stdout, m.events)
	}()

	go m.monitor(&parseWg)

	return nil
}

// Stop gracefully shuts down the Claude process.
// It signals the process and waits for monitor() to complete cleanup.
func (m *Manager) Stop() error {
	m.mu.Lock()

	if m.cmd == nil || m.cmd.Process == nil {
		m.mu.Unlock()
		return nil
	}

	slog.Info("stopping claude process", "pid", m.cmd.Process.Pid)

	// Try SIGTERM first.
	if err := m.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		slog.Warn("failed to send SIGTERM, sending SIGKILL", "error", err)
		_ = m.cmd.Process.Kill()
	}

	done := m.done
	m.mu.Unlock()

	// Wait for monitor() to complete (which calls the single cmd.Wait()).
	select {
	case <-done:
		// Process exited and cleanup is done.
	case <-time.After(10 * time.Second):
		m.mu.Lock()
		slog.Warn("claude process did not exit after SIGTERM, killing")
		if m.cmd != nil && m.cmd.Process != nil {
			_ = m.cmd.Process.Kill()
		}
		m.mu.Unlock()

		// Wait again after kill.
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			slog.Error("claude process did not exit after SIGKILL")
		}
	}

	return nil
}

// SendInput writes data to the Claude process stdin.
func (m *Manager) SendInput(input string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.stdin == nil || m.status != "running" {
		return fmt.Errorf("process is not running")
	}

	_, err := fmt.Fprintln(m.stdin, input)
	return err
}

// ReadEvents returns a read-only channel that emits parsed stdout events.
func (m *Manager) ReadEvents() <-chan StreamEvent {
	return m.events
}

// Restart stops the current process and starts a new one with a resumption prompt.
func (m *Manager) Restart(resumePrompt string) error {
	if err := m.Stop(); err != nil {
		slog.Warn("error stopping process for restart", "error", err)
	}

	m.mu.Lock()
	m.config.SystemPrompt = resumePrompt
	m.events = make(chan StreamEvent, 256)
	m.mu.Unlock()

	return m.Start(context.Background())
}

// Status returns the current process status.
func (m *Manager) Status() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

// IsRunning returns true if the process is actively running.
func (m *Manager) IsRunning() bool {
	return m.Status() == "running"
}

// monitor waits for the process to exit, ensures the output parser finishes,
// then closes the events channel and signals done.
func (m *Manager) monitor(parseWg *sync.WaitGroup) {
	// This is the ONLY call to cmd.Wait() — prevents double-Wait race.
	err := m.cmd.Wait()

	// Wait for parseOutput to drain all buffered stdout before closing.
	parseWg.Wait()

	m.mu.Lock()
	defer m.mu.Unlock()

	if err != nil {
		slog.Error("claude process exited with error", "error", err)
		m.status = "error"
	} else {
		slog.Info("claude process exited normally")
		m.status = "stopped"
	}

	close(m.events)
	close(m.done)
}
