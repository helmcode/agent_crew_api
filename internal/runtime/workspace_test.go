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

	// SetupAgentWorkspace now returns {workspace}/.claude (not a per-agent subdir).
	expectedDir := filepath.Join(tmpDir, ".claude")
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
		Name: "lead",
		Role: "leader",
		TeamMembers: []TeamMemberInfo{
			{Name: "worker-1", Role: "worker", Specialty: "backend"},
			{Name: "worker-2", Role: "worker", Specialty: "frontend"},
		},
	}

	md := GenerateClaudeMD(agent)

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

	md := GenerateClaudeMD(agent)

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
		ClaudeMD: "", // empty — should trigger GenerateClaudeMD
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

// --- Sub-agent file tests ---

func TestSetupSubAgentFile_AllFields(t *testing.T) {
	tmpDir := t.TempDir()

	agent := SubAgentInfo{
		Name:        "researcher",
		Description: "Delegate research tasks to this agent",
		Model:       "sonnet",
		Skills:      json.RawMessage(`["read-files","web-search"]`),
		ClaudeMD:    "You are a research specialist.\n",
	}

	filePath, err := SetupSubAgentFile(tmpDir, agent)
	if err != nil {
		t.Fatalf("SetupSubAgentFile: %v", err)
	}

	expectedPath := filepath.Join(tmpDir, ".claude", "agents", "researcher.md")
	if filePath != expectedPath {
		t.Errorf("path: got %q, want %q", filePath, expectedPath)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("reading sub-agent file: %v", err)
	}

	content := string(data)
	if !contains(content, "---\n") {
		t.Error("missing YAML frontmatter delimiters")
	}
	if !contains(content, "name: researcher") {
		t.Error("missing name in frontmatter")
	}
	if !contains(content, "description: ") {
		t.Error("missing description in frontmatter")
	}
	if !contains(content, "model: sonnet") {
		t.Error("missing model in frontmatter")
	}
	// background, isolation, permissionMode are always emitted.
	if !contains(content, "background: true") {
		t.Error("missing background: true in frontmatter")
	}
	if !contains(content, "isolation: worktree") {
		t.Error("missing isolation: worktree in frontmatter")
	}
	if !contains(content, "permissionMode: bypassPermissions") {
		t.Error("missing permissionMode: bypassPermissions in frontmatter")
	}
	if !contains(content, "skills:") {
		t.Error("missing skills in frontmatter")
	}
	if !contains(content, "read-files") {
		t.Error("missing skill 'read-files' in frontmatter")
	}
	if !contains(content, "You are a research specialist.") {
		t.Error("missing body content")
	}
}

