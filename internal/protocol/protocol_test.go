package protocol

import (
	"encoding/json"
	"testing"
)

func TestNewMessage(t *testing.T) {
	payload := LeaderResponsePayload{
		Status: "completed",
		Result: "Deployment successful",
	}

	msg, err := NewMessage("leader", "user", TypeLeaderResponse, payload)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}

	if msg.MessageID == "" {
		t.Error("expected non-empty message ID")
	}
	if msg.From != "leader" {
		t.Errorf("expected from 'leader', got %q", msg.From)
	}
	if msg.To != "user" {
		t.Errorf("expected to 'user', got %q", msg.To)
	}
	if msg.Type != TypeLeaderResponse {
		t.Errorf("expected type %q, got %q", TypeLeaderResponse, msg.Type)
	}
	if msg.Timestamp.IsZero() {
		t.Error("expected non-zero timestamp")
	}
}

func TestParsePayload_LeaderResponse(t *testing.T) {
	original := LeaderResponsePayload{
		Status: "completed",
		Result: "All tasks finished",
	}

	msg, err := NewMessage("leader", "user", TypeLeaderResponse, original)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}

	parsed, err := ParsePayload[LeaderResponsePayload](msg)
	if err != nil {
		t.Fatalf("ParsePayload: %v", err)
	}

	if parsed.Status != "completed" {
		t.Errorf("status: got %q, want 'completed'", parsed.Status)
	}
	if parsed.Result != "All tasks finished" {
		t.Errorf("result: got %q, want 'All tasks finished'", parsed.Result)
	}
}

func TestParsePayload_LeaderResponseWithError(t *testing.T) {
	original := LeaderResponsePayload{
		Status: "failed",
		Result: "",
		Error:  "context limit exceeded",
	}

	msg, err := NewMessage("leader", "user", TypeLeaderResponse, original)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}

	parsed, err := ParsePayload[LeaderResponsePayload](msg)
	if err != nil {
		t.Fatalf("ParsePayload: %v", err)
	}

	if parsed.Status != "failed" {
		t.Errorf("status: got %q, want 'failed'", parsed.Status)
	}
	if parsed.Error != "context limit exceeded" {
		t.Errorf("error: got %q, want 'context limit exceeded'", parsed.Error)
	}
}

func TestParsePayload_SystemCommand(t *testing.T) {
	original := SystemCommandPayload{
		Command: "shutdown",
		Args:    map[string]string{"reason": "maintenance"},
	}

	msg, err := NewMessage("leader", "worker-1", TypeSystemCommand, original)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}

	parsed, err := ParsePayload[SystemCommandPayload](msg)
	if err != nil {
		t.Fatalf("ParsePayload: %v", err)
	}

	if parsed.Command != "shutdown" {
		t.Errorf("command: got %q, want 'shutdown'", parsed.Command)
	}
	if parsed.Args["reason"] != "maintenance" {
		t.Errorf("args[reason]: got %q, want 'maintenance'", parsed.Args["reason"])
	}
}

func TestParsePayload_InvalidPayload(t *testing.T) {
	msg := &Message{
		Payload: json.RawMessage(`{"invalid json`),
	}

	_, err := ParsePayload[LeaderResponsePayload](msg)
	if err == nil {
		t.Fatal("expected error for invalid JSON payload")
	}
}

func TestMessage_JSONRoundTrip(t *testing.T) {
	msg, err := NewMessage("leader", "user", TypeLeaderResponse, LeaderResponsePayload{
		Status: "completed",
		Result: "Task done",
	})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	msg.Context = &MessageContext{
		ThreadID:    "thread-1",
		RelevantIDs: []string{"msg-1", "msg-2"},
	}
	msg.RefMessageID = "ref-123"

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded Message
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if decoded.MessageID != msg.MessageID {
		t.Errorf("message_id mismatch")
	}
	if decoded.Context.ThreadID != "thread-1" {
		t.Errorf("context thread_id mismatch")
	}
	if decoded.RefMessageID != "ref-123" {
		t.Errorf("ref_message_id mismatch")
	}
}

