package nats

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/helmcode/agent-crew/internal/claude"
	"github.com/helmcode/agent-crew/internal/permissions"
	"github.com/helmcode/agent-crew/internal/protocol"
)

// publishedMsg captures a message published to a NATS subject.
type publishedMsg struct {
	Subject string
	Msg     *protocol.Message
}

// fakePublisher implements the publisher interface for testing.
type fakePublisher struct {
	mu       sync.Mutex
	messages []publishedMsg
}

func (f *fakePublisher) Publish(subject string, msg *protocol.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, publishedMsg{Subject: subject, Msg: msg})
	return nil
}

func (f *fakePublisher) Subscribe(_ string, _ func(*protocol.Message)) error {
	return nil
}

func (f *fakePublisher) getMessages() []publishedMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]publishedMsg, len(f.messages))
	copy(out, f.messages)
	return out
}

// --- publishLeaderResponse tests ---

func TestPublishLeaderResponse(t *testing.T) {
	pub := &fakePublisher{}
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "leader",
			TeamName:  "testteam",
			Role:      "leader",
		},
		client: pub,
	}

	bridge.publishLeaderResponse("ref-123", "completed", "task done", "")

	msgs := pub.getMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 published message, got %d", len(msgs))
	}

	// Message.To must be "user".
	if msgs[0].Msg.To != "user" {
		t.Errorf("Message.To: got %q, want 'user'", msgs[0].Msg.To)
	}

	// NATS subject must be the team leader channel.
	if msgs[0].Subject != "team.testteam.leader" {
		t.Errorf("Subject: got %q, want 'team.testteam.leader'", msgs[0].Subject)
	}

	// From must be the agent name.
	if msgs[0].Msg.From != "leader" {
		t.Errorf("Message.From: got %q, want 'leader'", msgs[0].Msg.From)
	}

	// Message type must be leader_response.
	if msgs[0].Msg.Type != protocol.TypeLeaderResponse {
		t.Errorf("Type: got %q, want %q", msgs[0].Msg.Type, protocol.TypeLeaderResponse)
	}

	// RefMessageID must be set.
	if msgs[0].Msg.RefMessageID != "ref-123" {
		t.Errorf("RefMessageID: got %q, want 'ref-123'", msgs[0].Msg.RefMessageID)
	}

	// Verify the payload.
	var payload protocol.LeaderResponsePayload
	if err := json.Unmarshal(msgs[0].Msg.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Status != "completed" {
		t.Errorf("payload.Status: got %q, want 'completed'", payload.Status)
	}
	if payload.Result != "task done" {
		t.Errorf("payload.Result: got %q, want 'task done'", payload.Result)
	}
}

func TestPublishLeaderResponse_ErrorPayload(t *testing.T) {
	pub := &fakePublisher{}
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "leader",
			TeamName:  "myteam",
			Role:      "leader",
		},
		client: pub,
	}

	bridge.publishLeaderResponse("", "failed", "", "something went wrong")

	msgs := pub.getMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 published message, got %d", len(msgs))
	}

	if msgs[0].Msg.To != "user" {
		t.Errorf("Message.To: got %q, want 'user'", msgs[0].Msg.To)
	}

	var payload protocol.LeaderResponsePayload
	if err := json.Unmarshal(msgs[0].Msg.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Status != "failed" {
		t.Errorf("payload.Status: got %q, want 'failed'", payload.Status)
	}
	if payload.Error != "something went wrong" {
		t.Errorf("payload.Error: got %q, want 'something went wrong'", payload.Error)
	}
}

// --- processEvent tests ---

func TestProcessEvent_ResultPublishesLeaderResponse(t *testing.T) {
	pub := &fakePublisher{}
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "leader",
			TeamName:  "evtteam",
			Role:      "leader",
		},
		client: pub,
	}

	resultText := "Here is my completed work."
	msgContent, _ := json.Marshal(map[string]string{"type": "text", "text": resultText})
	event := claude.StreamEvent{
		Type:    "result",
		Message: msgContent,
	}

	var currentResult string
	bridge.processEvent(&event, &currentResult)

	msgs := pub.getMessages()

	// Should produce exactly 1 leader_response.
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	if msgs[0].Msg.Type != protocol.TypeLeaderResponse {
		t.Errorf("Type: got %q, want %q", msgs[0].Msg.Type, protocol.TypeLeaderResponse)
	}

	// Message.To must be "user".
	if msgs[0].Msg.To != "user" {
		t.Errorf("Message.To: got %q, want 'user'", msgs[0].Msg.To)
	}

	// NATS subject must be team leader channel.
	if msgs[0].Subject != "team.evtteam.leader" {
		t.Errorf("Subject: got %q, want 'team.evtteam.leader'", msgs[0].Subject)
	}

	// Verify the result content.
	var payload protocol.LeaderResponsePayload
	if err := json.Unmarshal(msgs[0].Msg.Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Status != "completed" {
		t.Errorf("status: got %q, want 'completed'", payload.Status)
	}
	if payload.Result != resultText {
		t.Errorf("result: got %q, want %q", payload.Result, resultText)
	}
}

