package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/helmcode/agent-crew/internal/claude"
	agentNats "github.com/helmcode/agent-crew/internal/nats"
	"github.com/helmcode/agent-crew/internal/permissions"
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

	// 4. Write CLAUDE.md if content was passed via env var.
	// This ensures agents get their CLAUDE.md even when the API server
	// runs inside Docker and cannot write to the host workspace path.
	workDir := os.Getenv("WORKSPACE_PATH")
	if workDir == "" {
		workDir = "/workspace"
	}
	if claudeMD := os.Getenv("AGENT_CLAUDE_MD"); claudeMD != "" {
		claudeDir := workDir + "/.claude"
		if err := os.MkdirAll(claudeDir, 0755); err != nil {
			slog.Warn("failed to create .claude dir", "error", err)
		} else if err := os.WriteFile(claudeDir+"/CLAUDE.md", []byte(claudeMD), 0644); err != nil {
			slog.Warn("failed to write CLAUDE.md", "error", err)
		} else {
			slog.Info("wrote CLAUDE.md from env var", "path", claudeDir+"/CLAUDE.md")
		}
	}

	// 5. Start Claude Manager.
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

	// 6. Start Bridge (NATS <-> Claude stdin/stdout).
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
