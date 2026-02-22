package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	agentNats "github.com/helmcode/agent-crew/internal/nats"
	"github.com/helmcode/agent-crew/internal/protocol"
)

// validSkillName matches safe skill package names (npm-style with optional scope).
var validSkillName = regexp.MustCompile(`^[a-zA-Z0-9@/_.-]+$`)

// installSkills installs a list of skill packages globally using the skills CLI.
// It returns per-skill results and continues on individual failures.
func installSkills(skills []string) []protocol.SkillInstallResult {
	var results []protocol.SkillInstallResult

	for _, skill := range skills {
		if skill == "" {
			continue
		}

		if !validSkillName.MatchString(skill) {
			slog.Warn("rejected skill with invalid name", "skill", skill)
			results = append(results, protocol.SkillInstallResult{
				Package: skill,
				Status:  "failed",
				Error:   "invalid skill name",
			})
			continue
		}

		slog.Info("installing skill globally", "skill", skill)
		cmd := exec.Command("npx", "skills", "add", skill, "-g", "--yes")
		output, err := cmd.CombinedOutput()
		if err != nil {
			errMsg := fmt.Sprintf("%v: %s", err, string(output))
			slog.Error("failed to install skill", "skill", skill, "error", errMsg)
			results = append(results, protocol.SkillInstallResult{
				Package: skill,
				Status:  "failed",
				Error:   errMsg,
			})
		} else {
			slog.Info("skill installed globally", "skill", skill)
			results = append(results, protocol.SkillInstallResult{
				Package: skill,
				Status:  "installed",
			})
		}
	}

	return results
}

// symlinkSkillsDir creates a symlink from the global skills directory ($HOME/.claude/skills)
// to the workspace skills directory so Claude Code discovers installed skills.
func symlinkSkillsDir(workDir string) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting home dir: %w", err)
	}

	globalSkillsDir := filepath.Join(homeDir, ".claude", "skills")
	workspaceSkillsDir := filepath.Join(workDir, ".claude", "skills")

	// Ensure the global skills directory exists.
	if err := os.MkdirAll(globalSkillsDir, 0755); err != nil {
		return fmt.Errorf("creating global skills dir %s: %w", globalSkillsDir, err)
	}

	// Remove existing workspace skills path if it exists so the symlink
	// can be created cleanly.
	if _, err := os.Lstat(workspaceSkillsDir); err == nil {
		if err := os.RemoveAll(workspaceSkillsDir); err != nil {
			return fmt.Errorf("removing existing workspace skills path %s: %w", workspaceSkillsDir, err)
		}
	}

	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(workspaceSkillsDir), 0755); err != nil {
		return fmt.Errorf("creating workspace .claude dir: %w", err)
	}

	if err := os.Symlink(globalSkillsDir, workspaceSkillsDir); err != nil {
		return fmt.Errorf("creating symlink from %s to %s: %w", globalSkillsDir, workspaceSkillsDir, err)
	}

	slog.Info("created skills symlink", "from", globalSkillsDir, "to", workspaceSkillsDir)
	return nil
}

// publishSkillStatus sends per-skill installation results to the team activity
// NATS channel so the orchestrator/UI can display them.
func publishSkillStatus(client *agentNats.Client, agentName, teamName string, results []protocol.SkillInstallResult) {
	installed, failed := 0, 0
	for _, r := range results {
		if r.Status == "installed" {
			installed++
		} else {
			failed++
		}
	}
	summary := fmt.Sprintf("%d installed, %d failed", installed, failed)

	slog.Info("skill installation complete", "summary", summary)

	payload := protocol.SkillStatusPayload{
		AgentName: agentName,
		Skills:    results,
		Summary:   summary,
	}

	msg, err := protocol.NewMessage(agentName, "system", protocol.TypeSkillStatus, payload)
	if err != nil {
		slog.Error("failed to create skill status message", "error", err)
		return
	}

	subject, err := protocol.TeamActivityChannel(teamName)
	if err != nil {
		slog.Error("failed to build activity channel for skill status", "error", err)
		return
	}

	if err := client.Publish(subject, msg); err != nil {
		slog.Error("failed to publish skill status", "error", err)
	}
}