func TestProcessEvent_ErrorResultPublishesFailedResponse(t *testing.T) {
	pub := &fakePublisher{}
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "leader",
			TeamName:  "errteam",
			Role:      "leader",
		},
		client: pub,
	}

	event := claude.StreamEvent{
		Type:      "result",
		IsError:   true,
		ErrorCode: "billing_error",
		Result:    "insufficient credits",
	}

	var currentResult string
	bridge.processEvent(&event, &currentResult)

	msgs := pub.getMessages()

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	if msgs[0].Msg.Type != protocol.TypeLeaderResponse {
		t.Errorf("Type: got %q, want %q", msgs[0].Msg.Type, protocol.TypeLeaderResponse)
	}

	// Message.To must be "user".
	if msgs[0].Msg.To != "user" {
		t.Errorf("Message.To: got %q, want 'user'", msgs[0].Msg.To)
	}

	var payload protocol.LeaderResponsePayload
	if err := json.Unmarshal(msgs[0].Msg.Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Status != "failed" {
		t.Errorf("status: got %q, want 'failed'", payload.Status)
	}
	if payload.Error == "" {
		t.Error("expected non-empty error message")
	}
}

// --- publishActivityEvent tests ---

func TestPublishActivityEvent(t *testing.T) {
	pub := &fakePublisher{}
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "leader",
			TeamName:  "actteam",
			Role:      "leader",
		},
		client: pub,
	}

	event := &claude.StreamEvent{
		Type: "tool_use",
		Name: "Bash",
		Input: json.RawMessage(`{"command":"ls -la"}`),
	}

	bridge.publishActivityEvent(event, "Bash: ls -la")

	msgs := pub.getMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 published message, got %d", len(msgs))
	}

	if msgs[0].Subject != "team.actteam.activity" {
		t.Errorf("Subject: got %q, want 'team.actteam.activity'", msgs[0].Subject)
	}

	if msgs[0].Msg.Type != protocol.TypeActivityEvent {
		t.Errorf("Type: got %q, want %q", msgs[0].Msg.Type, protocol.TypeActivityEvent)
	}

	if msgs[0].Msg.From != "leader" {
		t.Errorf("From: got %q, want 'leader'", msgs[0].Msg.From)
	}

	if msgs[0].Msg.To != "system" {
		t.Errorf("To: got %q, want 'system'", msgs[0].Msg.To)
	}

	var payload protocol.ActivityEventPayload
	if err := json.Unmarshal(msgs[0].Msg.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.EventType != "tool_use" {
		t.Errorf("payload.EventType: got %q, want 'tool_use'", payload.EventType)
	}
	if payload.AgentName != "leader" {
		t.Errorf("payload.AgentName: got %q, want 'leader'", payload.AgentName)
	}
	if payload.ToolName != "Bash" {
		t.Errorf("payload.ToolName: got %q, want 'Bash'", payload.ToolName)
	}
	if payload.Action != "Bash: ls -la" {
		t.Errorf("payload.Action: got %q, want 'Bash: ls -la'", payload.Action)
	}
}

func TestProcessEvent_ToolUsePublishesActivityEvent(t *testing.T) {
	pub := &fakePublisher{}
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "leader",
			TeamName:  "toolteam",
			Role:      "leader",
		},
		client: pub,
	}

	event := claude.StreamEvent{
		Type:  "tool_use",
		Name:  "Read",
		Input: json.RawMessage(`{"file_path":"/workspace/main.go"}`),
	}

	var currentResult string
	bridge.processEvent(&event, &currentResult)

	msgs := pub.getMessages()
	// tool_use should produce exactly 1 activity event (no leader response).
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	if msgs[0].Msg.Type != protocol.TypeActivityEvent {
		t.Errorf("Type: got %q, want %q", msgs[0].Msg.Type, protocol.TypeActivityEvent)
	}
	if msgs[0].Subject != "team.toolteam.activity" {
		t.Errorf("Subject: got %q, want 'team.toolteam.activity'", msgs[0].Subject)
	}
}

