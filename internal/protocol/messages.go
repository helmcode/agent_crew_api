// Package protocol defines shared message types for the AgentCrew JSON protocol.
package protocol

import (
	"encoding/json"
	"time"
)

// MessageType identifies the kind of protocol message.
type MessageType string

const (
	TypeUserMessage    MessageType = "user_message"
	TypeLeaderResponse MessageType = "leader_response"
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

// UserMessagePayload carries a free-form user message.
type UserMessagePayload struct {
	Content string `json:"content"`
}

// LeaderResponsePayload carries the leader's response back to the user.
type LeaderResponsePayload struct {
	Status string `json:"status"` // completed, failed, partial
	Result string `json:"result"`
	Error  string `json:"error,omitempty"`
}

// SystemCommandPayload carries a system-level command.
type SystemCommandPayload struct {
	Command string            `json:"command"` // shutdown, restart, compact_context
	Args    map[string]string `json:"args,omitempty"`
}
