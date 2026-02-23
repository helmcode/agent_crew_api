package main

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	agentNats "github.com/helmcode/agent-crew/internal/nats"
	"github.com/helmcode/agent-crew/internal/protocol"
)

// validSkillName matches safe skill names: alphanumeric, hyphens, underscores, dots, @, forward slashes.
var validSkillName = regexp.MustCompile(`^[a-zA-Z0-9@/_.-]+$`)

// validateSkillConfig checks that a SkillConfig has a valid HTTPS URL and safe skill name.
func validateSkillConfig(cfg protocol.SkillConfig) error {
	if cfg.RepoURL == "" {
		return fmt.Errorf("repo_url is required")
	}
	if cfg.SkillName == "" {
		return fmt.Errorf("skill_name is required")
	}

	// Validate URL.
	u, err := url.Parse(cfg.RepoURL)
	if err != nil {
		return fmt.Errorf("invalid repo_url: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("repo_url must use https scheme, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("repo_url must have a host")
	}

	// Block shell metacharacters in URL.
	if strings.ContainsAny(cfg.RepoURL, ";|&$`\\\"'<>(){}!") {
		return fmt.Errorf("repo_url contains invalid characters")
	}

	// Validate skill name.
	if !validSkillName.MatchString(cfg.SkillName) {
		return fmt.Errorf("skill_name contains invalid characters")
	}

	return nil
}

// installSkills installs a list of skill packages globally using the skills CLI.
func installSkills(skills []protocol.SkillConfig) []protocol.SkillInstallResult {
	var results []protocol.SkillInstallResult

	for _, cfg := range skills {
		pkg := cfg.RepoURL + ":" + cfg.SkillName

		if err := validateSkillConfig(cfg); err != nil {
			slog.Warn("rejected skill with invalid config", "repo_url", cfg.RepoURL, "skill_name", cfg.SkillName, "error", err)
			results = append(results, protocol.SkillInstallResult{
				Package: pkg,
				Status:  "failed",
				Error:   err.Error(),
			})
			continue
		}

		slog.Info("installing skill globally", "repo_url", cfg.RepoURL, "skill_name", cfg.SkillName)
		cmd := exec.Command("npx", "skills", "add", cfg.RepoURL, "--skill", cfg.SkillName, "-g", "--yes")
		output, err := cmd.CombinedOutput()
		if err != nil {
			errMsg := fmt.Sprintf("%v: %s", err, string(output))
			slog.Error("failed to install skill", "repo_url", cfg.RepoURL, "skill_name", cfg.SkillName, "error", errMsg)
			results = append(results, protocol.SkillInstallResult{
				Package: pkg,
				Status:  "failed",
				Error:   errMsg,
			})
		} else {
			slog.Info("skill installed globally", "repo_url", cfg.RepoURL, "skill_name", cfg.SkillName)
			results = append(results, protocol.SkillInstallResult{
				Package: pkg,
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
