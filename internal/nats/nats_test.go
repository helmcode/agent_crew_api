package nats

import (
	"testing"
	"time"
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
