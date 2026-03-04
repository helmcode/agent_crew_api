package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	agentNats "github.com/helmcode/agent-crew/internal/nats"
	"github.com/helmcode/agent-crew/internal/protocol"
)

// writeMcpConfig reads AGENT_MCP_SERVERS env var, validates the servers,
// generates the provider-specific MCP config file, writes it to disk,
// and publishes an mcp_status message via NATS.
func writeMcpConfig(workDir, providerName string, natsClient *agentNats.Client, agentName, teamName string) {
	serversEnv := os.Getenv("AGENT_MCP_SERVERS")
	if serversEnv == "" {
		return
	}

	var servers []protocol.McpServerConfig
	if err := json.Unmarshal([]byte(serversEnv), &servers); err != nil {
		slog.Warn("failed to parse AGENT_MCP_SERVERS", "error", err)
		return
	}

	if len(servers) == 0 {
		return
	}

	// Validate each server (second line of defense after API validation).
	var statuses []protocol.McpServerStatus
	var validServers []protocol.McpServerConfig
	for _, srv := range servers {
		if err := validateMcpServer(srv); err != nil {
			slog.Warn("invalid MCP server config, skipping", "name", srv.Name, "error", err)
			statuses = append(statuses, protocol.McpServerStatus{
				Name:   srv.Name,
				Status: "error",
				Error:  err.Error(),
			})
			continue
		}
		validServers = append(validServers, srv)
		statuses = append(statuses, protocol.McpServerStatus{
			Name:   srv.Name,
			Status: "configured",
		})
	}

	if len(validServers) == 0 {
		slog.Warn("no valid MCP servers after validation")
		publishMcpStatus(natsClient, agentName, teamName, statuses)
		return
	}

	// Generate and write config file based on provider.
	var configPath string
	var content []byte

	switch providerName {
	case "opencode":
		configPath = filepath.Join(workDir, "opencode.json")
		content = generateOpenCodeMcpConfig(configPath, validServers)
	default:
		configPath = filepath.Join(workDir, ".mcp.json")
		content = generateClaudeMcpConfig(validServers)
	}

	if err := os.WriteFile(configPath, content, 0644); err != nil {
		slog.Error("failed to write MCP config file", "path", configPath, "error", err)
		// Mark all as error.
		for i := range statuses {
			if statuses[i].Status == "configured" {
				statuses[i].Status = "error"
				statuses[i].Error = "failed to write config file: " + err.Error()
			}
		}
	} else {
		slog.Info("wrote MCP config file", "path", configPath, "servers", len(validServers))

		// Pre-warm stdio MCP servers so package managers (uvx, npx) cache
		// dependencies before the agent CLI starts. Without this, the first
		// MCP server launch inside Claude Code / OpenCode can timeout while
		// downloading and compiling packages.
		warmMcpServers(validServers, statuses)
	}

	publishMcpStatus(natsClient, agentName, teamName, statuses)
}

