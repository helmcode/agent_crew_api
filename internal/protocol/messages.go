// Package protocol defines shared message types for the AgentCrew JSON protocol.
package protocol

import (
	"encoding/json"
	"time"
)

// MessageType identifies the kind of protocol message.
type MessageType string

const (
	TypeUserMessage          MessageType = "user_message"
	TypeLeaderResponse       MessageType = "leader_response"
	TypeSystemCommand        MessageType = "system_command"
	TypeActivityEvent        MessageType = "activity_event"
	TypeContainerValidation  MessageType = "container_validation"
	TypeSkillStatus          MessageType = "skill_status"
	TypeMcpStatus            MessageType = "mcp_status"
)

// MessageContext carries optional conversation context.
type MessageContext struct {
	ThreadID    string   `json:"thread_id,omitempty"`
	RelevantIDs []string `json:"relevant_ids,omitempty"`
}

// Message is the envelope for all NATS messages in the system.
type Message struct {
	MessageID    string          `json:"message_id"`
	From         string          `json:"from"`
	To           string          `json:"to"`
	Type         MessageType     `json:"type"`
	Context      *MessageContext `json:"context,omitempty"`
	RefMessageID string          `json:"ref_message_id,omitempty"`
	Payload      json.RawMessage `json:"payload"`
	Timestamp    time.Time       `json:"timestamp"`
}

// FileRef describes a file uploaded alongside a chat message.
type FileRef struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
	Type string `json:"type"`
}

// UserMessagePayload carries a free-form user message.
type UserMessagePayload struct {
	Content        string    `json:"content"`
	Files          []FileRef `json:"files,omitempty"`
	Source         string    `json:"source,omitempty"`           // "chat", "scheduler", or "webhook"
	ScheduledRunID string    `json:"scheduled_run_id,omitempty"` // Set when source is "scheduler"
	WebhookRunID   string    `json:"webhook_run_id,omitempty"`   // Set when source is "webhook"
}

// LeaderResponsePayload carries the leader's response back to the user.
type LeaderResponsePayload struct {
	Status         string `json:"status"` // completed, failed, partial
	Result         string `json:"result"`
	Error          string `json:"error,omitempty"`
	ScheduledRunID string `json:"scheduled_run_id,omitempty"` // Correlation ID for scheduled runs
	WebhookRunID   string `json:"webhook_run_id,omitempty"`   // Correlation ID for webhook runs
}

// SystemCommandPayload carries a system-level command.
type SystemCommandPayload struct {
	Command string            `json:"command"` // shutdown, restart, compact_context
	Args    map[string]string `json:"args,omitempty"`
}

// ActivityEventPayload carries an intermediate activity event from the Claude
// Code process (tool calls, assistant messages, sub-agent delegation, etc.).
type ActivityEventPayload struct {
	EventType string          `json:"event_type"`          // tool_use, assistant, tool_result, system
	AgentName string          `json:"agent_name"`          // Name of the agent producing the event
	ToolName  string          `json:"tool_name,omitempty"` // Tool name (for tool_use events)
	Action    string          `json:"action,omitempty"`    // Human-readable action summary
	Payload   json.RawMessage `json:"payload,omitempty"`   // Raw event data
}

// ValidationCheckStatus represents the result status of a single validation check.
type ValidationCheckStatus string

const (
	ValidationOK      ValidationCheckStatus = "ok"
	ValidationWarning ValidationCheckStatus = "warning"
	ValidationError   ValidationCheckStatus = "error"
)

// ValidationCheck represents the result of a single container validation check.
type ValidationCheck struct {
	Name    string                `json:"name"`    // Identifier for the check (e.g., "claude_md", "agents_dir")
	Status  ValidationCheckStatus `json:"status"`  // ok, warning, error
	Message string                `json:"message"` // Human-readable description
}

// ContainerValidationPayload carries the results of post-setup container validation.
type ContainerValidationPayload struct {
	AgentName string            `json:"agent_name"`
	Checks    []ValidationCheck `json:"checks"`
	Summary   string            `json:"summary"` // Overall summary (e.g., "3 ok, 1 warning, 0 errors")
}

// SkillConfig represents a skill to install, with the repository URL and skill name as separate fields.
type SkillConfig struct {
	RepoURL   string `json:"repo_url"`
	SkillName string `json:"skill_name"`
}

// SkillInstallResult represents the installation outcome for a single skill package.
type SkillInstallResult struct {
	Package string `json:"package"`
	Status  string `json:"status"` // installed, failed
	Error   string `json:"error,omitempty"`
}

// SkillStatusPayload carries per-skill installation results from the sidecar.
type SkillStatusPayload struct {
	AgentName string               `json:"agent_name"`
	Skills    []SkillInstallResult `json:"skills"`
	Summary   string               `json:"summary"` // e.g., "2 installed, 1 failed"
}

// McpServerConfig describes a single MCP server for agent tooling.
type McpServerConfig struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"` // "stdio", "http", "sse"
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

// McpServerStatus tracks the status of a single MCP server.
// Status values:
//   - "configured" — sidecar wrote config, pre-warming passed
//   - "running"    — Claude Code successfully started the MCP server (runtime init)
//   - "error"      — sidecar validation or pre-warming failed
//   - "failed"     — MCP server failed at Claude Code runtime
type McpServerStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "configured", "running", "error", "failed"
	Error  string `json:"error,omitempty"`
}

// McpStatusPayload carries MCP server status from the sidecar.
type McpStatusPayload struct {
	AgentName string            `json:"agent_name"`
	Servers   []McpServerStatus `json:"servers"`
	Summary   string            `json:"summary"`
}
