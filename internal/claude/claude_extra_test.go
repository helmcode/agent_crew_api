package claude

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestExtractToolCommand_InvalidJSON(t *testing.T) {
	event := &StreamEvent{
		Type:  "tool_use",
		Name:  "Bash",
		Input: json.RawMessage(`{invalid`),
	}

	tool, cmd, paths := ExtractToolCommand(event)
	if tool != "Bash" {
		t.Errorf("tool: got %q, want 'Bash'", tool)
	}
	// Invalid JSON should not cause a panic; command and paths should be empty.
	if cmd != "" {
		t.Errorf("command: got %q, want empty", cmd)
	}
	if len(paths) != 0 {
		t.Errorf("paths: got %v, want empty", paths)
	}
}

func TestParseStreamOutput_UnparseableLines(t *testing.T) {
	input := "not json at all\n" +
		`{"type":"assistant","message":{"type":"text","text":"Valid"}}` + "\n" +
		"another bad line\n"

	ch := make(chan StreamEvent, 10)
	reader := bytes.NewBufferString(input)

	// ParseStreamOutput does not close the channel, so run it and then
	// close manually (in production, monitor() closes the channel).
	sessionID := ParseStreamOutput(reader, ch)
	close(ch)

	var events []StreamEvent
	for e := range ch {
		events = append(events, e)
	}

	// Only 1 valid event; unparseable lines are skipped.
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "assistant" {
		t.Errorf("event type: got %q, want 'assistant'", events[0].Type)
	}
	// No result event, so no session_id.
	if sessionID != "" {
		t.Errorf("session_id: got %q, want empty", sessionID)
	}
}

func TestContextMonitor_CumulativeTracking(t *testing.T) {
	cm := NewContextMonitor(1000, 0.8)

	cm.TrackInput(make([]byte, 400))  // +100 tokens
	cm.TrackOutput(make([]byte, 800)) // +200 tokens
	cm.TrackInput(make([]byte, 1200)) // +300 tokens
	// Total: 600 tokens -> 60%
	pct := cm.UsagePercent()
	if pct != 60 {
		t.Errorf("expected 60%%, got %d%%", pct)
	}
}

func TestContextMonitor_SmallInputMinimumOneToken(t *testing.T) {
	cm := NewContextMonitor(10000, 0.8)

	// 1 byte should count as 1 token minimum.
	cm.TrackInput(make([]byte, 1))
	cm.TrackOutput(make([]byte, 2))

	// 2 tokens out of 10000 is essentially 0%.
	pct := cm.UsagePercent()
	if pct != 0 {
		t.Errorf("expected ~0%%, got %d%%", pct)
	}
}

func TestNewManager_InitialState(t *testing.T) {
	m := NewManager(ProcessConfig{
		SystemPrompt: "You are a test agent",
		AllowedTools: []string{"Bash"},
		WorkDir:      "/tmp",
		MaxTokens:    100000,
	})

	if m.Status() != "stopped" {
		t.Errorf("initial status: got %q, want 'stopped'", m.Status())
	}
	if m.IsRunning() {
		t.Error("new manager should not be running")
	}
}

func TestNewManager_StopWhenNotRunning(t *testing.T) {
	m := NewManager(ProcessConfig{})

	// Stopping a non-running manager should not error.
	if err := m.Stop(); err != nil {
		t.Errorf("Stop on non-running manager: %v", err)
	}
}

func TestNewManager_SendInputWhenNotRunning(t *testing.T) {
	m := NewManager(ProcessConfig{})

	err := m.SendInput("hello")
	if err == nil {
		t.Error("expected error when sending input to non-running manager")
	}
}

func TestExtractToolCommand_GlobWithPattern(t *testing.T) {
	event := &StreamEvent{
		Type:  "tool_use",
		Name:  "Glob",
		Input: json.RawMessage(`{"pattern":"**/*.go"}`),
	}

	tool, cmd, paths := ExtractToolCommand(event)
	if tool != "Glob" {
		t.Errorf("tool: got %q, want 'Glob'", tool)
	}
	// Pattern is not extracted as command or path.
	if cmd != "" {
		t.Errorf("command: got %q, want empty", cmd)
	}
	if len(paths) != 0 {
		t.Errorf("paths: got %v, want empty", paths)
	}
}