func TestChannels(t *testing.T) {
	got, err := TeamLeaderChannel("myteam")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "team.myteam.leader" {
		t.Errorf("got %q, want %q", got, "team.myteam.leader")
	}

	got, err = TeamActivityChannel("myteam")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "team.myteam.activity" {
		t.Errorf("got %q, want %q", got, "team.myteam.activity")
	}
}

func TestChannels_InvalidNames(t *testing.T) {
	tests := []struct {
		name     string
		teamName string
	}{
		{"dot in team name", "my.team"},
		{"wildcard in team name", "my*team"},
		{"gt in team name", "my>team"},
		{"space in team name", "my team"},
		{"empty team name", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := TeamLeaderChannel(tt.teamName)
			if err == nil {
				t.Error("expected error for invalid team name (leader)")
			}
			_, err = TeamActivityChannel(tt.teamName)
			if err == nil {
				t.Error("expected error for invalid team name (activity)")
			}
		})
	}
}

func TestMessageTypes(t *testing.T) {
	types := []MessageType{
		TypeUserMessage,
		TypeLeaderResponse,
		TypeSystemCommand,
		TypeActivityEvent,
		TypeContainerValidation,
	}

	expected := []string{
		"user_message",
		"leader_response",
		"system_command",
		"activity_event",
		"container_validation",
	}

	for i, mt := range types {
		if string(mt) != expected[i] {
			t.Errorf("type %d: got %q, want %q", i, string(mt), expected[i])
		}
	}
}

func TestParsePayload_ContainerValidation(t *testing.T) {
	original := ContainerValidationPayload{
		AgentName: "leader",
		Checks: []ValidationCheck{
			{Name: "claude_md", Status: ValidationOK, Message: "CLAUDE.md exists"},
			{Name: "agents_dir", Status: ValidationError, Message: "agents directory missing"},
			{Name: "skills_symlink", Status: ValidationWarning, Message: "skills symlink broken"},
		},
		Summary: "1 ok, 1 warning(s), 1 error(s)",
	}

	msg, err := NewMessage("leader", "system", TypeContainerValidation, original)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}

	if msg.Type != TypeContainerValidation {
		t.Errorf("type: got %q, want %q", msg.Type, TypeContainerValidation)
	}

	parsed, err := ParsePayload[ContainerValidationPayload](msg)
	if err != nil {
		t.Fatalf("ParsePayload: %v", err)
	}

	if parsed.AgentName != "leader" {
		t.Errorf("agent_name: got %q, want 'leader'", parsed.AgentName)
	}
	if len(parsed.Checks) != 3 {
		t.Fatalf("checks: got %d, want 3", len(parsed.Checks))
	}
	if parsed.Checks[0].Status != ValidationOK {
		t.Errorf("check[0].status: got %q, want %q", parsed.Checks[0].Status, ValidationOK)
	}
	if parsed.Checks[1].Status != ValidationError {
		t.Errorf("check[1].status: got %q, want %q", parsed.Checks[1].Status, ValidationError)
	}
	if parsed.Checks[2].Status != ValidationWarning {
		t.Errorf("check[2].status: got %q, want %q", parsed.Checks[2].Status, ValidationWarning)
	}
	if parsed.Summary != "1 ok, 1 warning(s), 1 error(s)" {
		t.Errorf("summary: got %q", parsed.Summary)
	}
}

func TestValidationCheckStatus_Values(t *testing.T) {
	if string(ValidationOK) != "ok" {
		t.Errorf("ValidationOK: got %q, want 'ok'", ValidationOK)
	}
	if string(ValidationWarning) != "warning" {
		t.Errorf("ValidationWarning: got %q, want 'warning'", ValidationWarning)
	}
	if string(ValidationError) != "error" {
		t.Errorf("ValidationError: got %q, want 'error'", ValidationError)
	}
}
