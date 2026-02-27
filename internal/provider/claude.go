package provider

import (
	"context"
	"encoding/json"

	"github.com/helmcode/agent-crew/internal/claude"
)

// ClaudeManager wraps claude.Manager to implement the AgentManager interface.
// It is a thin adapter that delegates all operations to the underlying manager
// and converts claude.StreamEvent to provider.StreamEvent.
type ClaudeManager struct {
	inner  *claude.Manager
	events chan StreamEvent
}

// NewClaudeManager creates a ClaudeManager wrapping the given claude.Manager.
func NewClaudeManager(m *claude.Manager) *ClaudeManager {
	cm := &ClaudeManager{
		inner:  m,
		events: make(chan StreamEvent, 256),
	}
	go cm.convertEvents()
	return cm
}

// Start delegates to the underlying claude.Manager.Start.
func (c *ClaudeManager) Start(ctx context.Context) error {
	return c.inner.Start(ctx)
}

// SendInput delegates to the underlying claude.Manager.SendInput.
func (c *ClaudeManager) SendInput(input string) error {
	return c.inner.SendInput(input)
}

// ReadEvents returns a channel of provider.StreamEvent converted from claude events.
func (c *ClaudeManager) ReadEvents() <-chan StreamEvent {
	return c.events
}

// Restart delegates to the underlying claude.Manager.Restart.
func (c *ClaudeManager) Restart(resumePrompt string) error {
	return c.inner.Restart(resumePrompt)
}

// Stop delegates to the underlying claude.Manager.Stop.
func (c *ClaudeManager) Stop() error {
	return c.inner.Stop()
}

// Status delegates to the underlying claude.Manager.Status.
func (c *ClaudeManager) Status() string {
	return c.inner.Status()
}

// IsRunning delegates to the underlying claude.Manager.IsRunning.
func (c *ClaudeManager) IsRunning() bool {
	return c.inner.IsRunning()
}

// convertEvents reads claude.StreamEvent from the inner manager and converts
// them to provider.StreamEvent, forwarding to the events channel.
func (c *ClaudeManager) convertEvents() {
	for ce := range c.inner.ReadEvents() {
		pe := StreamEvent{
			Type:      ce.Type,
			Name:      ce.Name,
			IsError:   ce.IsError,
			Result:    ce.Result,
			SessionID: ce.SessionID,
		}

		// Convert json.RawMessage fields to strings.
		if len(ce.Message) > 0 {
			pe.Message = string(ce.Message)
		}
		if len(ce.Input) > 0 {
			pe.Input = string(ce.Input)
		}

		select {
		case c.events <- pe:
		default:
			// Drop event if channel is full (same behavior as claude.ParseStreamOutput).
		}
	}
	close(c.events)
}

// InnerManager returns the underlying claude.Manager for operations that need
// direct access (e.g. ExtractToolCommand, FormatToolResult).
func (c *ClaudeManager) InnerManager() *claude.Manager {
	return c.inner
}

// ToClaudeStreamEvent converts a provider.StreamEvent back to a claude.StreamEvent.
// This is used by the bridge for operations that still need the claude-specific type
// (e.g. ExtractToolCommand, JSON marshaling for activity events).
func ToClaudeStreamEvent(pe *StreamEvent) *claude.StreamEvent {
	ce := &claude.StreamEvent{
		Type:      pe.Type,
		Name:      pe.Name,
		IsError:   pe.IsError,
		Result:    pe.Result,
		SessionID: pe.SessionID,
	}
	if pe.Message != "" {
		ce.Message = json.RawMessage(pe.Message)
	}
	if pe.Input != "" {
		ce.Input = json.RawMessage(pe.Input)
	}
	return ce
}
