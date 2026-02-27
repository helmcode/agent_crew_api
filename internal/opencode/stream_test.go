package opencode

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseSSEStream_SingleEvent(t *testing.T) {
	input := "event: session.created\ndata: {\"sessionID\":\"abc\"}\n\n"
	ch := make(chan SSEEvent, 10)

	ParseSSEStream(strings.NewReader(input), ch)
	close(ch)

	events := drainSSE(ch)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "session.created" {
		t.Errorf("Type: got %q, want 'session.created'", events[0].Type)
	}
	if string(events[0].Data) != `{"sessionID":"abc"}` {
		t.Errorf("Data: got %q", string(events[0].Data))
	}
}

func TestParseSSEStream_MultipleEvents(t *testing.T) {
	input := "event: session.idle\ndata: {\"sessionID\":\"s1\"}\n\nevent: message.part.updated\ndata: {\"sessionID\":\"s1\",\"part\":{\"type\":\"text\"}}\n\n"
	ch := make(chan SSEEvent, 10)

	ParseSSEStream(strings.NewReader(input), ch)
	close(ch)

	events := drainSSE(ch)
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Type != "session.idle" {
		t.Errorf("event[0] Type: got %q", events[0].Type)
	}
	if events[1].Type != "message.part.updated" {
		t.Errorf("event[1] Type: got %q", events[1].Type)
	}
}

func TestParseSSEStream_IgnoresComments(t *testing.T) {
	input := ": this is a comment\nevent: server.connected\ndata: {}\n\n"
	ch := make(chan SSEEvent, 10)

	ParseSSEStream(strings.NewReader(input), ch)
	close(ch)

	events := drainSSE(ch)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "server.connected" {
		t.Errorf("Type: got %q", events[0].Type)
	}
}

func TestParseSSEStream_MultiLineData(t *testing.T) {
	input := "event: message.updated\ndata: {\"line1\":\ndata: \"continued\"}\n\n"
	ch := make(chan SSEEvent, 10)

	ParseSSEStream(strings.NewReader(input), ch)
	close(ch)

	events := drainSSE(ch)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	expected := "{\"line1\":\n\"continued\"}"
	if string(events[0].Data) != expected {
		t.Errorf("Data: got %q, want %q", string(events[0].Data), expected)
	}
}

func TestParseSSEStream_EmptyStream(t *testing.T) {
	ch := make(chan SSEEvent, 10)
	ParseSSEStream(strings.NewReader(""), ch)
	close(ch)

	events := drainSSE(ch)
	if len(events) != 0 {
		t.Errorf("expected 0 events from empty stream, got %d", len(events))
	}
}

func TestParseSSEStream_NoTrailingNewline(t *testing.T) {
	input := "event: session.idle\ndata: {\"sessionID\":\"s1\"}"
	ch := make(chan SSEEvent, 10)

	ParseSSEStream(strings.NewReader(input), ch)
	close(ch)

	events := drainSSE(ch)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
}

// --- ConvertSSEToProviderEvent tests ---

func TestConvert_MessagePartText(t *testing.T) {
	payload := MessagePartPayload{
		SessionID: "sess-1",
		MessageID: "msg-1",
		Part: Part{
			Type:    "text",
			Content: json.RawMessage(`{"text":"Hello world"}`),
		},
	}
	data, _ := json.Marshal(payload)

	pe := ConvertSSEToProviderEvent(SSEEvent{Type: EventMessagePartUpdated, Data: data}, "sess-1")

	if pe == nil {
		t.Fatal("expected non-nil event")
	}
	if pe.Type != "assistant" {
		t.Errorf("Type: got %q, want 'assistant'", pe.Type)
	}
	if pe.SessionID != "sess-1" {
		t.Errorf("SessionID: got %q", pe.SessionID)
	}
	var msg map[string]string
	if err := json.Unmarshal([]byte(pe.Message), &msg); err != nil {
		t.Fatalf("failed to parse Message: %v", err)
	}
	if msg["text"] != "Hello world" {
		t.Errorf("message text: got %q", msg["text"])
	}
}

func TestConvert_MessagePartTextEmpty(t *testing.T) {
	payload := MessagePartPayload{
		SessionID: "sess-1",
		Part:      Part{Type: "text", Content: json.RawMessage(`{"text":""}`)},
	}
	data, _ := json.Marshal(payload)

	pe := ConvertSSEToProviderEvent(SSEEvent{Type: EventMessagePartUpdated, Data: data}, "")
	if pe != nil {
		t.Errorf("expected nil for empty text, got %+v", pe)
	}
}

