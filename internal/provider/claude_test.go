package provider

import (
	"encoding/json"
	"testing"

	"github.com/helmcode/agent-crew/internal/claude"
)

func TestToClaudeStreamEvent_BasicFields(t *testing.T) {
	pe := &StreamEvent{
		Type:      "assistant",
		Message:   `"hello"`,
		Name:      "Bash",
		Input:     `{"command":"ls"}`,
		IsError:   false,
		Result:    "ok",
		ErrorCode: "",
		SessionID: "sess-123",
	}

	ce := ToClaudeStreamEvent(pe)

	if ce.Type != pe.Type {
		t.Errorf("Type: got %q, want %q", ce.Type, pe.Type)
	}
	if string(ce.Message) != pe.Message {
		t.Errorf("Message: got %q, want %q", string(ce.Message), pe.Message)
	}
	if ce.Name != pe.Name {
		t.Errorf("Name: got %q, want %q", ce.Name, pe.Name)
	}
	if string(ce.Input) != pe.Input {
		t.Errorf("Input: got %q, want %q", string(ce.Input), pe.Input)
	}
	if ce.IsError != pe.IsError {
		t.Errorf("IsError: got %v, want %v", ce.IsError, pe.IsError)
	}
	if ce.Result != pe.Result {
		t.Errorf("Result: got %q, want %q", ce.Result, pe.Result)
	}
	if ce.SessionID != pe.SessionID {
		t.Errorf("SessionID: got %q, want %q", ce.SessionID, pe.SessionID)
	}
}

func TestToClaudeStreamEvent_ErrorCode(t *testing.T) {
	pe := &StreamEvent{
		Type:      "error",
		IsError:   true,
		Result:    "billing limit exceeded",
		ErrorCode: "billing_error",
	}

	ce := ToClaudeStreamEvent(pe)

	if ce.ErrorCode != "billing_error" {
		t.Errorf("ErrorCode: got %q, want 'billing_error'", ce.ErrorCode)
	}

	// Verify FriendlyError now works correctly.
	friendly := ce.FriendlyError()
	expected := "Your API key has insufficient credits. Please add credits or update your key in Settings."
	if friendly != expected {
		t.Errorf("FriendlyError: got %q, want %q", friendly, expected)
	}
}

func TestToClaudeStreamEvent_AuthError(t *testing.T) {
	pe := &StreamEvent{
		Type:      "error",
		IsError:   true,
		ErrorCode: "authentication_error",
	}

	ce := ToClaudeStreamEvent(pe)

	friendly := ce.FriendlyError()
	expected := "API key is invalid or expired. Please update it in Settings."
	if friendly != expected {
		t.Errorf("FriendlyError: got %q, want %q", friendly, expected)
	}
}

func TestToClaudeStreamEvent_EmptyMessageAndInput(t *testing.T) {
	pe := &StreamEvent{
		Type: "result",
	}

	ce := ToClaudeStreamEvent(pe)

	if ce.Message != nil {
		t.Errorf("Message should be nil for empty string, got %v", ce.Message)
	}
	if ce.Input != nil {
		t.Errorf("Input should be nil for empty string, got %v", ce.Input)
	}
}