// generateClaudeMcpConfig produces .mcp.json content for Claude Code.
//
// Format:
//
//	{
//	  "mcpServers": {
//	    "server-name": {
//	      "command": "npx",
//	      "args": ["-y", "@modelcontextprotocol/server-postgres"],
//	      "env": { "DATABASE_URL": "..." }
//	    }
//	  }
//	}
func generateClaudeMcpConfig(servers []protocol.McpServerConfig) []byte {
	mcpServers := make(map[string]interface{})
	for _, srv := range servers {
		entry := make(map[string]interface{})
		switch srv.Transport {
		case "stdio":
			entry["command"] = srv.Command
			if len(srv.Args) > 0 {
				entry["args"] = srv.Args
			}
			if len(srv.Env) > 0 {
				entry["env"] = srv.Env
			}
		case "http":
			entry["type"] = "http"
			entry["url"] = srv.URL
			if len(srv.Headers) > 0 {
				entry["headers"] = srv.Headers
			}
		case "sse":
			entry["url"] = srv.URL
			if len(srv.Headers) > 0 {
				entry["headers"] = srv.Headers
			}
		}
		mcpServers[srv.Name] = entry
	}

	result := map[string]interface{}{
		"mcpServers": mcpServers,
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	_ = enc.Encode(result)
	return buf.Bytes()
}

// generateOpenCodeMcpConfig produces opencode.json content for OpenCode.
// If an existing opencode.json exists at the path, it merges MCP servers
// into the existing config. Otherwise, creates a new config.
//
// Format:
//
//	{
//	  "$schema": "https://opencode.ai/config.json",
//	  "mcp": {
//	    "server-name": {
//	      "type": "local",
//	      "command": ["npx", "-y", "@modelcontextprotocol/server-postgres"],
//	      "enabled": true,
//	      "environment": { "DATABASE_URL": "..." }
//	    }
//	  }
//	}
func generateOpenCodeMcpConfig(existingPath string, servers []protocol.McpServerConfig) []byte {
	// Try to read existing config to merge.
	existing := make(map[string]interface{})
	if data, err := os.ReadFile(existingPath); err == nil {
		_ = json.Unmarshal(data, &existing)
	}

	// Build MCP section.
	mcp := make(map[string]interface{})
	for _, srv := range servers {
		entry := map[string]interface{}{
			"enabled": true,
		}
		switch srv.Transport {
		case "stdio":
			entry["type"] = "local"
			cmd := []string{srv.Command}
			cmd = append(cmd, srv.Args...)
			entry["command"] = cmd
			if len(srv.Env) > 0 {
				entry["environment"] = srv.Env
			}
		case "http", "sse":
			entry["type"] = "remote"
			entry["url"] = srv.URL
			if len(srv.Headers) > 0 {
				entry["headers"] = srv.Headers
			}
		}
		mcp[srv.Name] = entry
	}

	// Merge into existing config.
	if _, ok := existing["$schema"]; !ok {
		existing["$schema"] = "https://opencode.ai/config.json"
	}
	existing["mcp"] = mcp

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	_ = enc.Encode(existing)
	return buf.Bytes()
}

// validateMcpServer performs sidecar-level validation of a single MCP server config.
func validateMcpServer(srv protocol.McpServerConfig) error {
	if srv.Name == "" {
		return fmt.Errorf("name is required")
	}

	shellMeta := ";|&$`\\\"'<>(){}!"
	switch srv.Transport {
	case "stdio":
		if srv.Command == "" {
			return fmt.Errorf("command is required for stdio transport")
		}
		if strings.ContainsAny(srv.Command, shellMeta) {
			return fmt.Errorf("command contains unsafe shell characters")
		}
		for _, arg := range srv.Args {
			if strings.ContainsAny(arg, shellMeta) {
				return fmt.Errorf("argument contains unsafe shell characters")
			}
		}
	case "http", "sse":
		if srv.URL == "" {
			return fmt.Errorf("url is required for %s transport", srv.Transport)
		}
		u, err := url.Parse(srv.URL)
		if err != nil {
			return fmt.Errorf("invalid url: %w", err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("url must use http or https scheme")
		}
	default:
		return fmt.Errorf("unsupported transport: %s", srv.Transport)
	}

	return nil
}

// warmMcpServers pre-warms stdio MCP servers by running their commands once.
// This triggers package downloads and compilation (e.g. uvx installs Python
// packages, npx downloads npm packages) so they are cached before the agent
// CLI starts. HTTP/SSE servers are skipped as they don't need local setup.
func warmMcpServers(servers []protocol.McpServerConfig, statuses []protocol.McpServerStatus) {
	for _, srv := range servers {
		if srv.Transport != "stdio" {
			continue
		}
		if err := warmStdioServer(srv); err != nil {
			slog.Warn("MCP server pre-warming failed (agent may timeout on first use)",
				"name", srv.Name, "error", err)
			// Update the corresponding status with a warning.
			for i := range statuses {
				if statuses[i].Name == srv.Name && statuses[i].Status == "configured" {
					statuses[i].Error = "pre-warming failed: " + err.Error()
				}
			}
		}
	}
}

// warmStdioServer runs a single stdio MCP server command with --help to trigger
// package manager caching. Uses a 3-minute timeout to accommodate first-time
// downloads and C extension compilation (e.g. pglast via uvx postgres-mcp).
func warmStdioServer(srv protocol.McpServerConfig) error {
	args := make([]string, len(srv.Args))
	copy(args, srv.Args)
	args = append(args, "--help")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, srv.Command, args...)
	cmd.Env = os.Environ()
	for k, v := range srv.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	slog.Info("pre-warming MCP server", "name", srv.Name, "command", srv.Command, "args", args)
	start := time.Now()

	output, err := cmd.CombinedOutput()
	elapsed := time.Since(start).Round(time.Millisecond)

	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("timed out after %s (packages may not be fully cached)", elapsed)
	}

	// A non-zero exit code is acceptable — the goal is to trigger the
	// download/compilation, not to run the server successfully.
	if err != nil {
		slog.Info("MCP server pre-warming finished with non-zero exit (expected for --help)",
			"name", srv.Name, "elapsed", elapsed, "error", err,
			"output_tail", truncateTail(string(output), 500))
	} else {
		slog.Info("MCP server pre-warming completed", "name", srv.Name, "elapsed", elapsed)
	}

	return nil
}

// truncateTail returns the last n bytes of a string, useful for logging.
func truncateTail(s string, n int) string {
	if len(s) <= n {
		return strings.TrimSpace(s)
	}
	return "..." + strings.TrimSpace(s[len(s)-n:])
}

// publishMcpStatus publishes MCP server status to the team activity NATS channel.
func publishMcpStatus(client *agentNats.Client, agentName, teamName string, statuses []protocol.McpServerStatus) {
	configured := 0
	errors := 0
	for _, s := range statuses {
		if s.Status == "configured" {
			configured++
		} else {
			errors++
		}
	}
	summary := fmt.Sprintf("%d configured, %d error(s)", configured, errors)

	slog.Info("MCP status", "summary", summary)
	for _, s := range statuses {
		slog.Info("MCP server", "name", s.Name, "status", s.Status, "error", s.Error)
	}

	payload := protocol.McpStatusPayload{
		AgentName: agentName,
		Servers:   statuses,
		Summary:   summary,
	}

	msg, err := protocol.NewMessage(agentName, "system", protocol.TypeMcpStatus, payload)
	if err != nil {
		slog.Error("failed to create MCP status message", "error", err)
		return
	}

	subject, err := protocol.TeamActivityChannel(teamName)
	if err != nil {
		slog.Error("failed to build activity channel for MCP status", "error", err)
		return
	}

	if err := client.Publish(subject, msg); err != nil {
		slog.Error("failed to publish MCP status", "error", err)
	}
}
