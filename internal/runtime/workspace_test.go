package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSetupAgentWorkspace(t *testing.T) {
	tmpDir := t.TempDir()

	agent := AgentWorkspaceInfo{
		Name:         "test-agent",
		Role:         "worker",
		Specialty:    "testing",
		SystemPrompt: "You are a test agent.",
		Skills:       json.RawMessage(`["go","python"]`),
	}

	dir, err := SetupAgentWorkspace(tmpDir, agent)
	if err != nil {
		t.Fatalf("SetupAgentWorkspace: %v", err)
	}

	expectedDir := filepath.Join(tmpDir, ".agentcrew", "test-agent")
	if dir != expectedDir {
		t.Errorf("dir: got %q, want %q", dir, expectedDir)
	}

	claudeMD := filepath.Join(dir, "CLAUDE.md")
	data, err := os.ReadFile(claudeMD)
	if err != nil {
		t.Fatalf("reading CLAUDE.md: %v", err)
	}

	content := string(data)
	if !contains(content, "# Agent: test-agent") {
		t.Error("CLAUDE.md missing agent name")
	}
	if !contains(content, "worker") {
		t.Error("CLAUDE.md missing role")
	}
	if !contains(content, "testing") {
		t.Error("CLAUDE.md missing specialty")
	}
	if !contains(content, "You are a test agent.") {
		t.Error("CLAUDE.md missing system prompt")
	}
}

func TestAgentConfigDir(t *testing.T) {
	dir := AgentConfigDir("/workspace", "My Agent")
	want := filepath.Join("/workspace", ".agentcrew", "my-agent")
	if dir != want {
		t.Errorf("AgentConfigDir: got %q, want %q", dir, want)
	}
}

func TestSyncUserClaudeConfig_NoClaudeDir(t *testing.T) {
	tmpDir := t.TempDir()

	// No .claude/ directory exists — should return nil without error.
	err := SyncUserClaudeConfig(tmpDir, "agent1")
	if err != nil {
		t.Fatalf("SyncUserClaudeConfig: %v", err)
	}

	// Agent config dir should not have been created.
	agentDir := AgentConfigDir(tmpDir, "agent1")
	if _, err := os.Stat(agentDir); !os.IsNotExist(err) {
		t.Error("agent config dir should not exist when no .claude dir")
	}
}

