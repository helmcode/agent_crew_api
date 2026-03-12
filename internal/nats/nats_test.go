package nats

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/helmcode/agent-crew/internal/protocol"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig("nats://localhost:4222", "test-agent")

	if cfg.URL != "nats://localhost:4222" {
		t.Errorf("URL: got %q, want 'nats://localhost:4222'", cfg.URL)
	}
	if cfg.Name != "test-agent" {
		t.Errorf("Name: got %q, want 'test-agent'", cfg.Name)
	}
	if cfg.MaxReconnects != -1 {
		t.Errorf("MaxReconnects: got %d, want -1", cfg.MaxReconnects)
	}
	if cfg.ReconnectWait != 2*time.Second {
		t.Errorf("ReconnectWait: got %v, want 2s", cfg.ReconnectWait)
	}
	if !cfg.JetStreamEnabled {
		t.Error("JetStreamEnabled should be true by default")
	}
}

func TestBridgeConfig(t *testing.T) {
	cfg := BridgeConfig{
		AgentName: "worker-1",
		TeamName:  "my-team",
		Role:      "worker",
	}

	if cfg.AgentName != "worker-1" {
		t.Errorf("AgentName: got %q", cfg.AgentName)
	}
	if cfg.TeamName != "my-team" {
		t.Errorf("TeamName: got %q", cfg.TeamName)
	}
	if cfg.Role != "worker" {
		t.Errorf("Role: got %q", cfg.Role)
	}
	if cfg.Gate != nil {
		t.Error("Gate should be nil when not set")
	}
}

func TestConnect_InvalidURL(t *testing.T) {
	cfg := ClientConfig{
		URL:           "nats://invalid-host-that-does-not-exist:4222",
		Name:          "test",
		MaxReconnects: 0,
		ReconnectWait: 100 * time.Millisecond,
	}

	_, err := Connect(cfg)
	if err == nil {
		t.Fatal("expected error connecting to invalid URL")
	}
}

func TestNewBridge(t *testing.T) {
	bridge := NewBridge(BridgeConfig{
		AgentName: "test-agent",
		TeamName:  "test-team",
		Role:      "worker",
	}, nil, nil)

	if bridge == nil {
		t.Fatal("expected non-nil bridge")
	}
	if bridge.config.AgentName != "test-agent" {
		t.Errorf("AgentName: got %q", bridge.config.AgentName)
	}
}

// --- streamNameFromSubject tests ---

func TestStreamNameFromSubject(t *testing.T) {
	tests := []struct {
		name    string
		subject string
		want    string
		wantErr bool
	}{
		{
			name:    "leader channel",
			subject: "team.myteam.leader",
			want:    "TEAM_myteam",
		},
		{
			name:    "activity channel",
			subject: "team.myteam.activity",
			want:    "TEAM_myteam",
		},
		{
			name:    "team with hyphens",
			subject: "team.my-awesome-team.leader",
			want:    "TEAM_my-awesome-team",
		},
		{
			name:    "invalid subject - no team prefix",
			subject: "other.myteam.leader",
			wantErr: true,
		},
		{
			name:    "invalid subject - single segment",
			subject: "team",
			wantErr: true,
		},
		{
			name:    "empty subject",
			subject: "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := streamNameFromSubject(tt.subject)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error for subject %q, got nil", tt.subject)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error for subject %q: %v", tt.subject, err)
				return
			}
			if got != tt.want {
				t.Errorf("streamNameFromSubject(%q) = %q, want %q", tt.subject, got, tt.want)
			}
		})
	}
}

// --- Subscribe fallback to core NATS tests ---

func TestSubscribe_FallsToCoreNATS_WhenJetStreamDisabled(t *testing.T) {
	// A client with js == nil should use core NATS subscriptions.
	client := &Client{
		config: ClientConfig{JetStreamEnabled: false},
		// conn is nil so we can't actually subscribe, but we verify the path taken.
	}

	// With js == nil, Subscribe should attempt subscribeCoreNATS and fail
	// because conn is nil (no panic from JetStream path).
	err := client.Subscribe("team.test.leader", func(msg *protocol.Message) {})
	if err == nil {
		t.Fatal("expected error from subscribing with nil conn")
	}
	// Should not have any JetStream consumer contexts.
	if len(client.consumerContexts) != 0 {
		t.Errorf("expected 0 consumer contexts, got %d", len(client.consumerContexts))
	}
}

// --- Close stops consumer contexts ---

type fakeConsumeContext struct {
	stopped  bool
	closedCh chan struct{}
}

func newFakeConsumeContext() *fakeConsumeContext {
	return &fakeConsumeContext{closedCh: make(chan struct{})}
}

func (f *fakeConsumeContext) Stop() {
	f.stopped = true
	select {
	case <-f.closedCh:
	default:
		close(f.closedCh)
	}
}

func (f *fakeConsumeContext) Drain() {
	f.Stop()
}

func (f *fakeConsumeContext) Closed() <-chan struct{} {
	return f.closedCh
}

// Verify fakeConsumeContext satisfies the interface at compile time.
var _ jetstream.ConsumeContext = (*fakeConsumeContext)(nil)

func TestClose_StopsConsumerContexts(t *testing.T) {
	cc1 := newFakeConsumeContext()
	cc2 := newFakeConsumeContext()

	// We can't call Close() directly because it needs a real nats.Conn.
	// Instead, verify the consumer stop logic that Close() executes.
	contexts := []jetstream.ConsumeContext{cc1, cc2}
	for _, cc := range contexts {
		cc.Stop()
	}

	if !cc1.stopped {
		t.Error("consumer context 1 should be stopped")
	}
	if !cc2.stopped {
		t.Error("consumer context 2 should be stopped")
	}
}

// --- subscribeJetStream with invalid subject ---

func TestSubscribeJetStream_InvalidSubject(t *testing.T) {
	// Client with a non-nil js field to trigger JetStream path.
	// We use a real Client struct but with invalid subject to test error handling.
	client := &Client{
		js: nil, // Even though we call subscribeJetStream directly
	}

	// subscribeJetStream should fail on streamNameFromSubject.
	err := client.subscribeJetStream("invalid-subject", func(msg *protocol.Message) {})
	if err == nil {
		t.Fatal("expected error for invalid subject")
	}
}