func TestSetupSubAgentFile_CreatesAgentsDir(t *testing.T) {
	tmpDir := t.TempDir()

	agent := SubAgentInfo{Name: "test-agent"}
	_, err := SetupSubAgentFile(tmpDir, agent)
	if err != nil {
		t.Fatalf("SetupSubAgentFile: %v", err)
	}

	agentsDir := filepath.Join(tmpDir, ".claude", "agents")
	info, err := os.Stat(agentsDir)
	if err != nil {
		t.Fatalf(".claude/agents/ dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Error(".claude/agents/ should be a directory")
	}
}

func TestGenerateSubAgentContent_OmitsEmptyOptionalFields(t *testing.T) {
	agent := SubAgentInfo{
		Name: "minimal-agent",
	}

	content := GenerateSubAgentContent(agent)

	if !contains(content, "name: minimal-agent") {
		t.Error("missing name")
	}
	if contains(content, "description:") {
		t.Error("empty description should be omitted")
	}
	if contains(content, "model:") {
		t.Error("empty model should be omitted")
	}
	if contains(content, "skills:") {
		t.Error("empty skills should be omitted")
	}
	// background, isolation, permissionMode are always present.
	if !contains(content, "background: true") {
		t.Error("background: true should always be present")
	}
	if !contains(content, "isolation: worktree") {
		t.Error("isolation: worktree should always be present")
	}
	if !contains(content, "permissionMode: bypassPermissions") {
		t.Error("permissionMode: bypassPermissions should always be present")
	}
}

func TestGenerateSubAgentContent_OmitsModelInherit(t *testing.T) {
	agent := SubAgentInfo{
		Name:  "default-agent",
		Model: "inherit",
	}

	content := GenerateSubAgentContent(agent)

	if contains(content, "model:") {
		t.Error("model 'inherit' should be omitted")
	}
}

func TestGenerateSubAgentContent_WithBody(t *testing.T) {
	agent := SubAgentInfo{
		Name:     "body-agent",
		ClaudeMD: "Custom instructions for this agent.",
	}

	content := GenerateSubAgentContent(agent)

	// Body should appear after the closing ---
	parts := splitFrontmatter(content)
	if len(parts) < 2 {
		t.Fatal("expected frontmatter and body sections")
	}
	if !contains(parts[1], "Custom instructions for this agent.") {
		t.Errorf("body section should contain ClaudeMD content, got %q", parts[1])
	}
}

func TestGenerateSubAgentContent_NoBodyWhenEmpty(t *testing.T) {
	agent := SubAgentInfo{
		Name: "no-body-agent",
	}

	content := GenerateSubAgentContent(agent)

	// background, isolation, permissionMode are always emitted.
	expected := "---\nname: no-body-agent\nbackground: true\nisolation: worktree\npermissionMode: bypassPermissions\n---\n"
	if content != expected {
		t.Errorf("content: got %q, want %q", content, expected)
	}
}

func TestGenerateSubAgentContent_YAMLQuoting(t *testing.T) {
	agent := SubAgentInfo{
		Name:        "quoted-agent",
		Description: "Agent: handles complex tasks",
	}

	content := GenerateSubAgentContent(agent)

	// Description with colon should be quoted.
	if !contains(content, `description: "Agent`) {
		t.Errorf("description with colon should be quoted, got:\n%s", content)
	}
}

func TestGenerateSubAgentContent_WithSkills(t *testing.T) {
	agent := SubAgentInfo{
		Name:   "skilled-agent",
		Skills: json.RawMessage(`["web-search","read-files"]`),
	}

	content := GenerateSubAgentContent(agent)

	if !contains(content, "skills:") {
		t.Error("missing skills section")
	}
	if !contains(content, "  - web-search") {
		t.Error("missing skill 'web-search'")
	}
	if !contains(content, "  - read-files") {
		t.Error("missing skill 'read-files'")
	}
}

func TestSetupSubAgentFile_SanitizesName(t *testing.T) {
	tmpDir := t.TempDir()

	agent := SubAgentInfo{Name: "My Agent Name"}
	filePath, err := SetupSubAgentFile(tmpDir, agent)
	if err != nil {
		t.Fatalf("SetupSubAgentFile: %v", err)
	}

	expectedPath := filepath.Join(tmpDir, ".claude", "agents", "my-agent-name.md")
	if filePath != expectedPath {
		t.Errorf("path: got %q, want %q", filePath, expectedPath)
	}
}

// splitFrontmatter splits content by the second "---\n" delimiter.
func splitFrontmatter(content string) []string {
	// Find first --- at beginning
	if !contains(content, "---\n") {
		return []string{content}
	}
	// Remove the opening ---\n
	rest := content[4:]
	idx := 0
	for i := 0; i <= len(rest)-4; i++ {
		if rest[i:i+4] == "---\n" {
			idx = i + 4
			break
		}
	}
	if idx == 0 {
		return []string{content}
	}
	return []string{rest[:idx-4], rest[idx:]}
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

func TestFormatSkillsYAML_StringArray(t *testing.T) {
	raw := json.RawMessage(`["web-search","read-files"]`)
	result := formatSkillsYAML(raw)

	if !contains(result, "  - web-search") {
		t.Error("missing skill 'web-search' with YAML indentation")
	}
	if !contains(result, "  - read-files") {
		t.Error("missing skill 'read-files' with YAML indentation")
	}
}

func TestFormatSkillsYAML_Empty(t *testing.T) {
	if formatSkillsYAML(nil) != "" {
		t.Error("nil skills should return empty string")
	}
	if formatSkillsYAML(json.RawMessage(`null`)) != "" {
		t.Error("null skills should return empty string")
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
