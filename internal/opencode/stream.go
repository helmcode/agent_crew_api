// Package opencode implements the OpenCode server client for the AgentManager interface.
// It communicates with `opencode serve` via HTTP REST + SSE.
package opencode

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/helmcode/agent-crew/internal/provider"
)

// SSE event types emitted by `opencode serve`.
const (
	// Session lifecycle events.
	EventSessionCreated   = "session.created"
	EventSessionUpdated   = "session.updated"
	EventSessionDeleted   = "session.deleted"
	EventSessionCompacted = "session.compacted"
	EventSessionError     = "session.error"
	EventSessionIdle      = "session.idle"
	EventSessionStatus    = "session.status"

	// Message events.
	EventMessageUpdated     = "message.updated"
	EventMessageRemoved     = "message.removed"
	EventMessagePartUpdated = "message.part.updated"
	EventMessagePartRemoved = "message.part.removed"

	// Tool events.
	EventToolExecuteBefore = "tool.execute.before"
	EventToolExecuteAfter  = "tool.execute.after"

	// Permission events.
	EventPermissionAsked   = "permission.asked"
	EventPermissionReplied = "permission.replied"

	// Question events.
	EventQuestionAsked = "question.asked"

	// Server events.
	EventServerConnected = "server.connected"
)

// SSEEvent represents a single parsed SSE event from the OpenCode stream.
type SSEEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// MessagePartPayload represents the properties of a message.part.updated event.
type MessagePartPayload struct {
	SessionID string `json:"sessionID"`
	MessageID string `json:"messageID"`
	Part      Part   `json:"part"`
}

// Part represents a single part of an OpenCode message.
type Part struct {
	Type    string          `json:"type"` // "text", "tool", "file", "reasoning", "snapshot"
	ID      string          `json:"id"`
	Content json.RawMessage `json:"content"`
	State   string          `json:"state"` // "pending", "running", "completed", "error"
}

// TextContent is the content structure for text parts.
type TextContent struct {
	Text string `json:"text"`
}

// ToolContent is the content structure for tool parts.
type ToolContent struct {
	Tool   string          `json:"tool"`
	Input  json.RawMessage `json:"input"`
	Output string          `json:"output"`
}