func TestSyncUserClaudeConfig_CopiesFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create user's .claude/ directory with config files.
	claudeDir := filepath.Join(tmpDir, ".claude")
	if err := os.MkdirAll(filepath.Join(claudeDir, "commands"), 0755); err != nil {
		t.Fatal(err)
	}

	// settings.json
	settingsContent := `{"theme":"dark","verbose":true}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settingsContent), 0644); err != nil {
		t.Fatal(err)
	}

	// commands/deploy.md
	commandContent := "# Deploy command\nRun deployment steps."
	if err := os.WriteFile(filepath.Join(claudeDir, "commands", "deploy.md"), []byte(commandContent), 0644); err != nil {
		t.Fatal(err)
	}

	// A CLAUDE.md that should be skipped.
	if err := os.WriteFile(filepath.Join(claudeDir, "CLAUDE.md"), []byte("user claude md"), 0644); err != nil {
		t.Fatal(err)
	}

	// Sync to agent.
	if err := SyncUserClaudeConfig(tmpDir, "agent1"); err != nil {
		t.Fatalf("SyncUserClaudeConfig: %v", err)
	}

	agentDir := AgentConfigDir(tmpDir, "agent1")

	// settings.json should be copied.
	data, err := os.ReadFile(filepath.Join(agentDir, "settings.json"))
	if err != nil {
		t.Fatalf("reading settings.json: %v", err)
	}
	if string(data) != settingsContent {
		t.Errorf("settings.json: got %q, want %q", string(data), settingsContent)
	}

	// commands/deploy.md should be copied.
	data, err = os.ReadFile(filepath.Join(agentDir, "commands", "deploy.md"))
	if err != nil {
		t.Fatalf("reading commands/deploy.md: %v", err)
	}
	if string(data) != commandContent {
		t.Errorf("commands/deploy.md: got %q, want %q", string(data), commandContent)
	}

	// CLAUDE.md should NOT be copied (skipped in favor of generated one).
	if _, err := os.Stat(filepath.Join(agentDir, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Error("CLAUDE.md should not be copied from user's .claude dir")
	}
}

func TestSyncUserClaudeConfig_ThenSetupOverwrites(t *testing.T) {
	tmpDir := t.TempDir()

	// Create user's .claude/ with settings.
	claudeDir := filepath.Join(tmpDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{"key":"value"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Step 1: Sync user config.
	if err := SyncUserClaudeConfig(tmpDir, "agent1"); err != nil {
		t.Fatalf("SyncUserClaudeConfig: %v", err)
	}

	// Step 2: Setup agent workspace (generates CLAUDE.md).
	agent := AgentWorkspaceInfo{
		Name: "agent1",
		Role: "worker",
	}
	dir, err := SetupAgentWorkspace(tmpDir, agent)
	if err != nil {
		t.Fatalf("SetupAgentWorkspace: %v", err)
	}

	// The generated CLAUDE.md should exist.
	data, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("reading CLAUDE.md: %v", err)
	}
	if !contains(string(data), "# Agent: agent1") {
		t.Error("CLAUDE.md should contain generated agent content")
	}

	// User's settings.json should still be present.
	data, err = os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatalf("reading settings.json: %v", err)
	}
	if string(data) != `{"key":"value"}` {
		t.Errorf("settings.json: got %q", string(data))
	}
}

func TestGenerateClaudeMD_Leader(t *testing.T) {
	agent := AgentWorkspaceInfo{
		Name:    "lead",
		Role:    "leader",
		TeamMembers: []TeamMemberInfo{
			{Name: "worker-1", Role: "worker", Specialty: "backend"},
			{Name: "worker-2", Role: "worker", Specialty: "frontend"},
		},
	}

	md := generateClaudeMD(agent)

	if !contains(md, "## Team Members") {
		t.Error("leader CLAUDE.md should have Team Members section")
	}
	if !contains(md, "worker-1") {
		t.Error("leader CLAUDE.md should list worker-1")
	}
	if !contains(md, "## Delegation Protocol") {
		t.Error("leader CLAUDE.md should have Delegation Protocol section")
	}
}

func TestGenerateClaudeMD_Worker(t *testing.T) {
	agent := AgentWorkspaceInfo{
		Name: "dev",
		Role: "worker",
	}

	md := generateClaudeMD(agent)

	if contains(md, "## Team Members") {
		t.Error("worker CLAUDE.md should not have Team Members section")
	}
	if contains(md, "## Delegation Protocol") {
		t.Error("worker CLAUDE.md should not have Delegation Protocol section")
	}
}

func TestFormatSkills_StringArray(t *testing.T) {
	raw := json.RawMessage(`["go","python","terraform"]`)
	result := formatSkills(raw)

	if !contains(result, "- go") {
		t.Error("missing skill 'go'")
	}
	if !contains(result, "- terraform") {
		t.Error("missing skill 'terraform'")
	}
}

func TestFormatSkills_ObjectArray(t *testing.T) {
	raw := json.RawMessage(`[{"name":"Go","description":"Backend development"}]`)
	result := formatSkills(raw)

	if !contains(result, "**Go**") {
		t.Error("missing skill name")
	}
	if !contains(result, "Backend development") {
		t.Error("missing skill description")
	}
}

func TestFormatSkills_Empty(t *testing.T) {
	if formatSkills(nil) != "" {
		t.Error("nil skills should return empty string")
	}
	if formatSkills(json.RawMessage(`null`)) != "" {
		t.Error("null skills should return empty string")
	}
	if formatSkills(json.RawMessage(`[]`)) != "" {
		t.Error("empty array should return empty string")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsStr(s, substr)
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
