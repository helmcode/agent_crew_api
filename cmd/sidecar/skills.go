package main

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
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

// installSkills installs a list of skill packages globally to ~/.claude/skills/
// using the skills CLI. Skills are NOT copied to /workspace/.claude/skills/.
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
		cmd := exec.Command("npx", "--yes", "@anthropic-ai/claude-code-skills", "add", cfg.RepoURL, "--skill", cfg.SkillName)
		cmd.Dir = "/workspace"
		cmd.Env = append(os.Environ(), "HOME="+os.Getenv("HOME"))
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
