package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/helmcode/agent-crew/internal/protocol"
)

func TestValidateSkillConfig(t *testing.T) {
	valid := []protocol.SkillConfig{
		{RepoURL: "https://github.com/jezweb/claude-skills", SkillName: "fastapi"},
		{RepoURL: "https://github.com/vercel-labs/agent-skills", SkillName: "vercel-react-best-practices"},
		{RepoURL: "https://github.com/owner/repo", SkillName: "skill"},
		{RepoURL: "https://github.com/my-org/my_repo.v2", SkillName: "my-skill"},
		{RepoURL: "https://github.com/org/repo", SkillName: "@scope/skill"},
	}
	for _, cfg := range valid {
		if err := validateSkillConfig(cfg); err != nil {
			t.Errorf("expected %+v to be valid, got error: %v", cfg, err)
		}
	}

	invalid := []struct {
		cfg    protocol.SkillConfig
		errMsg string
	}{
		{protocol.SkillConfig{RepoURL: "", SkillName: "fastapi"}, "repo_url is required"},
		{protocol.SkillConfig{RepoURL: "https://github.com/owner/repo", SkillName: ""}, "skill_name is required"},
		{protocol.SkillConfig{RepoURL: "http://github.com/owner/repo", SkillName: "skill"}, "repo_url must use https scheme"},
		{protocol.SkillConfig{RepoURL: "https://github.com/owner/repo;rm -rf /", SkillName: "skill"}, "repo_url contains invalid characters"},
		{protocol.SkillConfig{RepoURL: "https://github.com/owner/repo", SkillName: "skill with spaces"}, "skill_name contains invalid characters"},
		{protocol.SkillConfig{RepoURL: "https://github.com/owner/repo", SkillName: "skill;rm"}, "skill_name contains invalid characters"},
	}
	for _, tc := range invalid {
		if err := validateSkillConfig(tc.cfg); err == nil {
			t.Errorf("expected %+v to be invalid (expected error containing %q), but got nil", tc.cfg, tc.errMsg)
		}
	}
}

func TestInstallSkills_EmptySlice(t *testing.T) {
	results := installSkills([]protocol.SkillConfig{})
	if len(results) != 0 {
		t.Errorf("expected 0 results for empty slice, got %d", len(results))
	}
}

func TestInstallSkills_InvalidConfig(t *testing.T) {
	skills := []protocol.SkillConfig{
		{RepoURL: "", SkillName: "fastapi"},
		{RepoURL: "http://github.com/owner/repo", SkillName: "skill"},
	}
	results := installSkills(skills)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for i, r := range results {
		if r.Status != "failed" {
			t.Errorf("result[%d]: expected status=failed, got %q", i, r.Status)
		}
	}
}

func TestInstallSkills_CommandNotFound(t *testing.T) {
	// Override PATH so npx cannot be found.
	origPath := os.Getenv("PATH")
	os.Setenv("PATH", "")
	defer os.Setenv("PATH", origPath)

	skills := []protocol.SkillConfig{
		{RepoURL: "https://github.com/jezweb/claude-skills", SkillName: "fastapi"},
	}
	results := installSkills(skills)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != "failed" {
		t.Errorf("expected status=failed when npx not found, got %q", results[0].Status)
	}
	expectedPkg := "https://github.com/jezweb/claude-skills:fastapi"
	if results[0].Package != expectedPkg {
		t.Errorf("expected package=%q, got %q", expectedPkg, results[0].Package)
	}
}

func TestInstallSkills_MixedInput(t *testing.T) {
	// Override PATH so valid configs also fail at exec time.
	origPath := os.Getenv("PATH")
	os.Setenv("PATH", "")
	defer os.Setenv("PATH", origPath)

	skills := []protocol.SkillConfig{
		{RepoURL: "", SkillName: "bad"},                                                    // invalid: missing repo_url
		{RepoURL: "https://github.com/jezweb/claude-skills", SkillName: "fastapi"},         // valid but npx missing
		{RepoURL: "http://github.com/owner/repo", SkillName: "skill"},                      // invalid: non-https
		{RepoURL: "https://github.com/vercel-labs/agent-skills", SkillName: "react-skills"}, // valid but npx missing
	}
	results := installSkills(skills)
	if len(results) != 4 {
		t.Fatalf("expected 4 results, got %d: %+v", len(results), results)
	}

	// First: invalid (missing repo_url).
	if results[0].Status != "failed" {
		t.Errorf("result[0]: expected failed, got %q", results[0].Status)
	}

	// Second: valid config but exec fails.
	if results[1].Status != "failed" {
		t.Errorf("result[1]: expected failed (exec), got %q", results[1].Status)
	}
	if results[1].Package != "https://github.com/jezweb/claude-skills:fastapi" {
		t.Errorf("result[1]: unexpected package %q", results[1].Package)
	}

	// Third: invalid (non-https).
	if results[2].Status != "failed" {
		t.Errorf("result[2]: expected failed, got %q", results[2].Status)
	}

	// Fourth: valid config but exec fails.
	if results[3].Status != "failed" {
		t.Errorf("result[3]: expected failed (exec), got %q", results[3].Status)
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
