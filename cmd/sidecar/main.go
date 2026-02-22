package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/helmcode/agent-crew/internal/claude"
	agentNats "github.com/helmcode/agent-crew/internal/nats"
	"github.com/helmcode/agent-crew/internal/permissions"
	"github.com/helmcode/agent-crew/internal/protocol"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	slog.Info("starting agent sidecar")

	// 1. Load config.
	configPath := os.Getenv("AGENT_CONFIG_PATH")
	if configPath == "" {
		configPath = "/etc/agentcrew/agent.yaml"
	}

	// If config file doesn't exist, rely entirely on env vars.
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		configPath = ""
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	slog.Info("config loaded",
		"agent", cfg.Agent.Name,
		"team", cfg.Agent.Team,
		"role", cfg.Agent.Role,
		"nats_url", cfg.Agent.NATS.URL,
	)

	// 2. Connect to NATS.
	natsConfig := agentNats.DefaultConfig(
		cfg.Agent.NATS.URL,
		cfg.Agent.Team+"-"+cfg.Agent.Name,
	)
	natsConfig.Token = os.Getenv("NATS_AUTH_TOKEN")
	natsClient, err := agentNats.Connect(natsConfig)
	if err != nil {
		slog.Error("failed to connect to nats", "error", err)
		os.Exit(1)
	}
	defer natsClient.Close()

	// Ensure JetStream stream for the team.
	ctx := context.Background()
	if err := natsClient.EnsureStream(ctx, cfg.Agent.Team); err != nil {
		slog.Warn("failed to ensure jetstream stream (non-fatal)", "error", err)
	}

	// 3. Initialize Permission Gate.
	gate := permissions.NewGate(permissions.PermissionConfig{
		AllowedTools:    cfg.Agent.Permissions.AllowedTools,
		AllowedCommands: cfg.Agent.Permissions.AllowedCommands,
		DeniedCommands:  cfg.Agent.Permissions.DeniedCommands,
		FilesystemScope: cfg.Agent.Permissions.FilesystemScope,
	})

	// 4. Write workspace config files if content was passed via env vars.
	// This ensures agents get their files even when the API server runs inside
	// Docker and cannot write directly to the host workspace path.
	workDir := os.Getenv("WORKSPACE_PATH")
	if workDir == "" {
		workDir = "/workspace"
	}

	claudeDir := workDir + "/.claude"

	if claudeMD := os.Getenv("AGENT_CLAUDE_MD"); claudeMD != "" {
		if err := os.MkdirAll(claudeDir, 0755); err != nil {
			slog.Warn("failed to create .claude dir", "error", err)
		} else if err := os.WriteFile(claudeDir+"/CLAUDE.md", []byte(claudeMD), 0644); err != nil {
			slog.Warn("failed to write CLAUDE.md", "error", err)
		} else {
			slog.Info("wrote CLAUDE.md from env var", "path", claudeDir+"/CLAUDE.md")
		}
	}

	subAgentFilesEnv := os.Getenv("AGENT_SUB_AGENT_FILES")
	if subAgentFilesEnv != "" {
		var subAgentFiles map[string]string
		if err := json.Unmarshal([]byte(subAgentFilesEnv), &subAgentFiles); err != nil {
			slog.Warn("failed to parse AGENT_SUB_AGENT_FILES", "error", err)
		} else {
			agentsDir := claudeDir + "/agents"
			if err := os.MkdirAll(agentsDir, 0755); err != nil {
				slog.Warn("failed to create .claude/agents dir", "error", err)
			} else {
				for filename, content := range subAgentFiles {
					// Security: sanitize filename to prevent path traversal.
					safe := filepath.Base(filename)
					if safe != filename || strings.Contains(filename, "..") || strings.Contains(filename, "/") {
						slog.Warn("rejected sub-agent filename with path traversal", "original", filename, "sanitized", safe)
						continue
					}
					path := filepath.Join(agentsDir, safe)
					if err := os.WriteFile(path, []byte(content), 0644); err != nil {
						slog.Warn("failed to write sub-agent file", "file", safe, "error", err)
					} else {
						slog.Info("wrote sub-agent file from env var", "path", path)
					}
				}
			}
		}
	}

	// 5. Install sub-agent skills globally and symlink into workspace.
	skillsEnv := os.Getenv("AGENT_SKILLS_INSTALL")
	if skillsEnv != "" {
		var skills []string
		if err := json.Unmarshal([]byte(skillsEnv), &skills); err != nil {
			slog.Warn("failed to parse AGENT_SKILLS_INSTALL", "error", err)
		} else {
			results := installSkills(skills)

			// Report per-skill status via NATS.
			publishSkillStatus(natsClient, cfg.Agent.Name, cfg.Agent.Team, results)

			// Create symlink from global skills dir to workspace so Claude
			// Code discovers the skills at its expected path.
			if err := symlinkSkillsDir(workDir); err != nil {
				slog.Warn("failed to symlink skills directory", "error", err)
			}
		}
	}

	// 6. Container file validation phase.
	checks := runContainerValidation(workDir, claudeDir, skillsEnv != "", subAgentFilesEnv != "")
	publishValidationResults(natsClient, cfg.Agent.Name, cfg.Agent.Team, checks)

	// 7. Start Claude Manager.
	processCfg := claude.ProcessConfig{
		SystemPrompt: cfg.Agent.SystemPrompt,
		AllowedTools: cfg.Agent.Permissions.AllowedTools,
		WorkDir:      workDir,
	}

	manager := claude.NewManager(processCfg)
	if err := manager.Start(ctx); err != nil {
		slog.Error("failed to start claude process", "error", err)
		os.Exit(1)
	}

	// 8. Start Bridge (NATS <-> Claude stdin/stdout).
	bridgeCfg := agentNats.BridgeConfig{
		AgentName: cfg.Agent.Name,
		TeamName:  cfg.Agent.Team,
		Role:      cfg.Agent.Role,
		Gate:      gate,
	}

	bridge := agentNats.NewBridge(bridgeCfg, natsClient, manager)
	if err := bridge.Start(ctx); err != nil {
		slog.Error("failed to start bridge", "error", err)
		_ = manager.Stop()
		os.Exit(1)
	}

	slog.Info("agent sidecar ready",
		"agent", cfg.Agent.Name,
		"team", cfg.Agent.Team,
		"role", cfg.Agent.Role,
	)

	// Wait for shutdown signal.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down agent sidecar")

	// Graceful shutdown in reverse order.
	bridge.Stop()
	if err := manager.Stop(); err != nil {
		slog.Error("error stopping claude process", "error", err)
	}
	natsClient.Close()

	slog.Info("agent sidecar stopped")
}