func TestProcessEvent_AssistantPublishesActivityEvent(t *testing.T) {
	pub := &fakePublisher{}
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "leader",
			TeamName:  "asstteam",
			Role:      "leader",
		},
		client: pub,
	}

	msgContent, _ := json.Marshal(map[string]string{"type": "text", "text": "Thinking about the problem..."})
	event := claude.StreamEvent{
		Type:    "assistant",
		Message: msgContent,
	}

	var currentResult string
	bridge.processEvent(&event, &currentResult)

	msgs := pub.getMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	if msgs[0].Msg.Type != protocol.TypeActivityEvent {
		t.Errorf("Type: got %q, want %q", msgs[0].Msg.Type, protocol.TypeActivityEvent)
	}

	var payload protocol.ActivityEventPayload
	if err := json.Unmarshal(msgs[0].Msg.Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.EventType != "assistant" {
		t.Errorf("EventType: got %q, want 'assistant'", payload.EventType)
	}
	if payload.Action != "assistant message" {
		t.Errorf("Action: got %q, want 'assistant message'", payload.Action)
	}
}

func TestProcessEvent_ResultFromResultField(t *testing.T) {
	pub := &fakePublisher{}
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "leader",
			TeamName:  "resultfield",
			Role:      "leader",
		},
		client: pub,
	}

	// When Message field doesn't produce text, the Result field is used.
	event := claude.StreamEvent{
		Type:   "result",
		Result: "Fallback result text",
	}

	var currentResult string
	bridge.processEvent(&event, &currentResult)

	msgs := pub.getMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	if msgs[0].Msg.Type != protocol.TypeLeaderResponse {
		t.Errorf("Type: got %q, want %q", msgs[0].Msg.Type, protocol.TypeLeaderResponse)
	}

	var payload protocol.LeaderResponsePayload
	if err := json.Unmarshal(msgs[0].Msg.Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Result != "Fallback result text" {
		t.Errorf("result: got %q, want 'Fallback result text'", payload.Result)
	}
}

// --- processEvent: tool_result and error event types ---

func TestProcessEvent_ToolResultPublishesActivityEvent(t *testing.T) {
	pub := &fakePublisher{}
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "leader",
			TeamName:  "toolresteam",
			Role:      "leader",
		},
		client: pub,
	}

	event := claude.StreamEvent{
		Type:   "tool_result",
		Result: "file contents here",
	}

	var currentResult string
	bridge.processEvent(&event, &currentResult)

	msgs := pub.getMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Msg.Type != protocol.TypeActivityEvent {
		t.Errorf("Type: got %q, want %q", msgs[0].Msg.Type, protocol.TypeActivityEvent)
	}
	if msgs[0].Subject != "team.toolresteam.activity" {
		t.Errorf("Subject: got %q, want 'team.toolresteam.activity'", msgs[0].Subject)
	}

	var payload protocol.ActivityEventPayload
	if err := json.Unmarshal(msgs[0].Msg.Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.EventType != "tool_result" {
		t.Errorf("EventType: got %q, want 'tool_result'", payload.EventType)
	}
	if payload.Action != "tool result" {
		t.Errorf("Action: got %q, want 'tool result'", payload.Action)
	}
}

func TestProcessEvent_ErrorPublishesActivityEvent(t *testing.T) {
	pub := &fakePublisher{}
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "leader",
			TeamName:  "errteam2",
			Role:      "leader",
		},
		client: pub,
	}

	event := claude.StreamEvent{
		Type: "error",
	}

	var currentResult string
	bridge.processEvent(&event, &currentResult)

	msgs := pub.getMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Msg.Type != protocol.TypeActivityEvent {
		t.Errorf("Type: got %q, want %q", msgs[0].Msg.Type, protocol.TypeActivityEvent)
	}

	var payload protocol.ActivityEventPayload
	if err := json.Unmarshal(msgs[0].Msg.Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.EventType != "error" {
		t.Errorf("EventType: got %q, want 'error'", payload.EventType)
	}
	if payload.Action != "error" {
		t.Errorf("Action: got %q, want 'error'", payload.Action)
	}
}

