package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/helmcode/agent-crew/internal/claude"
	agentNats "github.com/helmcode/agent-crew/internal/nats"
	"github.com/helmcode/agent-crew/internal/opencode"
	"github.com/helmcode/agent-crew/internal/permissions"
	"github.com/helmcode/agent-crew/internal/protocol"
	"github.com/helmcode/agent-crew/internal/provider"
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
		"provider", cfg.Agent.Provider,
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

	// 4. Write workspace config files and start the agent manager.
	workDir := os.Getenv("WORKSPACE_PATH")
	if workDir == "" {
		workDir = "/workspace"
	}

	var manager provider.AgentManager

	switch cfg.Agent.Provider {
	case "opencode":
		manager, err = startOpenCode(ctx, cfg, workDir, natsClient)
	default:
		// "claude" or any unrecognized value defaults to Claude.
		manager, err = startClaude(ctx, cfg, workDir, natsClient)
	}

	if err != nil {
		slog.Error("failed to start agent", "provider", cfg.Agent.Provider, "error", err)
		os.Exit(1)
	}

	// 8. Start Bridge (NATS <-> agent stdin/stdout).
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
		"provider", cfg.Agent.Provider,
	)

	// Wait for shutdown signal.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down agent sidecar")

	// Graceful shutdown in reverse order.
	bridge.Stop()
	if err := manager.Stop(); err != nil {
		slog.Error("error stopping agent", "error", err)
	}
	natsClient.Close()

	slog.Info("agent sidecar stopped")
}

// startClaude handles the Claude Code provider startup flow.
// Writes .claude/CLAUDE.md and .claude/agents/*.md, installs skills,
// validates container files, then starts the Claude process.
func startClaude(ctx context.Context, cfg *AgentConfig, workDir string, natsClient *agentNats.Client) (provider.AgentManager, error) {
	claudeDir := workDir + "/.claude"

	// Write workspace config files from env vars.
	writeClaudeWorkspace(claudeDir)

	// Install skills.
	installSkillsFromEnv(natsClient, cfg)

	// Container validation.
	checks := runContainerValidation(workDir, claudeDir, os.Getenv("AGENT_SKILLS_INSTALL") != "", os.Getenv("AGENT_SUB_AGENT_FILES") != "")
	publishValidationResults(natsClient, cfg.Agent.Name, cfg.Agent.Team, checks)

	// Start Claude Manager.
	processCfg := claude.ProcessConfig{
		SystemPrompt: cfg.Agent.SystemPrompt,
		AllowedTools: cfg.Agent.Permissions.AllowedTools,
		WorkDir:      workDir,
	}

	claudeManager := claude.NewManager(processCfg)
	if err := claudeManager.Start(ctx); err != nil {
		return nil, fmt.Errorf("starting claude process: %w", err)
	}

	return provider.NewClaudeManager(claudeManager), nil
}

// startOpenCode handles the OpenCode provider startup flow.
// Writes .opencode/AGENTS.MD and .opencode/agents/*.md, installs skills
// (to .claude/skills/ as OpenCode reads them natively), starts `opencode serve`,
// waits for health, then creates an OpenCode Manager.
func startOpenCode(ctx context.Context, cfg *AgentConfig, workDir string, natsClient *agentNats.Client) (provider.AgentManager, error) {
	claudeDir := workDir + "/.claude"

	// Write OpenCode workspace files from env vars.
	writeOpenCodeWorkspace(workDir)

	// Skills are always installed to .claude/skills/ — OpenCode reads them natively.
	installSkillsFromEnv(natsClient, cfg)

	// Container validation for OpenCode layout.
	checks := runOpenCodeContainerValidation(workDir, claudeDir, os.Getenv("AGENT_SKILLS_INSTALL") != "", os.Getenv("AGENT_SUB_AGENT_FILES") != "")
	publishValidationResults(natsClient, cfg.Agent.Name, cfg.Agent.Team, checks)

	// Generate a secure random password for the OpenCode server.
	password, err := generateSecurePassword(32)
	if err != nil {
		return nil, fmt.Errorf("generating opencode server password: %w", err)
	}

	// Start `opencode serve` as a background process.
	port := "4096"
	cmd := exec.CommandContext(ctx, "opencode", "serve", "--port", port)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"OPENCODE_SERVER_PASSWORD="+password,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting opencode serve: %w", err)
	}

	slog.Info("opencode serve started", "port", port, "pid", cmd.Process.Pid)

	// Wait for the server to become healthy.
	baseURL := "http://127.0.0.1:" + port
	if err := opencode.WaitForHealth(baseURL, "opencode", password, 60*time.Second); err != nil {
		// Kill the process if health check fails.
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("opencode health check failed: %w", err)
	}

	// Create the OpenCode Manager.
	model := os.Getenv("OPENCODE_MODEL")
	mgr := opencode.NewManager(opencode.Config{
		BaseURL:      baseURL,
		Username:     "opencode",
		Password:     password,
		SystemPrompt: cfg.Agent.SystemPrompt,
		Model:        model,
	})

	if err := mgr.Start(ctx); err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("starting opencode manager: %w", err)
	}

	return mgr, nil
}

// writeClaudeWorkspace writes .claude/CLAUDE.md and .claude/agents/*.md from env vars.
func writeClaudeWorkspace(claudeDir string) {
	if claudeMD := os.Getenv("AGENT_CLAUDE_MD"); claudeMD != "" {
		if err := os.MkdirAll(claudeDir, 0755); err != nil {
			slog.Warn("failed to create .claude dir", "error", err)
		} else if err := os.WriteFile(claudeDir+"/CLAUDE.md", []byte(claudeMD), 0644); err != nil {
			slog.Warn("failed to write CLAUDE.md", "error", err)
		} else {
			slog.Info("wrote CLAUDE.md from env var", "path", claudeDir+"/CLAUDE.md")
		}
	}

	writeSubAgentFiles(claudeDir)
}