// ToolExecutePayload represents the properties of tool.execute.before/after events.
type ToolExecutePayload struct {
	SessionID string `json:"sessionID"`
	MessageID string `json:"messageID"`
	Tool      string `json:"tool"`
	Input     json.RawMessage `json:"input"`
	Output    string          `json:"output,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// SessionErrorPayload represents the properties of a session.error event.
type SessionErrorPayload struct {
	SessionID string `json:"sessionID"`
	Error     string `json:"error"`
	Code      string `json:"code,omitempty"`
}

// SessionIdlePayload represents the properties of a session.idle event.
type SessionIdlePayload struct {
	SessionID string `json:"sessionID"`
}

// MessageUpdatedPayload represents the properties of a message.updated event.
type MessageUpdatedPayload struct {
	SessionID string  `json:"sessionID"`
	MessageID string  `json:"messageID"`
	Message   Message `json:"message"`
}

// Message represents an OpenCode message.
type Message struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
	Role      string `json:"role"` // "user", "assistant"
	Parts     []Part `json:"parts"`
}

// SessionStatusPayload represents a session.status event.
type SessionStatusPayload struct {
	SessionID string `json:"sessionID"`
	Status    string `json:"status"` // "idle", "busy", "error"
}

// ParseSSEStream reads an SSE stream from r and sends parsed events to the channel.
// Blocks until the stream is closed or an error occurs.
func ParseSSEStream(r io.Reader, ch chan<- SSEEvent) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	var eventType string
	var dataLines []string

	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			// Empty line = end of event.
			if eventType != "" || len(dataLines) > 0 {
				data := strings.Join(dataLines, "\n")
				evt := SSEEvent{
					Type: eventType,
				}
				if data != "" {
					evt.Data = json.RawMessage(data)
				}

				select {
				case ch <- evt:
				default:
					slog.Warn("opencode SSE event channel full, dropping event", "type", eventType)
				}

				eventType = ""
				dataLines = nil
			}
			continue
		}

		if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		} else if strings.HasPrefix(line, ":") {
			// Comment line, ignore.
			continue
		}
	}

	// Flush any remaining event.
	if eventType != "" || len(dataLines) > 0 {
		data := strings.Join(dataLines, "\n")
		evt := SSEEvent{
			Type: eventType,
		}
		if data != "" {
			evt.Data = json.RawMessage(data)
		}
		select {
		case ch <- evt:
		default:
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Error("opencode SSE stream read error", "error", err)
	}
}

// ConvertSSEToProviderEvent converts an OpenCode SSE event to a provider.StreamEvent.
// Returns nil if the event should be skipped (not relevant for the bridge).
func ConvertSSEToProviderEvent(evt SSEEvent) *provider.StreamEvent {
	switch evt.Type {
	case EventMessagePartUpdated:
		return convertMessagePart(evt.Data)
	case EventToolExecuteBefore:
		return convertToolBefore(evt.Data)
	case EventToolExecuteAfter:
		return convertToolAfter(evt.Data)
	case EventSessionError:
		return convertSessionError(evt.Data)
	case EventSessionIdle:
		return convertSessionIdle(evt.Data)
	default:
		return nil
	}
}

func convertMessagePart(data json.RawMessage) *provider.StreamEvent {
	var payload MessagePartPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		slog.Debug("failed to parse message.part.updated", "error", err)
		return nil
	}

	switch payload.Part.Type {
	case "text":
		var content TextContent
		if err := json.Unmarshal(payload.Part.Content, &content); err != nil {
			slog.Debug("failed to parse text content", "error", err)
			return nil
		}
		// Only emit when the part is completed or has content.
		if content.Text == "" {
			return nil
		}
		msgJSON, _ := json.Marshal(map[string]string{"type": "text", "text": content.Text})
		return &provider.StreamEvent{
			Type:      "assistant",
			Message:   string(msgJSON),
			SessionID: payload.SessionID,
		}

	case "tool":
		var content ToolContent
		if err := json.Unmarshal(payload.Part.Content, &content); err != nil {
			slog.Debug("failed to parse tool content", "error", err)
			return nil
		}
		// Tool parts with output are results.
		if content.Output != "" {
			return &provider.StreamEvent{
				Type:      "tool_result",
				Name:      content.Tool,
				Result:    content.Output,
				SessionID: payload.SessionID,
			}
		}
		// Tool parts without output are in-progress.
		inputStr := ""
		if len(content.Input) > 0 {
			inputStr = string(content.Input)
		}
		return &provider.StreamEvent{
			Type:      "tool_use",
			Name:      content.Tool,
			Input:     inputStr,
			SessionID: payload.SessionID,
		}

	default:
		// reasoning, file, snapshot — skip.
		return nil
	}
}

func convertToolBefore(data json.RawMessage) *provider.StreamEvent {
	var payload ToolExecutePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		slog.Debug("failed to parse tool.execute.before", "error", err)
		return nil
	}

	inputStr := ""
	if len(payload.Input) > 0 {
		inputStr = string(payload.Input)
	}

	return &provider.StreamEvent{
		Type:      "tool_use",
		Name:      payload.Tool,
		Input:     inputStr,
		SessionID: payload.SessionID,
	}
}

func convertToolAfter(data json.RawMessage) *provider.StreamEvent {
	var payload ToolExecutePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		slog.Debug("failed to parse tool.execute.after", "error", err)
		return nil
	}

	pe := &provider.StreamEvent{
		Type:      "tool_result",
		Name:      payload.Tool,
		Result:    payload.Output,
		SessionID: payload.SessionID,
	}

	if payload.Error != "" {
		pe.IsError = true
		pe.Result = payload.Error
	}

	return pe
}

func convertSessionError(data json.RawMessage) *provider.StreamEvent {
	var payload SessionErrorPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		slog.Debug("failed to parse session.error", "error", err)
		return nil
	}

	return &provider.StreamEvent{
		Type:      "error",
		IsError:   true,
		Result:    payload.Error,
		ErrorCode: payload.Code,
		SessionID: payload.SessionID,
	}
}

func convertSessionIdle(data json.RawMessage) *provider.StreamEvent {
	var payload SessionIdlePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		slog.Debug("failed to parse session.idle", "error", err)
		return nil
	}

	return &provider.StreamEvent{
		Type:      "result",
		SessionID: payload.SessionID,
	}
}

// FormatSSEEventType returns a human-readable description of an SSE event type.
func FormatSSEEventType(eventType string) string {
	descriptions := map[string]string{
		EventSessionCreated:     "session created",
		EventSessionUpdated:     "session updated",
		EventSessionIdle:        "session idle",
		EventSessionError:       "session error",
		EventMessagePartUpdated: "message part updated",
		EventToolExecuteBefore:  "tool execution started",
		EventToolExecuteAfter:   "tool execution completed",
		EventPermissionAsked:    "permission requested",
		EventQuestionAsked:      "question asked",
		EventServerConnected:    "server connected",
	}
	if desc, ok := descriptions[eventType]; ok {
		return desc
	}
	return fmt.Sprintf("unknown event: %s", eventType)
}
