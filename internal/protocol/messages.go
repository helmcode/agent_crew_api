// Package protocol defines shared message types for the AgentCrew JSON protocol.
package protocol

import (
	"encoding/json"
	"time"
)

// MessageType identifies the kind of protocol message.
type MessageType string

const (
	TypeTaskAssignment MessageType = "task_assignment"
	TypeTaskResult     MessageType = "task_result"
	TypeQuestion       MessageType = "question"
	TypeStatusUpdate   MessageType = "status_update"
	TypeContextShare   MessageType = "context_share"
	TypeUserMessage    MessageType = "user_message"
	TypeSystemCommand  MessageType = "system_command"
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

// TaskAssignmentPayload carries a task to be executed by an agent.
type TaskAssignmentPayload struct {
	Instruction     string `json:"instruction"`
	ExpectedOutput  string `json:"expected_output,omitempty"`
	DeadlineSeconds int    `json:"deadline_seconds,omitempty"`
}

// TaskResultPayload carries the outcome of an executed task.
type TaskResultPayload struct {
	Status    string   `json:"status"` // completed, failed, partial
	Result    string   `json:"result"`
	Artifacts []string `json:"artifacts,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// StatusUpdatePayload reports an agent's current state.
type StatusUpdatePayload struct {
	Agent            string    `json:"agent"`
	Status           string    `json:"status"` // idle, working, error, restarting, context_compacting
	CurrentTask      string    `json:"current_task,omitempty"`
	UptimeSeconds    int64     `json:"uptime_seconds"`
	ContextUsagePct  int       `json:"context_usage_pct"`
	TasksCompleted   int       `json:"tasks_completed"`
	TasksFailed      int       `json:"tasks_failed"`
	LastActivity     time.Time `json:"last_activity"`
}

// QuestionPayload carries a question from one agent to another.
type QuestionPayload struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

// UserMessagePayload carries a free-form user message.
type UserMessagePayload struct {
	Content string `json:"content"`
}

// SystemCommandPayload carries a system-level command.
type SystemCommandPayload struct {
	Command string            `json:"command"` // shutdown, restart, compact_context
	Args    map[string]string `json:"args,omitempty"`
}
