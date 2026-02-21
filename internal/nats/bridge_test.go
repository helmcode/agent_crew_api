package nats

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/helmcode/agent-crew/internal/claude"
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

// --- parseDelegations tests ---

func TestParseDelegations_NoDelegations(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty string", ""},
		{"plain text", "Just some regular output from Claude."},
		{"partial tag", "[TASK:agent] no closing tag"},
		{"mismatched tags", "[TASK:agent] hello [/TASK:wrong]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDelegations(tt.input)
			if len(got) != 0 {
				t.Errorf("parseDelegations(%q): got %d delegations, want 0", tt.input, len(got))
			}
		})
	}
}

func TestParseDelegations_SingleDelegation(t *testing.T) {
	input := "Some preamble text.\n[TASK:worker-1] Please implement the login page [/TASK]\nSome follow-up."
	got := parseDelegations(input)

	if len(got) != 1 {
		t.Fatalf("got %d delegations, want 1", len(got))
	}
	if got[0].TargetAgent != "worker-1" {
		t.Errorf("TargetAgent: got %q, want 'worker-1'", got[0].TargetAgent)
	}
	if got[0].Instruction != "Please implement the login page" {
		t.Errorf("Instruction: got %q, want 'Please implement the login page'", got[0].Instruction)
	}
}

func TestParseDelegations_MultipleDelegations(t *testing.T) {
	input := `I'll delegate these tasks:
[TASK:frontend-dev] Build the UI components for the dashboard [/TASK]
[TASK:backend-dev] Create the REST API endpoints [/TASK]
[TASK:devops] Set up the CI/CD pipeline [/TASK]`

	got := parseDelegations(input)
	if len(got) != 3 {
		t.Fatalf("got %d delegations, want 3", len(got))
	}

	expected := []struct {
		agent       string
		instruction string
	}{
		{"frontend-dev", "Build the UI components for the dashboard"},
		{"backend-dev", "Create the REST API endpoints"},
		{"devops", "Set up the CI/CD pipeline"},
	}

	for i, e := range expected {
		if got[i].TargetAgent != e.agent {
			t.Errorf("[%d] TargetAgent: got %q, want %q", i, got[i].TargetAgent, e.agent)
		}
		if got[i].Instruction != e.instruction {
			t.Errorf("[%d] Instruction: got %q, want %q", i, got[i].Instruction, e.instruction)
		}
	}
}

func TestParseDelegations_MultilineInstruction(t *testing.T) {
	input := `[TASK:worker-1]
Please do the following:
1. Read the config file
2. Update the database schema
3. Run the migrations
[/TASK]`

	got := parseDelegations(input)
	if len(got) != 1 {
		t.Fatalf("got %d delegations, want 1", len(got))
	}
	if got[0].TargetAgent != "worker-1" {
		t.Errorf("TargetAgent: got %q, want 'worker-1'", got[0].TargetAgent)
	}
	// Instruction should contain all lines, trimmed.
	if got[0].Instruction == "" {
		t.Error("Instruction should not be empty")
	}
}

func TestParseDelegations_AgentNameValidation(t *testing.T) {
	// Agent names with hyphens and underscores should work.
	input := "[TASK:my_agent-01] do stuff [/TASK]"
	got := parseDelegations(input)
	if len(got) != 1 {
		t.Fatalf("got %d delegations, want 1", len(got))
	}
	if got[0].TargetAgent != "my_agent-01" {
		t.Errorf("TargetAgent: got %q, want 'my_agent-01'", got[0].TargetAgent)
	}
}

func TestParseDelegations_EmptyInstruction(t *testing.T) {
	// Empty instruction should be skipped.
	input := "[TASK:worker-1]   [/TASK]"
	got := parseDelegations(input)
	if len(got) != 0 {
		t.Errorf("got %d delegations, want 0 (empty instruction)", len(got))
	}
}

// --- publishTaskResult tests ---

