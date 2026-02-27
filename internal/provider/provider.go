// Package provider defines the AgentManager interface for abstracting different
// AI agent backends (Claude Code, OpenCode, etc.).
package provider

import "context"

// AgentManager is the interface for managing an AI agent process lifecycle.
// Implementations bridge the gap between the NATS messaging layer and a
// specific CLI tool (e.g. Claude Code, OpenCode).
type AgentManager interface {
	Start(ctx context.Context) error
	SendInput(input string) error
	ReadEvents() <-chan StreamEvent
	Restart(resumePrompt string) error
	Stop() error
	Status() string
	IsRunning() bool
}

// StreamEvent represents a single event from an agent's output stream.
// This is the provider-agnostic version of claude.StreamEvent.
type StreamEvent struct {
	Type      string // "assistant", "tool_use", "tool_result", "result", "error"
	Message   string
	Name      string // Tool name (for tool_use events)
	Input     string // Tool input (for tool_use events)
	IsError   bool
	Result    string
	SessionID string
}
