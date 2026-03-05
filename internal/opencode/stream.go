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
// In OpenCode's SSE format, sessionID and messageID may be inside the part object.
type MessagePartPayload struct {
	SessionID string          `json:"sessionID,omitempty"`
	MessageID string          `json:"messageID,omitempty"`
	Part      Part            `json:"part"`
	Delta     json.RawMessage `json:"delta,omitempty"` // Incremental content fragment.
}

// Part represents a single part of an OpenCode message.
// Content fields vary by type and may appear as flat fields on the part object
// (OpenCode SSE format) or inside a structured Content field.
type Part struct {
	Type      string `json:"type"`  // "text", "tool", "file", "reasoning", "snapshot"
	ID        string `json:"id"`
	SessionID string `json:"sessionID,omitempty"`
	MessageID string `json:"messageID,omitempty"`
	State     string `json:"state"` // "pending", "running", "completed", "error"

	// Flat content fields (OpenCode SSE format).
	Text   string          `json:"text,omitempty"`
	Tool   string          `json:"tool,omitempty"`
	Input  json.RawMessage `json:"input,omitempty"`
	Output string          `json:"output,omitempty"`
	Error  string          `json:"error,omitempty"`

	// Structured content (alternative format).
	Content json.RawMessage `json:"content,omitempty"`
}

// TextContent is the content structure for text parts (structured format).
type TextContent struct {
	Text string `json:"text"`
}

// ToolContent is the content structure for tool parts.
type ToolContent struct {
	Tool   string          `json:"tool"`
	Input  json.RawMessage `json:"input"`
	Output string          `json:"output"`
	Error  string          `json:"error,omitempty"`
}

// SessionErrorPayload represents the properties of a session.error event.
// The Error field is json.RawMessage because OpenCode sends it as an object
// ({"name":"APIError","data":{"message":"..."}}) while other sources may send a string.
type SessionErrorPayload struct {
	SessionID string          `json:"sessionID"`
	Error     json.RawMessage `json:"error"`
	Code      string          `json:"code,omitempty"`
}

// sessionErrorObject is the structured error format from OpenCode SSE.
type sessionErrorObject struct {
	Name string `json:"name"`
	Data struct {
		Message    string `json:"message"`
		StatusCode int    `json:"statusCode"`
	} `json:"data"`
}

// SessionIdlePayload represents the properties of a session.idle event.
type SessionIdlePayload struct {
	SessionID string `json:"sessionID"`
}

// maxDataLines is the maximum number of "data:" lines allowed per SSE event.
// This prevents unbounded memory growth from a malicious or buggy server.
const maxDataLines = 1000