func TestConvert_ToolRunning(t *testing.T) {
	payload := MessagePartPayload{
		SessionID: "sess-1",
		Part: Part{
			Type:    "tool",
			State:   "running",
			Content: json.RawMessage(`{"tool":"Bash","input":{"command":"ls"}}`),
		},
	}
	data, _ := json.Marshal(payload)

	pe := ConvertSSEToProviderEvent(SSEEvent{Type: EventMessagePartUpdated, Data: data}, "sess-1")

	if pe == nil {
		t.Fatal("expected non-nil event")
	}
	if pe.Type != "tool_use" {
		t.Errorf("Type: got %q, want 'tool_use'", pe.Type)
	}
	if pe.Name != "Bash" {
		t.Errorf("Name: got %q, want 'Bash'", pe.Name)
	}
	if pe.Input != `{"command":"ls"}` {
		t.Errorf("Input: got %q", pe.Input)
	}
}

func TestConvert_ToolCompleted(t *testing.T) {
	payload := MessagePartPayload{
		SessionID: "sess-1",
		Part: Part{
			Type:    "tool",
			State:   "completed",
			Content: json.RawMessage(`{"tool":"Read","output":"file contents"}`),
		},
	}
	data, _ := json.Marshal(payload)

	pe := ConvertSSEToProviderEvent(SSEEvent{Type: EventMessagePartUpdated, Data: data}, "sess-1")

	if pe == nil {
		t.Fatal("expected non-nil event")
	}
	if pe.Type != "tool_result" {
		t.Errorf("Type: got %q, want 'tool_result'", pe.Type)
	}
	if pe.Name != "Read" {
		t.Errorf("Name: got %q", pe.Name)
	}
	if pe.Result != "file contents" {
		t.Errorf("Result: got %q", pe.Result)
	}
	if pe.IsError {
		t.Error("IsError should be false")
	}
}

func TestConvert_ToolError(t *testing.T) {
	payload := MessagePartPayload{
		SessionID: "sess-1",
		Part: Part{
			Type:    "tool",
			State:   "error",
			Content: json.RawMessage(`{"tool":"Bash","error":"command not found"}`),
		},
	}
	data, _ := json.Marshal(payload)

	pe := ConvertSSEToProviderEvent(SSEEvent{Type: EventMessagePartUpdated, Data: data}, "sess-1")

	if pe == nil {
		t.Fatal("expected non-nil event")
	}
	if pe.Type != "tool_result" {
		t.Errorf("Type: got %q, want 'tool_result'", pe.Type)
	}
	if !pe.IsError {
		t.Error("IsError should be true")
	}
	if pe.Result != "command not found" {
		t.Errorf("Result: got %q, want 'command not found'", pe.Result)
	}
}

func TestConvert_ToolPending(t *testing.T) {
	payload := MessagePartPayload{
		SessionID: "sess-1",
		Part: Part{
			Type:    "tool",
			State:   "pending",
			Content: json.RawMessage(`{"tool":"Write","input":{"file_path":"/tmp/out"}}`),
		},
	}
	data, _ := json.Marshal(payload)

	pe := ConvertSSEToProviderEvent(SSEEvent{Type: EventMessagePartUpdated, Data: data}, "sess-1")

	if pe == nil {
		t.Fatal("expected non-nil event")
	}
	if pe.Type != "tool_use" {
		t.Errorf("Type: got %q, want 'tool_use'", pe.Type)
	}
}

func TestConvert_Reasoning(t *testing.T) {
	payload := MessagePartPayload{
		SessionID: "sess-1",
		Part: Part{
			Type:    "reasoning",
			Content: json.RawMessage(`{"text":"Let me think..."}`),
		},
	}
	data, _ := json.Marshal(payload)

	pe := ConvertSSEToProviderEvent(SSEEvent{Type: EventMessagePartUpdated, Data: data}, "sess-1")

	if pe == nil {
		t.Fatal("expected non-nil event for reasoning")
	}
	if pe.Type != "assistant" {
		t.Errorf("Type: got %q, want 'assistant'", pe.Type)
	}
}

func TestConvert_ReasoningEmpty(t *testing.T) {
	payload := MessagePartPayload{
		SessionID: "sess-1",
		Part:      Part{Type: "reasoning", Content: json.RawMessage(`{"text":""}`)},
	}
	data, _ := json.Marshal(payload)

	pe := ConvertSSEToProviderEvent(SSEEvent{Type: EventMessagePartUpdated, Data: data}, "")
	if pe != nil {
		t.Errorf("expected nil for empty reasoning, got %+v", pe)
	}
}

