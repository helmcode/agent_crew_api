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
