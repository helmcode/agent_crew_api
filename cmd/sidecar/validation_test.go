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

	// Create a skills symlink that resolves.
	globalSkills := filepath.Join(tmpDir, "global-skills")
	if err := os.MkdirAll(globalSkills, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalSkills, "pkg1"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	skillsDir := filepath.Join(claudeDir, "skills")
	if err := os.Symlink(globalSkills, skillsDir); err != nil {
		t.Fatal(err)
	}

	// Override HOME so skills_installed check finds the global dir.
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	// Place the global skills in the expected HOME path.
	homeSkills := filepath.Join(tmpDir, ".claude", "skills")
	// .claude/skills already exists as a symlink to globalSkills, but the
	// validation checks the HOME-based path. We need to set it up at ~/.claude/skills.
	// Since claudeDir is already at tmpDir/.claude, the symlink is correct.

	checks := runContainerValidation(workDir, claudeDir, true, true)

	// Expect: claude_md=ok, agents_dir=ok, skills_symlink=ok, skills_installed=ok
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
	if c, ok := checkMap["skills_symlink"]; !ok || c.Status != protocol.ValidationOK {
		t.Errorf("skills_symlink: got %+v, want status=ok", checkMap["skills_symlink"])
	}

	_ = homeSkills // used conceptually via HOME env
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

func TestRunContainerValidation_BrokenSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := tmpDir
	claudeDir := filepath.Join(tmpDir, ".claude")
	os.MkdirAll(claudeDir, 0755)
	os.WriteFile(filepath.Join(claudeDir, "CLAUDE.md"), []byte("# test"), 0644)

	// Create a broken symlink.
	skillsDir := filepath.Join(claudeDir, "skills")
	os.Symlink("/nonexistent/path", skillsDir)

	checks := runContainerValidation(workDir, claudeDir, true, false)

	checkMap := make(map[string]protocol.ValidationCheck)
	for _, c := range checks {
		checkMap[c.Name] = c
	}

	if c, ok := checkMap["skills_symlink"]; !ok || c.Status != protocol.ValidationWarning {
		t.Errorf("skills_symlink: got %+v, want status=warning", checkMap["skills_symlink"])
	}
}

func TestRunContainerValidation_SkillsInstalledOK(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := tmpDir
	claudeDir := filepath.Join(tmpDir, ".claude")

	os.MkdirAll(claudeDir, 0755)
	os.WriteFile(filepath.Join(claudeDir, "CLAUDE.md"), []byte("# test"), 0644)

	// Create global skills directory with packages at $HOME/.claude/skills.
	globalSkills := filepath.Join(tmpDir, ".claude", "skills")
	// claudeDir is tmpDir/.claude, so globalSkills = claudeDir/skills.
	// We need to create it as a real directory with content for skills_installed.
	os.MkdirAll(globalSkills, 0755)
	os.WriteFile(filepath.Join(globalSkills, "my-skill-pkg"), []byte("installed"), 0644)

	// The skills_symlink check uses workDir/.claude/skills — since claudeDir
	// IS workDir/.claude, and skills is a real dir, EvalSymlinks will succeed.

	// Override HOME so skills_installed check uses our tmpDir.
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	checks := runContainerValidation(workDir, claudeDir, true, false)

	checkMap := make(map[string]protocol.ValidationCheck)
	for _, c := range checks {
		checkMap[c.Name] = c
	}

	// skills_installed should be OK because globalSkills has a file.
	if c, ok := checkMap["skills_installed"]; !ok {
		t.Error("expected skills_installed check to be present")
	} else if c.Status != protocol.ValidationOK {
		t.Errorf("skills_installed: got status %q, want 'ok'; message: %s", c.Status, c.Message)
	}
}

func TestRunContainerValidation_SkillsInstalledEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := tmpDir
	claudeDir := filepath.Join(tmpDir, ".claude")

	os.MkdirAll(claudeDir, 0755)
	os.WriteFile(filepath.Join(claudeDir, "CLAUDE.md"), []byte("# test"), 0644)

	// Create global skills directory but leave it EMPTY.
	globalSkills := filepath.Join(tmpDir, ".claude", "skills")
	os.MkdirAll(globalSkills, 0755)

	// Override HOME so skills_installed check uses our tmpDir.
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	checks := runContainerValidation(workDir, claudeDir, true, false)

	checkMap := make(map[string]protocol.ValidationCheck)
	for _, c := range checks {
		checkMap[c.Name] = c
	}

	// skills_installed should be WARNING because the directory is empty.
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