// ParseSSEStream reads an SSE stream from r and sends parsed events to the channel.
// Blocks until the stream is closed or an error occurs.
func ParseSSEStream(r io.Reader, ch chan<- SSEEvent) {
	scanner := bufio.NewScanner(r)
	const maxTokenSize = 16 * 1024 * 1024
	scanner.Buffer(make([]byte, 0, 64*1024), maxTokenSize)

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
			if len(dataLines) >= maxDataLines {
				slog.Warn("SSE event exceeded max data lines, discarding", "type", eventType)
				eventType = ""
				dataLines = nil
				continue
			}
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
// sessionID filters events to the active session; empty string disables filtering.
// Returns nil if the event should be skipped (not relevant for the bridge).
func ConvertSSEToProviderEvent(evt SSEEvent, sessionID string) *provider.StreamEvent {
	switch evt.Type {
	case EventMessagePartUpdated:
		return convertMessagePart(evt.Data, sessionID)
	case EventMessageUpdated:
		return convertMessageUpdated(evt.Data, sessionID)
	case EventSessionError:
		return convertSessionError(evt.Data, sessionID)
	case EventSessionIdle:
		return convertSessionIdle(evt.Data, sessionID)
	default:
		return nil
	}
}

func convertMessagePart(data json.RawMessage, filterSessionID string) *provider.StreamEvent {
	var payload MessagePartPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		slog.Debug("failed to parse message.part.updated", "error", err)
		return nil
	}

	// SessionID may be at top level or inside the part object.
	sessionID := payload.SessionID
	if sessionID == "" {
		sessionID = payload.Part.SessionID
	}

	// Filter by session ID.
	if filterSessionID != "" && sessionID != filterSessionID {
		return nil
	}

	switch payload.Part.Type {
	case "text":
		text := resolveText(payload.Part)
		if text == "" {
			return nil
		}
		msgJSON, _ := json.Marshal(map[string]string{"type": "text", "text": text})
		return &provider.StreamEvent{
			Type:      "assistant",
			Message:   string(msgJSON),
			SessionID: sessionID,
		}

	case "tool":
		tc := resolveToolContent(payload.Part)
		if tc.Tool == "" {
			slog.Debug("tool part has no tool name")
			return nil
		}
		return convertToolPart(sessionID, payload.Part.State, tc)

	case "reasoning":
		text := resolveText(payload.Part)
		if text == "" {
			return nil
		}
		msgJSON, _ := json.Marshal(map[string]string{"type": "text", "text": text})
		return &provider.StreamEvent{
			Type:      "reasoning",
			Message:   string(msgJSON),
			SessionID: sessionID,
		}

	default:
		// file, snapshot — skip.
		return nil
	}
}

// resolveText extracts text from a Part, checking the flat Text field first,
// then falling back to the structured Content field.
func resolveText(p Part) string {
	if p.Text != "" {
		return p.Text
	}
	if len(p.Content) > 0 {
		var content TextContent
		if err := json.Unmarshal(p.Content, &content); err == nil {
			return content.Text
		}
	}
	return ""
}

// resolveToolContent extracts tool fields from a Part, checking flat fields first,
// then falling back to the structured Content field.
func resolveToolContent(p Part) ToolContent {
	if p.Tool != "" {
		return ToolContent{
			Tool:   p.Tool,
			Input:  p.Input,
			Output: p.Output,
			Error:  p.Error,
		}
	}
	if len(p.Content) > 0 {
		var tc ToolContent
		if json.Unmarshal(p.Content, &tc) == nil {
			return tc
		}
	}
	return ToolContent{}
}

// convertToolPart maps tool parts using the state field:
//   - state: "running" → tool_use
//   - state: "completed" → tool_result
//   - state: "error" → tool_result with IsError=true
func convertToolPart(sessionID, state string, content ToolContent) *provider.StreamEvent {
	switch state {
	case "running", "pending":
		inputStr := ""
		if len(content.Input) > 0 {
			inputStr = string(content.Input)
		}
		return &provider.StreamEvent{
			Type:      "tool_use",
			Name:      content.Tool,
			Input:     inputStr,
			SessionID: sessionID,
		}

	case "completed":
		return &provider.StreamEvent{
			Type:      "tool_result",
			Name:      content.Tool,
			Result:    content.Output,
			SessionID: sessionID,
		}

	case "error":
		errMsg := content.Error
		if errMsg == "" {
			errMsg = content.Output
		}
		return &provider.StreamEvent{
			Type:      "tool_result",
			Name:      content.Tool,
			IsError:   true,
			Result:    errMsg,
			SessionID: sessionID,
		}

	default:
		// Unknown state, treat as tool_use.
		inputStr := ""
		if len(content.Input) > 0 {
			inputStr = string(content.Input)
		}
		return &provider.StreamEvent{
			Type:      "tool_use",
			Name:      content.Tool,
			Input:     inputStr,
			SessionID: sessionID,
		}
	}
}