func TestPublishTaskResult_SetsAgentNameNotSubject(t *testing.T) {
	pub := &fakePublisher{}
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "worker-1",
			TeamName:  "testteam",
			Role:      "worker",
		},
		client: pub,
	}

	bridge.publishTaskResult("leader", "ref-123", "completed", "task done", "")

	msgs := pub.getMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 published message, got %d", len(msgs))
	}

	// Verify Message.To is the agent name, not a NATS subject.
	if msgs[0].Msg.To != "leader" {
		t.Errorf("Message.To: got %q, want 'leader'", msgs[0].Msg.To)
	}

	// Verify the NATS subject is correctly built.
	if msgs[0].Subject != "team.testteam.leader" {
		t.Errorf("Subject: got %q, want 'team.testteam.leader'", msgs[0].Subject)
	}

	// Verify From is set correctly.
	if msgs[0].Msg.From != "worker-1" {
		t.Errorf("Message.From: got %q, want 'worker-1'", msgs[0].Msg.From)
	}

	// Verify RefMessageID is set.
	if msgs[0].Msg.RefMessageID != "ref-123" {
		t.Errorf("RefMessageID: got %q, want 'ref-123'", msgs[0].Msg.RefMessageID)
	}

	// Verify the payload.
	var payload protocol.TaskResultPayload
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

func TestPublishTaskResult_ErrorPayload(t *testing.T) {
	pub := &fakePublisher{}
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "worker-2",
			TeamName:  "myteam",
			Role:      "worker",
		},
		client: pub,
	}

	bridge.publishTaskResult("leader", "", "failed", "", "something went wrong")

	msgs := pub.getMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 published message, got %d", len(msgs))
	}

	if msgs[0].Msg.To != "leader" {
		t.Errorf("Message.To: got %q, want 'leader'", msgs[0].Msg.To)
	}

	var payload protocol.TaskResultPayload
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

func TestPublishTaskResult_SubjectBuiltFromAgentName(t *testing.T) {
	// Verify that different agent names produce the correct NATS subjects.
	tests := []struct {
		to              string
		expectedSubject string
	}{
		{"leader", "team.alpha.leader"},
		{"worker-1", "team.alpha.worker-1"},
		{"frontend-dev", "team.alpha.frontend-dev"},
	}

	for _, tt := range tests {
		pub := &fakePublisher{}
		bridge := &Bridge{
			config: BridgeConfig{
				AgentName: "sender",
				TeamName:  "alpha",
				Role:      "leader",
			},
			client: pub,
		}

		bridge.publishTaskResult(tt.to, "", "completed", "done", "")

		msgs := pub.getMessages()
		if len(msgs) != 1 {
			t.Fatalf("to=%q: expected 1 message, got %d", tt.to, len(msgs))
		}
		if msgs[0].Subject != tt.expectedSubject {
			t.Errorf("to=%q: Subject got %q, want %q", tt.to, msgs[0].Subject, tt.expectedSubject)
		}
		if msgs[0].Msg.To != tt.to {
			t.Errorf("to=%q: Message.To got %q, want %q", tt.to, msgs[0].Msg.To, tt.to)
		}
	}
}

// --- publishStatus tests ---

func TestPublishStatus(t *testing.T) {
	pub := &fakePublisher{}
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "worker-1",
			TeamName:  "statusteam",
			Role:      "worker",
		},
		client: pub,
	}

	bridge.publishStatus("idle", "")

	msgs := pub.getMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 published message, got %d", len(msgs))
	}

	if msgs[0].Subject != "team.statusteam.status" {
		t.Errorf("Subject: got %q, want 'team.statusteam.status'", msgs[0].Subject)
	}
	if msgs[0].Msg.Type != protocol.TypeStatusUpdate {
		t.Errorf("Type: got %q, want %q", msgs[0].Msg.Type, protocol.TypeStatusUpdate)
	}
}

// --- publishTaskAssignment tests ---