// --- processEvent: tool_use action format ---

func TestProcessEvent_ToolUseActionFormat_WithCommand(t *testing.T) {
	pub := &fakePublisher{}
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "leader",
			TeamName:  "actfmt",
			Role:      "leader",
		},
		client: pub,
	}

	event := claude.StreamEvent{
		Type:  "tool_use",
		Name:  "Bash",
		Input: json.RawMessage(`{"command":"ls -la /workspace"}`),
	}

	var currentResult string
	bridge.processEvent(&event, &currentResult)

	msgs := pub.getMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	var payload protocol.ActivityEventPayload
	if err := json.Unmarshal(msgs[0].Msg.Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Action != "Bash: ls -la /workspace" {
		t.Errorf("Action: got %q, want 'Bash: ls -la /workspace'", payload.Action)
	}
}

func TestProcessEvent_ToolUseActionFormat_WithoutCommand(t *testing.T) {
	pub := &fakePublisher{}
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "leader",
			TeamName:  "actfmt2",
			Role:      "leader",
		},
		client: pub,
	}

	event := claude.StreamEvent{
		Type:  "tool_use",
		Name:  "Read",
		Input: json.RawMessage(`{"file_path":"/workspace/main.go"}`),
	}

	var currentResult string
	bridge.processEvent(&event, &currentResult)

	msgs := pub.getMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	var payload protocol.ActivityEventPayload
	if err := json.Unmarshal(msgs[0].Msg.Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// No command field → action should be just the tool name.
	if payload.Action != "Read" {
		t.Errorf("Action: got %q, want 'Read'", payload.Action)
	}
}

// --- Permission gate integration tests ---

func TestProcessEvent_ToolUseDeniedByGate(t *testing.T) {
	pub := &fakePublisher{}
	gate := permissions.NewGate(permissions.PermissionConfig{
		AllowedTools: []string{"Read", "Write"},
		// Bash is NOT in the allowed list.
	})
	// A real Manager (status="stopped") so SendInput returns error instead of panicking.
	mgr := claude.NewManager(claude.ProcessConfig{})
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "leader",
			TeamName:  "gateteam",
			Role:      "leader",
			Gate:      gate,
		},
		client:  pub,
		manager: mgr,
	}

	event := claude.StreamEvent{
		Type:  "tool_use",
		Name:  "Bash",
		Input: json.RawMessage(`{"command":"rm -rf /"}`),
	}

	var currentResult string
	bridge.processEvent(&event, &currentResult)

	msgs := pub.getMessages()
	// tool_use denied: should publish 1 activity event but NO leader response.
	// The activity event is published BEFORE the gate check.
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message (activity event only), got %d", len(msgs))
	}
	if msgs[0].Msg.Type != protocol.TypeActivityEvent {
		t.Errorf("Type: got %q, want %q", msgs[0].Msg.Type, protocol.TypeActivityEvent)
	}
}

func TestProcessEvent_ToolUseAllowedByGate(t *testing.T) {
	pub := &fakePublisher{}
	gate := permissions.NewGate(permissions.PermissionConfig{
		AllowedTools: []string{"Read", "Bash"},
	})
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "leader",
			TeamName:  "allowteam",
			Role:      "leader",
			Gate:      gate,
		},
		client: pub,
	}

	event := claude.StreamEvent{
		Type:  "tool_use",
		Name:  "Read",
		Input: json.RawMessage(`{"file_path":"/workspace/main.go"}`),
	}

	var currentResult string
	bridge.processEvent(&event, &currentResult)

	msgs := pub.getMessages()
	// Allowed tool: should publish 1 activity event.
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Msg.Type != protocol.TypeActivityEvent {
		t.Errorf("Type: got %q, want %q", msgs[0].Msg.Type, protocol.TypeActivityEvent)
	}
}