func TestConvert_SessionError(t *testing.T) {
	payload := SessionErrorPayload{
		SessionID: "sess-1",
		Error:     "rate limit exceeded",
		Code:      "rate_limit",
	}
	data, _ := json.Marshal(payload)

	pe := ConvertSSEToProviderEvent(SSEEvent{Type: EventSessionError, Data: data}, "sess-1")

	if pe == nil {
		t.Fatal("expected non-nil event")
	}
	if pe.Type != "error" {
		t.Errorf("Type: got %q, want 'error'", pe.Type)
	}
	if !pe.IsError {
		t.Error("IsError should be true")
	}
	if pe.Result != "rate limit exceeded" {
		t.Errorf("Result: got %q", pe.Result)
	}
	if pe.ErrorCode != "rate_limit" {
		t.Errorf("ErrorCode: got %q, want 'rate_limit'", pe.ErrorCode)
	}
}

func TestConvert_SessionIdle(t *testing.T) {
	payload := SessionIdlePayload{SessionID: "sess-1"}
	data, _ := json.Marshal(payload)

	pe := ConvertSSEToProviderEvent(SSEEvent{Type: EventSessionIdle, Data: data}, "sess-1")

	if pe == nil {
		t.Fatal("expected non-nil event")
	}
	if pe.Type != "result" {
		t.Errorf("Type: got %q, want 'result'", pe.Type)
	}
	if pe.SessionID != "sess-1" {
		t.Errorf("SessionID: got %q", pe.SessionID)
	}
}

func TestConvert_UnknownType(t *testing.T) {
	pe := ConvertSSEToProviderEvent(SSEEvent{Type: "unknown.event.type", Data: json.RawMessage(`{}`)}, "")
	if pe != nil {
		t.Errorf("expected nil for unknown event type, got %+v", pe)
	}
}

func TestConvert_FilePartSkipped(t *testing.T) {
	payload := MessagePartPayload{
		SessionID: "sess-1",
		Part:      Part{Type: "file", Content: json.RawMessage(`{}`)},
	}
	data, _ := json.Marshal(payload)

	pe := ConvertSSEToProviderEvent(SSEEvent{Type: EventMessagePartUpdated, Data: data}, "")
	if pe != nil {
		t.Errorf("expected nil for file part, got %+v", pe)
	}
}

// --- Session filtering tests ---

func TestConvert_FiltersOtherSession(t *testing.T) {
	payload := MessagePartPayload{
		SessionID: "other-session",
		Part:      Part{Type: "text", Content: json.RawMessage(`{"text":"Hello"}`)},
	}
	data, _ := json.Marshal(payload)

	pe := ConvertSSEToProviderEvent(SSEEvent{Type: EventMessagePartUpdated, Data: data}, "my-session")
	if pe != nil {
		t.Errorf("expected nil for event from different session, got %+v", pe)
	}
}

func TestConvert_EmptyFilterPassesAll(t *testing.T) {
	payload := MessagePartPayload{
		SessionID: "any-session",
		Part:      Part{Type: "text", Content: json.RawMessage(`{"text":"Hello"}`)},
	}
	data, _ := json.Marshal(payload)

	pe := ConvertSSEToProviderEvent(SSEEvent{Type: EventMessagePartUpdated, Data: data}, "")
	if pe == nil {
		t.Fatal("expected non-nil when session filter is empty")
	}
}

func TestConvert_SessionIdleFiltered(t *testing.T) {
	payload := SessionIdlePayload{SessionID: "other-session"}
	data, _ := json.Marshal(payload)

	pe := ConvertSSEToProviderEvent(SSEEvent{Type: EventSessionIdle, Data: data}, "my-session")
	if pe != nil {
		t.Errorf("expected nil for session.idle from different session, got %+v", pe)
	}
}

func TestConvert_SessionErrorFiltered(t *testing.T) {
	payload := SessionErrorPayload{SessionID: "other-session", Error: "fail"}
	data, _ := json.Marshal(payload)

	pe := ConvertSSEToProviderEvent(SSEEvent{Type: EventSessionError, Data: data}, "my-session")
	if pe != nil {
		t.Errorf("expected nil for session.error from different session, got %+v", pe)
	}
}

func TestFormatSSEEventType(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{EventSessionIdle, "session idle"},
		{EventToolExecuteBefore, "tool execution started"},
		{EventToolExecuteAfter, "tool execution completed"},
		{EventServerConnected, "server connected"},
		{"unknown.type", "unknown event: unknown.type"},
	}

	for _, tt := range tests {
		result := FormatSSEEventType(tt.input)
		if result != tt.expected {
			t.Errorf("FormatSSEEventType(%q): got %q, want %q", tt.input, result, tt.expected)
		}
	}
}

// drainSSE reads all events from a channel into a slice.
func drainSSE(ch <-chan SSEEvent) []SSEEvent {
	var events []SSEEvent
	for e := range ch {
		events = append(events, e)
	}
	return events
}
