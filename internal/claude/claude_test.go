package claude

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestParseStreamEvent(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantType string
		wantErr  bool
	}{
		{
			name:     "assistant event",
			input:    `{"type":"assistant","message":{"type":"text","text":"Hello"}}`,
			wantType: "assistant",
		},
		{
			name:     "tool_use event",
			input:    `{"type":"tool_use","name":"Bash","input":{"command":"ls -la"}}`,
			wantType: "tool_use",
		},
		{
			name:     "result event",
			input:    `{"type":"result","message":{"type":"text","text":"Done"}}`,
			wantType: "result",
		},
		{
			name:    "invalid JSON",
			input:   `{invalid`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, err := ParseStreamEvent([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if event.Type != tt.wantType {
				t.Errorf("type: got %q, want %q", event.Type, tt.wantType)
			}
		})
	}
}

func TestExtractToolCommand_Bash(t *testing.T) {
	event := &StreamEvent{
		Type:  "tool_use",
		Name:  "Bash",
		Input: json.RawMessage(`{"command":"terraform plan"}`),
	}

	tool, cmd, paths := ExtractToolCommand(event)
	if tool != "Bash" {
		t.Errorf("tool: got %q, want 'Bash'", tool)
	}
	if cmd != "terraform plan" {
		t.Errorf("command: got %q, want 'terraform plan'", cmd)
	}
	if len(paths) != 0 {
		t.Errorf("paths: expected empty, got %v", paths)
	}
}

func TestExtractToolCommand_Read(t *testing.T) {
	event := &StreamEvent{
		Type:  "tool_use",
		Name:  "Read",
		Input: json.RawMessage(`{"file_path":"/workspace/main.tf"}`),
	}

	tool, cmd, paths := ExtractToolCommand(event)
	if tool != "Read" {
		t.Errorf("tool: got %q, want 'Read'", tool)
	}
	if cmd != "" {
		t.Errorf("command: expected empty, got %q", cmd)
	}
	if len(paths) != 1 || paths[0] != "/workspace/main.tf" {
		t.Errorf("paths: got %v, want [/workspace/main.tf]", paths)
	}
}

func TestExtractToolCommand_EmptyInput(t *testing.T) {
	event := &StreamEvent{
		Type: "tool_use",
		Name: "SomeTool",
	}

	tool, cmd, paths := ExtractToolCommand(event)
	if tool != "SomeTool" {
		t.Errorf("tool: got %q", tool)
	}
	if cmd != "" || len(paths) != 0 {
		t.Error("expected empty command and paths for nil input")
	}
}

func TestFormatToolResult(t *testing.T) {
	result := FormatToolResult("output text", false)

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if parsed["type"] != "tool_result" {
		t.Errorf("type: got %v", parsed["type"])
	}
	if parsed["output"] != "output text" {
		t.Errorf("output: got %v", parsed["output"])
	}
	if parsed["is_error"] != false {
		t.Errorf("is_error: got %v", parsed["is_error"])
	}
}

func TestFormatToolResult_Error(t *testing.T) {
	result := FormatToolResult("something failed", true)

	var parsed map[string]interface{}
	json.Unmarshal([]byte(result), &parsed)

	if parsed["is_error"] != true {
		t.Errorf("is_error: got %v, want true", parsed["is_error"])
	}
}

func TestParseStreamOutput(t *testing.T) {
	lines := strings.Join([]string{
		`{"type":"assistant","message":{"type":"text","text":"Hello"}}`,
		``,
		`{"type":"tool_use","name":"Bash","input":{"command":"ls"}}`,
		`{"type":"result","message":{"type":"text","text":"Done"}}`,
	}, "\n")

	ch := make(chan StreamEvent, 10)
	reader := bytes.NewReader([]byte(lines))

	ParseStreamOutput(reader, ch)

	events := make([]StreamEvent, 0)
	for e := range ch {
		events = append(events, e)
		if len(events) == 3 {
			break
		}
	}

	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	if events[0].Type != "assistant" {
		t.Errorf("event 0: got type %q", events[0].Type)
	}
	if events[1].Type != "tool_use" {
		t.Errorf("event 1: got type %q", events[1].Type)
	}
	if events[2].Type != "result" {
		t.Errorf("event 2: got type %q", events[2].Type)
	}
}

func TestContextMonitor_TrackAndUsage(t *testing.T) {
	cm := NewContextMonitor(1000, 0.8)

	// Track 200 bytes => ~50 tokens => 5%.
	cm.TrackInput(make([]byte, 200))
	pct := cm.UsagePercent()
	if pct != 5 {
		t.Errorf("usage: got %d%%, want 5%%", pct)
	}

	if cm.NeedsCompaction() {
		t.Error("should not need compaction at 5%")
	}
}

func TestContextMonitor_NeedsCompaction(t *testing.T) {
	cm := NewContextMonitor(100, 0.8)

	// Track enough to exceed 80%: 400 bytes => 100 tokens => 100%.
	cm.TrackInput(make([]byte, 400))

	if !cm.NeedsCompaction() {
		t.Error("should need compaction at 100%")
	}

	if cm.UsagePercent() != 100 {
		t.Errorf("usage: got %d%%, want 100%%", cm.UsagePercent())
	}
}

func TestContextMonitor_Reset(t *testing.T) {
	cm := NewContextMonitor(100, 0.8)
	cm.TrackInput(make([]byte, 400))
	cm.Reset()

	if cm.UsagePercent() != 0 {
		t.Errorf("after reset: got %d%%, want 0%%", cm.UsagePercent())
	}
}

func TestContextMonitor_ZeroMaxTokens(t *testing.T) {
	cm := NewContextMonitor(0, 0.8)
	cm.TrackInput(make([]byte, 100))

	if cm.UsagePercent() != 0 {
		t.Error("zero max tokens should return 0%")
	}
	if cm.NeedsCompaction() {
		t.Error("zero max tokens should never need compaction")
	}
}

func TestGenerateResumptionPrompt(t *testing.T) {
	prompt := GenerateResumptionPrompt(
		"Deploy the service",
		"Terraform plan completed",
		[]string{"main.tf", "variables.tf"},
	)

	if !strings.Contains(prompt, "Deploy the service") {
		t.Error("prompt should contain original task")
	}
	if !strings.Contains(prompt, "Terraform plan completed") {
		t.Error("prompt should contain progress")
	}
	if !strings.Contains(prompt, "main.tf") {
		t.Error("prompt should contain modified files")
	}
	if !strings.Contains(prompt, "Continue from where you left off") {
		t.Error("prompt should contain continuation instruction")
	}
}

func TestGenerateResumptionPrompt_NoFiles(t *testing.T) {
	prompt := GenerateResumptionPrompt("Task", "Progress", nil)

	if strings.Contains(prompt, "Modified Files") {
		t.Error("prompt should not contain 'Modified Files' section when no files")
	}
}
