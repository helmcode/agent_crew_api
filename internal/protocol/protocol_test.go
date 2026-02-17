package protocol

import (
	"encoding/json"
	"testing"
)

func TestNewMessage(t *testing.T) {
	payload := TaskAssignmentPayload{
		Instruction:    "Deploy the service",
		ExpectedOutput: "Service running",
	}

	msg, err := NewMessage("leader", "worker-1", TypeTaskAssignment, payload)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}

	if msg.MessageID == "" {
		t.Error("expected non-empty message ID")
	}
	if msg.From != "leader" {
		t.Errorf("expected from 'leader', got %q", msg.From)
	}
	if msg.To != "worker-1" {
		t.Errorf("expected to 'worker-1', got %q", msg.To)
	}
	if msg.Type != TypeTaskAssignment {
		t.Errorf("expected type %q, got %q", TypeTaskAssignment, msg.Type)
	}
	if msg.Timestamp.IsZero() {
		t.Error("expected non-zero timestamp")
	}
}

func TestParsePayload_TaskAssignment(t *testing.T) {
	original := TaskAssignmentPayload{
		Instruction:     "Run terraform plan",
		ExpectedOutput:  "No errors",
		DeadlineSeconds: 300,
	}

	msg, err := NewMessage("leader", "devops", TypeTaskAssignment, original)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}

	parsed, err := ParsePayload[TaskAssignmentPayload](msg)
	if err != nil {
		t.Fatalf("ParsePayload: %v", err)
	}

	if parsed.Instruction != original.Instruction {
		t.Errorf("instruction: got %q, want %q", parsed.Instruction, original.Instruction)
	}
	if parsed.DeadlineSeconds != 300 {
		t.Errorf("deadline: got %d, want 300", parsed.DeadlineSeconds)
	}
}

func TestParsePayload_TaskResult(t *testing.T) {
	original := TaskResultPayload{
		Status:    "completed",
		Result:    "Deployment successful",
		Artifacts: []string{"terraform.tfstate"},
	}

	msg, err := NewMessage("devops", "leader", TypeTaskResult, original)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}

	parsed, err := ParsePayload[TaskResultPayload](msg)
	if err != nil {
		t.Fatalf("ParsePayload: %v", err)
	}

	if parsed.Status != "completed" {
		t.Errorf("status: got %q, want 'completed'", parsed.Status)
	}
	if len(parsed.Artifacts) != 1 {
		t.Errorf("artifacts: got %d, want 1", len(parsed.Artifacts))
	}
}

func TestParsePayload_StatusUpdate(t *testing.T) {
	original := StatusUpdatePayload{
		Agent:           "worker-1",
		Status:          "working",
		CurrentTask:     "Deploying service",
		ContextUsagePct: 45,
		TasksCompleted:  3,
	}

	msg, err := NewMessage("worker-1", "leader", TypeStatusUpdate, original)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}

	parsed, err := ParsePayload[StatusUpdatePayload](msg)
	if err != nil {
		t.Fatalf("ParsePayload: %v", err)
	}

	if parsed.ContextUsagePct != 45 {
		t.Errorf("context usage: got %d, want 45", parsed.ContextUsagePct)
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

	_, err := ParsePayload[TaskAssignmentPayload](msg)
	if err == nil {
		t.Fatal("expected error for invalid JSON payload")
	}
}

func TestMessage_JSONRoundTrip(t *testing.T) {
	msg, err := NewMessage("a", "b", TypeQuestion, QuestionPayload{
		Question: "Which approach?",
		Options:  []string{"A", "B"},
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
	tests := []struct {
		name     string
		fn       func() (string, error)
		expected string
	}{
		{"leader channel", func() (string, error) { return TeamLeaderChannel("myteam") }, "team.myteam.leader"},
		{"agent channel", func() (string, error) { return AgentChannel("myteam", "worker-1") }, "team.myteam.worker-1"},
		{"broadcast channel", func() (string, error) { return BroadcastChannel("myteam") }, "team.myteam.broadcast"},
		{"status channel", func() (string, error) { return StatusChannel("myteam") }, "team.myteam.status"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.fn()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestChannels_InvalidNames(t *testing.T) {
	tests := []struct {
		name      string
		teamName  string
		agentName string
	}{
		{"dot in team name", "my.team", ""},
		{"wildcard in team name", "my*team", ""},
		{"gt in team name", "my>team", ""},
		{"space in team name", "my team", ""},
		{"empty team name", "", ""},
		{"dot in agent name", "myteam", "agent.1"},
		{"wildcard in agent name", "myteam", "agent*"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := TeamLeaderChannel(tt.teamName)
			if tt.teamName == "" || containsNATSSpecial(tt.teamName) {
				if err == nil {
					t.Error("expected error for invalid team name")
				}
			}

			if tt.agentName != "" {
				_, err = AgentChannel(tt.teamName, tt.agentName)
				if err == nil {
					t.Error("expected error for invalid name")
				}
			}
		})
	}
}

func containsNATSSpecial(s string) bool {
	for _, c := range s {
		switch c {
		case '.', '*', '>', ' ', '\t', '\n', '\r':
			return true
		}
	}
	return false
}

func TestMessageTypes(t *testing.T) {
	types := []MessageType{
		TypeTaskAssignment,
		TypeTaskResult,
		TypeQuestion,
		TypeStatusUpdate,
		TypeContextShare,
		TypeUserMessage,
		TypeSystemCommand,
	}

	expected := []string{
		"task_assignment",
		"task_result",
		"question",
		"status_update",
		"context_share",
		"user_message",
		"system_command",
	}

	for i, mt := range types {
		if string(mt) != expected[i] {
			t.Errorf("type %d: got %q, want %q", i, string(mt), expected[i])
		}
	}
}
