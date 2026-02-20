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

	expectedDir := filepath.Join(tmpDir, ".claude", "test-agent")
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

func TestAgentClaudeDir(t *testing.T) {
	dir := AgentClaudeDir("/workspace", "My Agent")
	want := filepath.Join("/workspace", ".claude", "my-agent")
	if dir != want {
		t.Errorf("AgentClaudeDir: got %q, want %q", dir, want)
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

func TestSetupAgentWorkspace_RawClaudeMD(t *testing.T) {
	tmpDir := t.TempDir()

	rawContent := "# Custom Agent Config\n\nThis is user-provided CLAUDE.md content.\n"
	agent := AgentWorkspaceInfo{
		Name:     "custom-agent",
		Role:     "worker",
		ClaudeMD: rawContent,
	}

	dir, err := SetupAgentWorkspace(tmpDir, agent)
	if err != nil {
		t.Fatalf("SetupAgentWorkspace: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("reading CLAUDE.md: %v", err)
	}

	// Should use raw content, not the generated one.
	if string(data) != rawContent {
		t.Errorf("CLAUDE.md: got %q, want %q", string(data), rawContent)
	}
}

func TestSetupAgentWorkspace_EmptyClaudeMD_FallsBackToGenerated(t *testing.T) {
	tmpDir := t.TempDir()

	agent := AgentWorkspaceInfo{
		Name:     "fallback-agent",
		Role:     "worker",
		ClaudeMD: "", // empty — should trigger generateClaudeMD
	}

	dir, err := SetupAgentWorkspace(tmpDir, agent)
	if err != nil {
		t.Fatalf("SetupAgentWorkspace: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("reading CLAUDE.md: %v", err)
	}

	content := string(data)
	if !contains(content, "# Agent: fallback-agent") {
		t.Error("CLAUDE.md should contain generated content when ClaudeMD is empty")
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
