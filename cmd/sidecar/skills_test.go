package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidSkillName(t *testing.T) {
	valid := []string{
		"my-skill",
		"@org/my-skill",
		"skill_v2",
		"skill.js",
		"simple",
		"a1b2c3",
	}
	for _, name := range valid {
		if !validSkillName.MatchString(name) {
			t.Errorf("expected %q to be a valid skill name", name)
		}
	}

	invalid := []string{
		"",
		"skill with spaces",
		"skill;rm -rf /",
		"skill$(cmd)",
		"skill`cmd`",
		"skill|pipe",
		"skill&bg",
		"skill\nnewline",
	}
	for _, name := range invalid {
		if validSkillName.MatchString(name) {
			t.Errorf("expected %q to be an invalid skill name", name)
		}
	}
}

func TestInstallSkills_EmptyAndInvalidNames(t *testing.T) {
	// Empty skill names should be skipped entirely.
	results := installSkills([]string{""})
	if len(results) != 0 {
		t.Errorf("expected 0 results for empty skill name, got %d", len(results))
	}

	// Invalid skill names should be rejected with "failed" status.
	results = installSkills([]string{"bad skill name!"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result for invalid name, got %d", len(results))
	}
	if results[0].Status != "failed" {
		t.Errorf("expected status=failed, got %q", results[0].Status)
	}
	if results[0].Error != "invalid skill name" {
		t.Errorf("expected error='invalid skill name', got %q", results[0].Error)
	}
}

func TestInstallSkills_CommandNotFound(t *testing.T) {
	// When npx/skills CLI is not available, the command should fail gracefully
	// and report status=failed with an error message.

	// Override PATH so npx cannot be found.
	origPath := os.Getenv("PATH")
	os.Setenv("PATH", "")
	defer os.Setenv("PATH", origPath)

	results := installSkills([]string{"valid-skill-name"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != "failed" {
		t.Errorf("expected status=failed when npx not found, got %q", results[0].Status)
	}
	if results[0].Package != "valid-skill-name" {
		t.Errorf("expected package='valid-skill-name', got %q", results[0].Package)
	}
}

func TestInstallSkills_MixedInput(t *testing.T) {
	// Override PATH so valid names also fail at exec time.
	origPath := os.Getenv("PATH")
	os.Setenv("PATH", "")
	defer os.Setenv("PATH", origPath)

	results := installSkills([]string{"", "bad name!", "@org/valid-pkg", ""})
	// Empty strings are skipped, "bad name!" is rejected, "@org/valid-pkg" fails at exec.
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d: %+v", len(results), results)
	}

	// First result: invalid name rejection.
	if results[0].Package != "bad name!" || results[0].Status != "failed" {
		t.Errorf("result[0]: expected bad name rejection, got %+v", results[0])
	}

	// Second result: exec failure.
	if results[1].Package != "@org/valid-pkg" || results[1].Status != "failed" {
		t.Errorf("result[1]: expected exec failure, got %+v", results[1])
	}
}

func TestSymlinkSkillsDir_CreatesSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "workspace")

	// Override HOME to use temp dir.
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	if err := symlinkSkillsDir(workDir); err != nil {
		t.Fatalf("symlinkSkillsDir failed: %v", err)
	}

	// Verify the symlink was created.
	workspaceSkillsDir := filepath.Join(workDir, ".claude", "skills")
	info, err := os.Lstat(workspaceSkillsDir)
	if err != nil {
		t.Fatalf("expected symlink at %s, got error: %v", workspaceSkillsDir, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected symlink, got mode %v", info.Mode())
	}

	// Verify it points to the global skills dir.
	target, err := os.Readlink(workspaceSkillsDir)
	if err != nil {
		t.Fatalf("readlink failed: %v", err)
	}
	expectedTarget := filepath.Join(tmpDir, ".claude", "skills")
	if target != expectedTarget {
		t.Errorf("symlink target = %q, want %q", target, expectedTarget)
	}

	// Verify the global skills directory was created.
	if _, err := os.Stat(expectedTarget); err != nil {
		t.Errorf("expected global skills dir to exist at %s: %v", expectedTarget, err)
	}
}

func TestSymlinkSkillsDir_ReplacesExisting(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "workspace")

	// Override HOME.
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	// Pre-create a regular directory where the symlink should go.
	existingDir := filepath.Join(workDir, ".claude", "skills")
	if err := os.MkdirAll(existingDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existingDir, "stale-file"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	// symlinkSkillsDir should replace the existing directory with a symlink.
	if err := symlinkSkillsDir(workDir); err != nil {
		t.Fatalf("symlinkSkillsDir failed: %v", err)
	}

	info, err := os.Lstat(existingDir)
	if err != nil {
		t.Fatalf("expected symlink at %s: %v", existingDir, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected symlink after replacement, got mode %v", info.Mode())
	}
}

func TestSymlinkSkillsDir_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "workspace")

	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	// Call twice — second call should succeed without error.
	if err := symlinkSkillsDir(workDir); err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if err := symlinkSkillsDir(workDir); err != nil {
		t.Fatalf("second call failed: %v", err)
	}

	// Verify symlink still resolves correctly.
	workspaceSkillsDir := filepath.Join(workDir, ".claude", "skills")
	target, err := os.Readlink(workspaceSkillsDir)
	if err != nil {
		t.Fatalf("readlink failed: %v", err)
	}
	expectedTarget := filepath.Join(tmpDir, ".claude", "skills")
	if target != expectedTarget {
		t.Errorf("symlink target = %q, want %q", target, expectedTarget)
	}
}