func convertSessionError(data json.RawMessage, filterSessionID string) *provider.StreamEvent {
	var payload SessionErrorPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		slog.Debug("failed to parse session.error", "error", err)
		return nil
	}

	if filterSessionID != "" && payload.SessionID != filterSessionID {
		return nil
	}

	var errMsg, errCode string

	// Try structured error object first (OpenCode format:
	// {"name":"APIError","data":{"message":"invalid x-api-key","statusCode":401}}).
	var errObj sessionErrorObject
	if json.Unmarshal(payload.Error, &errObj) == nil && errObj.Name != "" {
		errMsg = errObj.Data.Message
		if errMsg == "" {
			errMsg = errObj.Name
		}
		errCode = errObj.Name
	} else {
		// Fall back to plain string.
		var errStr string
		if json.Unmarshal(payload.Error, &errStr) == nil {
			errMsg = errStr
		} else {
			errMsg = string(payload.Error)
		}
		errCode = payload.Code
	}

	return &provider.StreamEvent{
		Type:      "error",
		IsError:   true,
		Result:    errMsg,
		ErrorCode: errCode,
		SessionID: payload.SessionID,
	}
}

// MessageUpdatedPayload represents the data of a message.updated SSE event.
// This fires when the entire message object changes, e.g. when a message
// completes with an error (info.error is set).
type MessageUpdatedPayload struct {
	Info struct {
		Role      string `json:"role"`
		SessionID string `json:"sessionID"`
		Error     *struct {
			Name string `json:"name"`
			Data struct {
				Message    string `json:"message"`
				StatusCode int    `json:"statusCode"`
			} `json:"data"`
		} `json:"error,omitempty"`
	} `json:"info"`
}

// convertMessageUpdated handles message.updated events. These fire when a
// message's metadata changes, most importantly when an assistant message
// completes with an error (e.g. invalid API key, rate limit, etc.).
func convertMessageUpdated(data json.RawMessage, filterSessionID string) *provider.StreamEvent {
	var payload MessageUpdatedPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		slog.Debug("failed to parse message.updated", "error", err)
		return nil
	}

	// Filter by session ID.
	if filterSessionID != "" && payload.Info.SessionID != filterSessionID {
		return nil
	}

	// Only emit an event if the message has an error.
	if payload.Info.Error == nil {
		return nil
	}

	errMsg := payload.Info.Error.Data.Message
	if errMsg == "" {
		errMsg = payload.Info.Error.Name
	}

	return &provider.StreamEvent{
		Type:      "result",
		IsError:   true,
		Result:    errMsg,
		ErrorCode: payload.Info.Error.Name,
		SessionID: payload.Info.SessionID,
	}
}

// convertSessionIdle maps session.idle to a "result" event. The NATS bridge
// should accumulate preceding assistant text parts into the final result
// message before forwarding this event to the client.
func convertSessionIdle(data json.RawMessage, filterSessionID string) *provider.StreamEvent {
	var payload SessionIdlePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		slog.Debug("failed to parse session.idle", "error", err)
		return nil
	}

	if filterSessionID != "" && payload.SessionID != filterSessionID {
		return nil
	}

	return &provider.StreamEvent{
		Type:      "result",
		SessionID: payload.SessionID,
	}
}

// unwrapSSEEvent handles the OpenCode SSE format where events are sent without
// an `event:` line. Instead, the event type is embedded in the JSON data:
//
//	data: {"type":"session.error","properties":{"sessionID":"ses_xxx",...}}
//
// When evt.Type is empty and the data contains a "type" + "properties" envelope,
// this function extracts the type and replaces Data with the properties object
// so downstream converters work unchanged.
func unwrapSSEEvent(evt SSEEvent) SSEEvent {
	if evt.Type != "" || len(evt.Data) == 0 {
		return evt
	}

	var envelope struct {
		Type       string          `json:"type"`
		Properties json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(evt.Data, &envelope); err != nil || envelope.Type == "" {
		return evt
	}

	evt.Type = envelope.Type
	if len(envelope.Properties) > 0 {
		evt.Data = envelope.Properties
	}
	return evt
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