func TestPublishTaskAssignment(t *testing.T) {
	pub := &fakePublisher{}
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "leader",
			TeamName:  "delegteam",
			Role:      "leader",
		},
		client: pub,
	}

	bridge.publishTaskAssignment("worker-1", "implement the feature")

	msgs := pub.getMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 published message, got %d", len(msgs))
	}

	if msgs[0].Subject != "team.delegteam.worker-1" {
		t.Errorf("Subject: got %q, want 'team.delegteam.worker-1'", msgs[0].Subject)
	}
	if msgs[0].Msg.To != "worker-1" {
		t.Errorf("Message.To: got %q, want 'worker-1'", msgs[0].Msg.To)
	}
	if msgs[0].Msg.From != "leader" {
		t.Errorf("Message.From: got %q, want 'leader'", msgs[0].Msg.From)
	}

	var payload protocol.TaskAssignmentPayload
	if err := json.Unmarshal(msgs[0].Msg.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Instruction != "implement the feature" {
		t.Errorf("Instruction: got %q, want 'implement the feature'", payload.Instruction)
	}
}

// --- processEvent tests ---

func TestProcessEvent_ResultPublishesToLeaderWithAgentName(t *testing.T) {
	pub := &fakePublisher{}
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "worker-1",
			TeamName:  "evtteam",
			Role:      "worker",
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

	// Should produce: 1 task_result + 1 status_update (idle).
	var taskResults []publishedMsg
	for _, m := range msgs {
		if m.Msg.Type == protocol.TypeTaskResult {
			taskResults = append(taskResults, m)
		}
	}

	if len(taskResults) != 1 {
		t.Fatalf("expected 1 task_result, got %d (total msgs: %d)", len(taskResults), len(msgs))
	}

	// The critical assertion: Message.To must be "leader" (agent name),
	// NOT "team.evtteam.leader" (NATS subject).
	if taskResults[0].Msg.To != "leader" {
		t.Errorf("Message.To: got %q, want 'leader'", taskResults[0].Msg.To)
	}

	// The NATS subject should be the full subject.
	if taskResults[0].Subject != "team.evtteam.leader" {
		t.Errorf("Subject: got %q, want 'team.evtteam.leader'", taskResults[0].Subject)
	}

	// Verify the result content.
	var payload protocol.TaskResultPayload
	if err := json.Unmarshal(taskResults[0].Msg.Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Status != "completed" {
		t.Errorf("status: got %q, want 'completed'", payload.Status)
	}
	if payload.Result != resultText {
		t.Errorf("result: got %q, want %q", payload.Result, resultText)
	}
}

func TestProcessEvent_ErrorResultPublishesFailedToLeader(t *testing.T) {
	pub := &fakePublisher{}
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "worker-1",
			TeamName:  "errteam",
			Role:      "worker",
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

	var taskResults []publishedMsg
	for _, m := range msgs {
		if m.Msg.Type == protocol.TypeTaskResult {
			taskResults = append(taskResults, m)
		}
	}

	if len(taskResults) != 1 {
		t.Fatalf("expected 1 task_result, got %d", len(taskResults))
	}

	// Message.To must be "leader", not a NATS subject.
	if taskResults[0].Msg.To != "leader" {
		t.Errorf("Message.To: got %q, want 'leader'", taskResults[0].Msg.To)
	}

	var payload protocol.TaskResultPayload
	if err := json.Unmarshal(taskResults[0].Msg.Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Status != "failed" {
		t.Errorf("status: got %q, want 'failed'", payload.Status)
	}
	if payload.Error == "" {
		t.Error("expected non-empty error message")
	}
}

func TestProcessEvent_LeaderDelegates(t *testing.T) {
	pub := &fakePublisher{}
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "leader",
			TeamName:  "delegtest",
			Role:      "leader",
		},
		client: pub,
	}

	resultText := "I'll delegate:\n[TASK:worker-1] Do task A [/TASK]\n[TASK:worker-2] Do task B [/TASK]"
	msgContent, _ := json.Marshal(map[string]string{"type": "text", "text": resultText})
	event := claude.StreamEvent{
		Type:    "result",
		Message: msgContent,
	}

	var currentResult string
	bridge.processEvent(&event, &currentResult)

	msgs := pub.getMessages()

	// Count task_assignment messages.
	var assignments []publishedMsg
	for _, m := range msgs {
		if m.Msg.Type == protocol.TypeTaskAssignment {
			assignments = append(assignments, m)
		}
	}

	if len(assignments) != 2 {
		t.Fatalf("expected 2 task_assignments, got %d", len(assignments))
	}

	// Verify first delegation.
	if assignments[0].Msg.To != "worker-1" {
		t.Errorf("[0] Message.To: got %q, want 'worker-1'", assignments[0].Msg.To)
	}
	if assignments[0].Subject != "team.delegtest.worker-1" {
		t.Errorf("[0] Subject: got %q, want 'team.delegtest.worker-1'", assignments[0].Subject)
	}

	// Verify second delegation.
	if assignments[1].Msg.To != "worker-2" {
		t.Errorf("[1] Message.To: got %q, want 'worker-2'", assignments[1].Msg.To)
	}
	if assignments[1].Subject != "team.delegtest.worker-2" {
		t.Errorf("[1] Subject: got %q, want 'team.delegtest.worker-2'", assignments[1].Subject)
	}
}