// runContainerValidation checks that all expected workspace files and
// directories exist after the setup phase. Returns a list of validation checks.
func runContainerValidation(workDir, claudeDir string, skillsConfigured, subAgentsConfigured bool) []protocol.ValidationCheck {
	var checks []protocol.ValidationCheck

	// Check 1: CLAUDE.md must exist.
	claudeMDPath := filepath.Join(claudeDir, "CLAUDE.md")
	if _, err := os.Stat(claudeMDPath); err != nil {
		checks = append(checks, protocol.ValidationCheck{
			Name:    "claude_md",
			Status:  protocol.ValidationError,
			Message: fmt.Sprintf("CLAUDE.md not found at %s", claudeMDPath),
		})
	} else {
		checks = append(checks, protocol.ValidationCheck{
			Name:    "claude_md",
			Status:  protocol.ValidationOK,
			Message: "CLAUDE.md exists",
		})
	}

	// Check 2: agents directory has files (only if sub-agents were configured).
	agentsDir := filepath.Join(claudeDir, "agents")
	if subAgentsConfigured {
		entries, err := os.ReadDir(agentsDir)
		if err != nil || len(entries) == 0 {
			checks = append(checks, protocol.ValidationCheck{
				Name:    "agents_dir",
				Status:  protocol.ValidationError,
				Message: fmt.Sprintf("agents directory missing or empty at %s", agentsDir),
			})
		} else {
			checks = append(checks, protocol.ValidationCheck{
				Name:    "agents_dir",
				Status:  protocol.ValidationOK,
				Message: fmt.Sprintf("agents directory has %d file(s)", len(entries)),
			})
		}
	}

	// Check 3: skills symlink exists and resolves (only if skills were configured).
	if skillsConfigured {
		workspaceSkillsDir := filepath.Join(workDir, ".claude", "skills")
		resolved, err := filepath.EvalSymlinks(workspaceSkillsDir)
		if err != nil {
			checks = append(checks, protocol.ValidationCheck{
				Name:    "skills_symlink",
				Status:  protocol.ValidationWarning,
				Message: fmt.Sprintf("skills symlink missing or broken at %s: %v", workspaceSkillsDir, err),
			})
		} else {
			checks = append(checks, protocol.ValidationCheck{
				Name:    "skills_symlink",
				Status:  protocol.ValidationOK,
				Message: fmt.Sprintf("skills symlink resolves to %s", resolved),
			})
		}

		// Check 4: global skills directory has installed packages.
		homeDir, err := os.UserHomeDir()
		if err == nil {
			globalSkillsDir := filepath.Join(homeDir, ".claude", "skills")
			entries, err := os.ReadDir(globalSkillsDir)
			if err != nil || len(entries) == 0 {
				checks = append(checks, protocol.ValidationCheck{
					Name:    "skills_installed",
					Status:  protocol.ValidationWarning,
					Message: fmt.Sprintf("no installed skill packages found in %s", globalSkillsDir),
				})
			} else {
				checks = append(checks, protocol.ValidationCheck{
					Name:    "skills_installed",
					Status:  protocol.ValidationOK,
					Message: fmt.Sprintf("%d skill package(s) installed", len(entries)),
				})
			}
		}
	}

	return checks
}

// publishValidationResults publishes validation check results to the team
// activity NATS channel so the API relay can save them as TaskLogs.
func publishValidationResults(client *agentNats.Client, agentName, teamName string, checks []protocol.ValidationCheck) {
	okCount, warnCount, errCount := 0, 0, 0
	for _, c := range checks {
		switch c.Status {
		case protocol.ValidationOK:
			okCount++
		case protocol.ValidationWarning:
			warnCount++
		case protocol.ValidationError:
			errCount++
		}
	}
	summary := fmt.Sprintf("%d ok, %d warning(s), %d error(s)", okCount, warnCount, errCount)

	slog.Info("container validation complete", "summary", summary)
	for _, c := range checks {
		slog.Info("validation check", "name", c.Name, "status", c.Status, "message", c.Message)
	}

	payload := protocol.ContainerValidationPayload{
		AgentName: agentName,
		Checks:    checks,
		Summary:   summary,
	}

	msg, err := protocol.NewMessage(agentName, "system", protocol.TypeContainerValidation, payload)
	if err != nil {
		slog.Error("failed to create validation message", "error", err)
		return
	}

	subject, err := protocol.TeamActivityChannel(teamName)
	if err != nil {
		slog.Error("failed to build activity channel for validation", "error", err)
		return
	}

	if err := client.Publish(subject, msg); err != nil {
		slog.Error("failed to publish validation results", "error", err)
	}
}