// writeOpenCodeWorkspace writes .opencode/AGENTS.MD and .opencode/agents/*.md from env vars.
// Uses AGENT_CLAUDE_MD as the leader instructions content (backward compat).
func writeOpenCodeWorkspace(workDir string) {
	opencodeDir := workDir + "/.opencode"

	if claudeMD := os.Getenv("AGENT_CLAUDE_MD"); claudeMD != "" {
		if err := os.MkdirAll(opencodeDir, 0755); err != nil {
			slog.Warn("failed to create .opencode dir", "error", err)
		} else if err := os.WriteFile(opencodeDir+"/AGENTS.MD", []byte(claudeMD), 0644); err != nil {
			slog.Warn("failed to write AGENTS.MD", "error", err)
		} else {
			slog.Info("wrote AGENTS.MD from env var", "path", opencodeDir+"/AGENTS.MD")
		}
	}

	// Write sub-agent files to .opencode/agents/.
	subAgentFilesEnv := os.Getenv("AGENT_SUB_AGENT_FILES")
	if subAgentFilesEnv != "" {
		var subAgentFiles map[string]string
		if err := json.Unmarshal([]byte(subAgentFilesEnv), &subAgentFiles); err != nil {
			slog.Warn("failed to parse AGENT_SUB_AGENT_FILES", "error", err)
		} else {
			agentsDir := opencodeDir + "/agents"
			if err := os.MkdirAll(agentsDir, 0755); err != nil {
				slog.Warn("failed to create .opencode/agents dir", "error", err)
			} else {
				for filename, content := range subAgentFiles {
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
}

// writeSubAgentFiles writes .claude/agents/*.md files from AGENT_SUB_AGENT_FILES env var.
func writeSubAgentFiles(claudeDir string) {
	subAgentFilesEnv := os.Getenv("AGENT_SUB_AGENT_FILES")
	if subAgentFilesEnv == "" {
		return
	}

	var subAgentFiles map[string]string
	if err := json.Unmarshal([]byte(subAgentFilesEnv), &subAgentFiles); err != nil {
		slog.Warn("failed to parse AGENT_SUB_AGENT_FILES", "error", err)
		return
	}

	agentsDir := claudeDir + "/agents"
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		slog.Warn("failed to create .claude/agents dir", "error", err)
		return
	}

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

// installSkillsFromEnv reads AGENT_SKILLS_INSTALL and installs skills.
func installSkillsFromEnv(natsClient *agentNats.Client, cfg *AgentConfig) {
	skillsEnv := os.Getenv("AGENT_SKILLS_INSTALL")
	if skillsEnv == "" {
		return
	}

	var skills []protocol.SkillConfig
	if err := json.Unmarshal([]byte(skillsEnv), &skills); err != nil {
		slog.Warn("failed to parse AGENT_SKILLS_INSTALL", "error", err)
		return
	}

	results := installSkills(skills)
	publishSkillStatus(natsClient, cfg.Agent.Name, cfg.Agent.Team, results)
}

// generateSecurePassword generates a cryptographically secure random password
// of the specified byte length, encoded as URL-safe base64.
func generateSecurePassword(numBytes int) (string, error) {
	b := make([]byte, numBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// runContainerValidation checks that all expected workspace files and
// directories exist after the setup phase for Claude provider.
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

	// Check 3: skills installed in <workspace>/.claude/skills/ (only if skills were configured).
	if skillsConfigured {
		checks = append(checks, checkSkillsDir(claudeDir)...)
	}

	return checks
}

// runOpenCodeContainerValidation checks that all expected workspace files exist
// for the OpenCode provider layout.
func runOpenCodeContainerValidation(workDir, claudeDir string, skillsConfigured, subAgentsConfigured bool) []protocol.ValidationCheck {
	var checks []protocol.ValidationCheck

	// Check 1: AGENTS.MD must exist in .opencode/.
	opencodeDir := filepath.Join(workDir, ".opencode")
	agentsMDPath := filepath.Join(opencodeDir, "AGENTS.MD")
	if _, err := os.Stat(agentsMDPath); err != nil {
		checks = append(checks, protocol.ValidationCheck{
			Name:    "agents_md",
			Status:  protocol.ValidationError,
			Message: fmt.Sprintf("AGENTS.MD not found at %s", agentsMDPath),
		})
	} else {
		checks = append(checks, protocol.ValidationCheck{
			Name:    "agents_md",
			Status:  protocol.ValidationOK,
			Message: "AGENTS.MD exists",
		})
	}

	// Check 2: .opencode/agents/ directory has files (only if sub-agents were configured).
	agentsDir := filepath.Join(opencodeDir, "agents")
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

	// Check 3: skills installed in .claude/skills/ (skills always go to .claude/).
	if skillsConfigured {
		checks = append(checks, checkSkillsDir(claudeDir)...)
	}

	return checks
}

// checkSkillsDir validates that the skills directory exists and has content.
func checkSkillsDir(claudeDir string) []protocol.ValidationCheck {
	skillsDir := filepath.Join(claudeDir, "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil || len(entries) == 0 {
		return []protocol.ValidationCheck{{
			Name:    "skills_installed",
			Status:  protocol.ValidationWarning,
			Message: fmt.Sprintf("no installed skill packages found in %s", skillsDir),
		}}
	}
	return []protocol.ValidationCheck{{
		Name:    "skills_installed",
		Status:  protocol.ValidationOK,
		Message: fmt.Sprintf("%d skill package(s) installed in %s", len(entries), skillsDir),
	}}
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