func TestProcessEvent_WorkerDoesNotDelegate(t *testing.T) {
	pub := &fakePublisher{}
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "worker-1",
			TeamName:  "nodelegtest",
			Role:      "worker",
		},
		client: pub,
	}

	resultText := "[TASK:worker-2] This should NOT be delegated [/TASK]"
	msgContent, _ := json.Marshal(map[string]string{"type": "text", "text": resultText})
	event := claude.StreamEvent{
		Type:    "result",
		Message: msgContent,
	}

	var currentResult string
	bridge.processEvent(&event, &currentResult)

	msgs := pub.getMessages()

	// Workers should not produce task_assignment messages.
	for _, m := range msgs {
		if m.Msg.Type == protocol.TypeTaskAssignment {
			t.Error("worker should not produce task_assignment messages")
		}
	}
}

func TestProcessEvent_ResultFromResultField(t *testing.T) {
	pub := &fakePublisher{}
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "worker-1",
			TeamName:  "resultfield",
			Role:      "worker",
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
	var taskResults []publishedMsg
	for _, m := range msgs {
		if m.Msg.Type == protocol.TypeTaskResult {
			taskResults = append(taskResults, m)
		}
	}

	if len(taskResults) != 1 {
		t.Fatalf("expected 1 task_result, got %d", len(taskResults))
	}

	var payload protocol.TaskResultPayload
	if err := json.Unmarshal(taskResults[0].Msg.Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Result != "Fallback result text" {
		t.Errorf("result: got %q, want 'Fallback result text'", payload.Result)
	}
}

// --- publishError tests ---

func TestPublishError(t *testing.T) {
	pub := &fakePublisher{}
	bridge := &Bridge{
		config: BridgeConfig{
			AgentName: "worker-1",
			TeamName:  "errteam2",
			Role:      "worker",
		},
		client: pub,
	}

	bridge.publishError("leader", "msg-123", "parse failed")

	msgs := pub.getMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	if msgs[0].Msg.To != "leader" {
		t.Errorf("Message.To: got %q, want 'leader'", msgs[0].Msg.To)
	}

	var payload protocol.TaskResultPayload
	if err := json.Unmarshal(msgs[0].Msg.Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Status != "failed" {
		t.Errorf("status: got %q, want 'failed'", payload.Status)
	}
	if payload.Error != "parse failed" {
		t.Errorf("error: got %q, want 'parse failed'", payload.Error)
	}
}
