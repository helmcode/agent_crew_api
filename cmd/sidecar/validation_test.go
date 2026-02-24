package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/helmcode/agent-crew/internal/protocol"
)

func TestRunContainerValidation_AllOK(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := tmpDir
	claudeDir := filepath.Join(tmpDir, ".claude")

	// Set up valid workspace structure.
	if err := os.MkdirAll(filepath.Join(claudeDir, "agents"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "CLAUDE.md"), []byte("# test"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "agents", "helper.md"), []byte("agent"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create skills directory (<claudeDir>/skills/) with content.
	skillsDir := filepath.Join(claudeDir, "skills")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "pkg1.json"), []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}

	checks := runContainerValidation(workDir, claudeDir, true, true)

	// Expect: claude_md=ok, agents_dir=ok, skills_installed=ok
	if len(checks) < 3 {
		t.Fatalf("expected at least 3 checks, got %d", len(checks))
	}

	checkMap := make(map[string]protocol.ValidationCheck)
	for _, c := range checks {
		checkMap[c.Name] = c
	}

	if c, ok := checkMap["claude_md"]; !ok || c.Status != protocol.ValidationOK {
		t.Errorf("claude_md: got %+v, want status=ok", checkMap["claude_md"])
	}
	if c, ok := checkMap["agents_dir"]; !ok || c.Status != protocol.ValidationOK {
		t.Errorf("agents_dir: got %+v, want status=ok", checkMap["agents_dir"])
	}
	if c, ok := checkMap["skills_installed"]; !ok || c.Status != protocol.ValidationOK {
		t.Errorf("skills_installed: got %+v, want status=ok", checkMap["skills_installed"])
	}
}

func TestRunContainerValidation_MissingClaudeMD(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := tmpDir
	claudeDir := filepath.Join(tmpDir, ".claude")
	os.MkdirAll(claudeDir, 0755)

	checks := runContainerValidation(workDir, claudeDir, false, false)

	if len(checks) != 1 {
		t.Fatalf("expected 1 check, got %d", len(checks))
	}
	if checks[0].Name != "claude_md" || checks[0].Status != protocol.ValidationError {
		t.Errorf("expected claude_md error, got %+v", checks[0])
	}
}

func TestRunContainerValidation_EmptyAgentsDir(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := tmpDir
	claudeDir := filepath.Join(tmpDir, ".claude")

	os.MkdirAll(filepath.Join(claudeDir, "agents"), 0755)
	os.WriteFile(filepath.Join(claudeDir, "CLAUDE.md"), []byte("# test"), 0644)

	checks := runContainerValidation(workDir, claudeDir, false, true)

	checkMap := make(map[string]protocol.ValidationCheck)
	for _, c := range checks {
		checkMap[c.Name] = c
	}

	if c, ok := checkMap["agents_dir"]; !ok || c.Status != protocol.ValidationError {
		t.Errorf("agents_dir: got %+v, want status=error (empty dir)", checkMap["agents_dir"])
	}
}

func TestRunContainerValidation_MissingSkillsDir(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := tmpDir
	claudeDir := filepath.Join(tmpDir, ".claude")
	os.MkdirAll(claudeDir, 0755)
	os.WriteFile(filepath.Join(claudeDir, "CLAUDE.md"), []byte("# test"), 0644)

	// No skills directory exists at <claudeDir>/skills/.
	checks := runContainerValidation(workDir, claudeDir, true, false)

	checkMap := make(map[string]protocol.ValidationCheck)
	for _, c := range checks {
		checkMap[c.Name] = c
	}

	if c, ok := checkMap["skills_installed"]; !ok || c.Status != protocol.ValidationWarning {
		t.Errorf("skills_installed: got %+v, want status=warning", checkMap["skills_installed"])
	}
}

func TestRunContainerValidation_SkillsDirWithContent(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := tmpDir
	claudeDir := filepath.Join(tmpDir, ".claude")

	os.MkdirAll(claudeDir, 0755)
	os.WriteFile(filepath.Join(claudeDir, "CLAUDE.md"), []byte("# test"), 0644)

	// Create skills directory (<claudeDir>/skills/) with content.
	skillsDir := filepath.Join(claudeDir, "skills")
	os.MkdirAll(skillsDir, 0755)
	os.WriteFile(filepath.Join(skillsDir, "my-skill-pkg"), []byte("installed"), 0644)

	checks := runContainerValidation(workDir, claudeDir, true, false)

	checkMap := make(map[string]protocol.ValidationCheck)
	for _, c := range checks {
		checkMap[c.Name] = c
	}

	if c, ok := checkMap["skills_installed"]; !ok {
		t.Error("expected skills_installed check to be present")
	} else if c.Status != protocol.ValidationOK {
		t.Errorf("skills_installed: got status %q, want 'ok'; message: %s", c.Status, c.Message)
	}
}

func TestRunContainerValidation_SkillsDirEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := tmpDir
	claudeDir := filepath.Join(tmpDir, ".claude")

	os.MkdirAll(claudeDir, 0755)
	os.WriteFile(filepath.Join(claudeDir, "CLAUDE.md"), []byte("# test"), 0644)

	// Create skills directory (<claudeDir>/skills/) but leave it empty.
	skillsDir := filepath.Join(claudeDir, "skills")
	os.MkdirAll(skillsDir, 0755)

	checks := runContainerValidation(workDir, claudeDir, true, false)

	checkMap := make(map[string]protocol.ValidationCheck)
	for _, c := range checks {
		checkMap[c.Name] = c
	}

	if c, ok := checkMap["skills_installed"]; !ok {
		t.Error("expected skills_installed check to be present")
	} else if c.Status != protocol.ValidationWarning {
		t.Errorf("skills_installed: got status %q, want 'warning'; message: %s", c.Status, c.Message)
	}
}

func TestRunContainerValidation_NoSkillsOrAgentsConfigured(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := tmpDir
	claudeDir := filepath.Join(tmpDir, ".claude")
	os.MkdirAll(claudeDir, 0755)
	os.WriteFile(filepath.Join(claudeDir, "CLAUDE.md"), []byte("# test"), 0644)

	// Neither skills nor sub-agents configured — only CLAUDE.md check runs.
	checks := runContainerValidation(workDir, claudeDir, false, false)

	if len(checks) != 1 {
		t.Fatalf("expected 1 check (only claude_md), got %d", len(checks))
	}
	if checks[0].Name != "claude_md" || checks[0].Status != protocol.ValidationOK {
		t.Errorf("expected claude_md ok, got %+v", checks[0])
	}
}