func TestConvertEvents_ChannelForwarding(t *testing.T) {
	// Create a claude.StreamEvent channel to simulate the inner manager.
	claudeEvents := make(chan claude.StreamEvent, 3)

	// Send test events.
	claudeEvents <- claude.StreamEvent{
		Type:      "assistant",
		Message:   json.RawMessage(`"first message"`),
		SessionID: "sess-1",
	}
	claudeEvents <- claude.StreamEvent{
		Type:      "error",
		IsError:   true,
		Result:    "auth failed",
		ErrorCode: "authentication_error",
	}
	claudeEvents <- claude.StreamEvent{
		Type:    "tool_use",
		Name:    "Read",
		Input:   json.RawMessage(`{"file_path":"/tmp/test"}`),
		Result:  "file contents",
	}
	close(claudeEvents)

	// Convert via a manual loop (same logic as convertEvents).
	providerEvents := make(chan StreamEvent, 3)
	for ce := range claudeEvents {
		pe := StreamEvent{
			Type:      ce.Type,
			Name:      ce.Name,
			IsError:   ce.IsError,
			Result:    ce.Result,
			ErrorCode: ce.ErrorCode,
			SessionID: ce.SessionID,
		}
		if len(ce.Message) > 0 {
			pe.Message = string(ce.Message)
		}
		if len(ce.Input) > 0 {
			pe.Input = string(ce.Input)
		}
		providerEvents <- pe
	}
	close(providerEvents)

	// Verify events.
	events := make([]StreamEvent, 0, 3)
	for e := range providerEvents {
		events = append(events, e)
	}

	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	// Event 1: assistant message.
	if events[0].Type != "assistant" {
		t.Errorf("event[0] Type: got %q, want 'assistant'", events[0].Type)
	}
	if events[0].Message != `"first message"` {
		t.Errorf("event[0] Message: got %q, want '\"first message\"'", events[0].Message)
	}
	if events[0].SessionID != "sess-1" {
		t.Errorf("event[0] SessionID: got %q, want 'sess-1'", events[0].SessionID)
	}

	// Event 2: error with ErrorCode.
	if events[1].Type != "error" {
		t.Errorf("event[1] Type: got %q, want 'error'", events[1].Type)
	}
	if !events[1].IsError {
		t.Error("event[1] IsError: expected true")
	}
	if events[1].ErrorCode != "authentication_error" {
		t.Errorf("event[1] ErrorCode: got %q, want 'authentication_error'", events[1].ErrorCode)
	}

	// Event 3: tool_use.
	if events[2].Type != "tool_use" {
		t.Errorf("event[2] Type: got %q, want 'tool_use'", events[2].Type)
	}
	if events[2].Name != "Read" {
		t.Errorf("event[2] Name: got %q, want 'Read'", events[2].Name)
	}
	if events[2].Input != `{"file_path":"/tmp/test"}` {
		t.Errorf("event[2] Input: got %q", events[2].Input)
	}
}

func TestToClaudeStreamEvent_Roundtrip(t *testing.T) {
	original := claude.StreamEvent{
		Type:      "error",
		Message:   json.RawMessage(`"something went wrong"`),
		Name:      "Bash",
		Input:     json.RawMessage(`{"command":"fail"}`),
		IsError:   true,
		Result:    "command failed",
		ErrorCode: "billing_error",
		SessionID: "sess-42",
	}

	// Convert claude → provider.
	pe := StreamEvent{
		Type:      original.Type,
		Name:      original.Name,
		IsError:   original.IsError,
		Result:    original.Result,
		ErrorCode: original.ErrorCode,
		SessionID: original.SessionID,
	}
	if len(original.Message) > 0 {
		pe.Message = string(original.Message)
	}
	if len(original.Input) > 0 {
		pe.Input = string(original.Input)
	}

	// Convert provider → claude.
	roundtripped := ToClaudeStreamEvent(&pe)

	if roundtripped.Type != original.Type {
		t.Errorf("Type: got %q, want %q", roundtripped.Type, original.Type)
	}
	if string(roundtripped.Message) != string(original.Message) {
		t.Errorf("Message: got %q, want %q", string(roundtripped.Message), string(original.Message))
	}
	if roundtripped.Name != original.Name {
		t.Errorf("Name: got %q, want %q", roundtripped.Name, original.Name)
	}
	if string(roundtripped.Input) != string(original.Input) {
		t.Errorf("Input: got %q, want %q", string(roundtripped.Input), string(original.Input))
	}
	if roundtripped.IsError != original.IsError {
		t.Errorf("IsError: got %v, want %v", roundtripped.IsError, original.IsError)
	}
	if roundtripped.Result != original.Result {
		t.Errorf("Result: got %q, want %q", roundtripped.Result, original.Result)
	}
	if roundtripped.ErrorCode != original.ErrorCode {
		t.Errorf("ErrorCode: got %q, want %q", roundtripped.ErrorCode, original.ErrorCode)
	}
	if roundtripped.SessionID != original.SessionID {
		t.Errorf("SessionID: got %q, want %q", roundtripped.SessionID, original.SessionID)
	}

	// Verify FriendlyError works after roundtrip.
	friendly := roundtripped.FriendlyError()
	expected := "Your API key has insufficient credits. Please add credits or update your key in Settings."
	if friendly != expected {
		t.Errorf("FriendlyError after roundtrip: got %q, want %q", friendly, expected)
	}
}