func TestProcessEvent_ToolUseDeniedByDeniedCommand(t *testing.T) {
	pub := &fakePublisher{}
	gate := permissions.NewGate(permissions.PermissionConfig{
		AllowedTools:   []string{"Bash"},
		DeniedCommands: []string{"rm *"},
	})
	mgr := claude.NewManager(claude.ProcessConfig{})
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "leader",
			TeamName:  "denyteam",
			Role:      "leader",
			Gate:      gate,
		},
		client:  pub,
		manager: mgr,
	}

	event := claude.StreamEvent{
		Type:  "tool_use",
		Name:  "Bash",
		Input: json.RawMessage(`{"command":"rm -rf /workspace"}`),
	}

	var currentResult string
	bridge.processEvent(&event, &currentResult)

	msgs := pub.getMessages()
	// Denied command: activity event is published before gate check.
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message (activity event only), got %d", len(msgs))
	}
	if msgs[0].Msg.Type != protocol.TypeActivityEvent {
		t.Errorf("Type: got %q, want %q", msgs[0].Msg.Type, protocol.TypeActivityEvent)
	}
}

func TestProcessEvent_FilesystemScopeEnforced(t *testing.T) {
	pub := &fakePublisher{}
	gate := permissions.NewGate(permissions.PermissionConfig{
		AllowedTools:    []string{"Read", "Write"},
		FilesystemScope: "/workspace",
	})
	mgr := claude.NewManager(claude.ProcessConfig{})
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "leader",
			TeamName:  "scopeteam",
			Role:      "leader",
			Gate:      gate,
		},
		client:  pub,
		manager: mgr,
	}

	// Path OUTSIDE scope — should be denied.
	event := claude.StreamEvent{
		Type:  "tool_use",
		Name:  "Read",
		Input: json.RawMessage(`{"file_path":"/etc/passwd"}`),
	}

	var currentResult string
	bridge.processEvent(&event, &currentResult)

	msgs := pub.getMessages()
	// Activity event published before gate check, but no further action.
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message (activity event only), got %d", len(msgs))
	}
	if msgs[0].Msg.Type != protocol.TypeActivityEvent {
		t.Errorf("Type: got %q, want %q", msgs[0].Msg.Type, protocol.TypeActivityEvent)
	}
}

func TestProcessEvent_FilesystemScopeAllowed(t *testing.T) {
	pub := &fakePublisher{}
	gate := permissions.NewGate(permissions.PermissionConfig{
		AllowedTools:    []string{"Read"},
		FilesystemScope: "/workspace",
	})
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "leader",
			TeamName:  "scopeokteam",
			Role:      "leader",
			Gate:      gate,
		},
		client: pub,
	}

	// Path INSIDE scope — should be allowed.
	event := claude.StreamEvent{
		Type:  "tool_use",
		Name:  "Read",
		Input: json.RawMessage(`{"file_path":"/workspace/src/main.go"}`),
	}

	var currentResult string
	bridge.processEvent(&event, &currentResult)

	msgs := pub.getMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	// Should be allowed — activity event published.
	if msgs[0].Msg.Type != protocol.TypeActivityEvent {
		t.Errorf("Type: got %q, want %q", msgs[0].Msg.Type, protocol.TypeActivityEvent)
	}
}

func TestProcessEvent_NilGateAllowsAll(t *testing.T) {
	pub := &fakePublisher{}
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "leader",
			TeamName:  "nogateteam",
			Role:      "leader",
			Gate:      nil, // No gate configured.
		},
		client: pub,
	}

	event := claude.StreamEvent{
		Type:  "tool_use",
		Name:  "Bash",
		Input: json.RawMessage(`{"command":"echo hello"}`),
	}

	var currentResult string
	bridge.processEvent(&event, &currentResult)

	msgs := pub.getMessages()
	// With no gate, only the activity event is published (no denial).
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Msg.Type != protocol.TypeActivityEvent {
		t.Errorf("Type: got %q, want %q", msgs[0].Msg.Type, protocol.TypeActivityEvent)
	}
}

// --- processEvent: result clears currentResult ---

func TestProcessEvent_ResultClearsCurrentResult(t *testing.T) {
	pub := &fakePublisher{}
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "leader",
			TeamName:  "clearteam",
			Role:      "leader",
		},
		client: pub,
	}

	msgContent, _ := json.Marshal(map[string]string{"type": "text", "text": "Final answer"})
	event := claude.StreamEvent{
		Type:    "result",
		Message: msgContent,
	}

	currentResult := "leftover from previous"
	bridge.processEvent(&event, &currentResult)

	// After processing a result, currentResult should be reset to empty.
	if currentResult != "" {
		t.Errorf("currentResult should be empty after result event, got %q", currentResult)
	}
}
