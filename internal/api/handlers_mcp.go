package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/helmcode/agent-crew/internal/models"
	"github.com/helmcode/agent-crew/internal/protocol"
)

// GetMcpConfig reads the MCP config file from a running team's leader container.
func (s *Server) GetMcpConfig(c *fiber.Ctx) error {
	id := c.Params("id")
	var team models.Team
	if err := s.db.Scopes(OrgScope(c)).Preload("Agents").First(&team, "id = ?", id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "team not found")
	}

	if team.Status != models.TeamStatusRunning {
		// Return DB-stored config if team is not running.
		if len(team.McpServers) == 0 || string(team.McpServers) == "null" || string(team.McpServers) == "[]" {
			return c.JSON(McpConfigResponse{Content: "[]", Path: "", Provider: team.Provider})
		}
		return c.JSON(McpConfigResponse{
			Content:  string(team.McpServers),
			Path:     "",
			Provider: team.Provider,
		})
	}

	// Find the leader container.
	var leader *models.Agent
	for i := range team.Agents {
		if team.Agents[i].Role == models.AgentRoleLeader {
			leader = &team.Agents[i]
			break
		}
	}
	if leader == nil || leader.ContainerID == "" {
		return fiber.NewError(fiber.StatusNotFound, "leader container not found")
	}

	// Determine config file path based on provider.
	var configPath string
	if team.Provider == models.ProviderOpenCode {
		configPath = "/workspace/opencode.json"
	} else {
		configPath = "/workspace/.mcp.json"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	output, err := s.runtime.ExecInContainer(ctx, leader.ContainerID, []string{"cat", configPath})
	if err != nil {
		// File may not exist yet — return empty.
		return c.JSON(McpConfigResponse{
			Content:  "",
			Path:     configPath,
			Provider: team.Provider,
		})
	}

	return c.JSON(McpConfigResponse{
		Content:  strings.TrimSpace(output),
		Path:     configPath,
		Provider: team.Provider,
	})
}

// UpdateMcpConfig writes raw JSON to the MCP config file in a running container.
func (s *Server) UpdateMcpConfig(c *fiber.Ctx) error {
	id := c.Params("id")
	var team models.Team
	if err := s.db.Scopes(OrgScope(c)).Preload("Agents").First(&team, "id = ?", id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "team not found")
	}

	var req UpdateMcpConfigRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	// Validate that content is valid JSON.
	if !json.Valid([]byte(req.Content)) {
		return fiber.NewError(fiber.StatusBadRequest, "content must be valid JSON")
	}

	// Try to extract server configs from the raw JSON to sync with DB.
	servers := extractServersFromRawConfig(req.Content, team.Provider)
	if servers != nil {
		serversJSON, _ := json.Marshal(servers)
		s.db.Model(&team).Update("mcp_servers", models.JSON(serversJSON))
	}

	if team.Status != models.TeamStatusRunning {
		return c.JSON(fiber.Map{"status": "saved"})
	}

	// Find leader container.
	var leader *models.Agent
	for i := range team.Agents {
		if team.Agents[i].Role == models.AgentRoleLeader {
			leader = &team.Agents[i]
			break
		}
	}
	if leader == nil || leader.ContainerID == "" {
		return fiber.NewError(fiber.StatusNotFound, "leader container not found")
	}

	var configPath string
	if team.Provider == models.ProviderOpenCode {
		configPath = "/workspace/opencode.json"
	} else {
		configPath = "/workspace/.mcp.json"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Write content via heredoc (safer than echo + shell escaping).
	_, err := s.runtime.ExecInContainer(ctx, leader.ContainerID, []string{"sh", "-c", fmt.Sprintf("cat > %s << 'MCPEOF'\n%s\nMCPEOF", configPath, req.Content)})
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to write MCP config: "+err.Error())
	}

	return c.JSON(fiber.Map{"status": "written", "path": configPath})
}

// AddMcpServer adds a new MCP server to a team's configuration.
func (s *Server) AddMcpServer(c *fiber.Ctx) error {
	id := c.Params("id")
	var team models.Team
	if err := s.db.Scopes(OrgScope(c)).Preload("Agents").First(&team, "id = ?", id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "team not found")
	}

	var req AddMcpServerRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	// Validate via the common validator.
	newServer := map[string]interface{}{
		"name": req.Name, "transport": req.Transport,
		"command": req.Command, "args": req.Args,
		"env": req.Env, "url": req.URL, "headers": req.Headers,
	}
	if err := validateMcpServers([]interface{}{newServer}); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}

	// Parse existing servers.
	var existing []protocol.McpServerConfig
	if len(team.McpServers) > 0 && string(team.McpServers) != "null" {
		_ = json.Unmarshal(team.McpServers, &existing)
	}

	// Check for duplicate name.
	for _, srv := range existing {
		if strings.EqualFold(srv.Name, req.Name) {
			return fiber.NewError(fiber.StatusConflict, "MCP server with this name already exists")
		}
	}

	// Append new server.
	server := protocol.McpServerConfig{
		Name:      req.Name,
		Transport: req.Transport,
		Command:   req.Command,
		Args:      req.Args,
		Env:       req.Env,
		URL:       req.URL,
		Headers:   req.Headers,
	}
	existing = append(existing, server)

	data, _ := json.Marshal(existing)
	team.McpServers = models.JSON(data)
	s.db.Model(&team).Update("mcp_servers", team.McpServers)

	// If team is running, regenerate the config file in the container.
	if team.Status == models.TeamStatusRunning {
		s.writeMcpConfigToContainer(team)
	}

	// Return updated team.
	s.db.Scopes(OrgScope(c)).Preload("Agents").First(&team, "id = ?", id)
	return c.Status(fiber.StatusCreated).JSON(team)
}

// RemoveMcpServer removes an MCP server from a team's configuration.
func (s *Server) RemoveMcpServer(c *fiber.Ctx) error {
	id := c.Params("id")
	serverName := c.Params("serverName")

	var team models.Team
	if err := s.db.Scopes(OrgScope(c)).Preload("Agents").First(&team, "id = ?", id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "team not found")
	}

	var existing []protocol.McpServerConfig
	if len(team.McpServers) > 0 && string(team.McpServers) != "null" {
		_ = json.Unmarshal(team.McpServers, &existing)
	}

	found := false
	filtered := make([]protocol.McpServerConfig, 0, len(existing))
	for _, srv := range existing {
		if strings.EqualFold(srv.Name, serverName) {
			found = true
			continue
		}
		filtered = append(filtered, srv)
	}

	if !found {
		return fiber.NewError(fiber.StatusNotFound, "MCP server not found")
	}

	data, _ := json.Marshal(filtered)
	team.McpServers = models.JSON(data)
	s.db.Model(&team).Update("mcp_servers", team.McpServers)

	// Update statuses too — remove the status for the removed server.
	var statuses []protocol.McpServerStatus
	if len(team.McpStatuses) > 0 && string(team.McpStatuses) != "null" {
		_ = json.Unmarshal(team.McpStatuses, &statuses)
	}
	filteredStatuses := make([]protocol.McpServerStatus, 0)
	for _, st := range statuses {
		if !strings.EqualFold(st.Name, serverName) {
			filteredStatuses = append(filteredStatuses, st)
		}
	}
	statusData, _ := json.Marshal(filteredStatuses)
	team.McpStatuses = models.JSON(statusData)
	s.db.Model(&team).Update("mcp_statuses", team.McpStatuses)

	// If team is running, regenerate config file.
	if team.Status == models.TeamStatusRunning {
		s.writeMcpConfigToContainer(team)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// writeMcpConfigToContainer regenerates the MCP config file inside the leader container.
func (s *Server) writeMcpConfigToContainer(team models.Team) {
	var leader *models.Agent
	for i := range team.Agents {
		if team.Agents[i].Role == models.AgentRoleLeader {
			leader = &team.Agents[i]
			break
		}
	}
	if leader == nil || leader.ContainerID == "" {
		return
	}

	var servers []protocol.McpServerConfig
	if len(team.McpServers) > 0 && string(team.McpServers) != "null" {
		_ = json.Unmarshal(team.McpServers, &servers)
	}

	var content []byte
	var configPath string
	if team.Provider == models.ProviderOpenCode {
		configPath = "/workspace/opencode.json"
		content = generateOpenCodeMcpJSON(servers)
	} else {
		configPath = "/workspace/.mcp.json"
		content = generateClaudeMcpJSON(servers)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := s.runtime.ExecInContainer(ctx, leader.ContainerID, []string{"sh", "-c", fmt.Sprintf("cat > %s << 'MCPEOF'\n%s\nMCPEOF", configPath, string(content))})
	if err != nil {
		slog.Error("failed to write MCP config to container", "team", team.Name, "error", err)
	}
}

// generateClaudeMcpJSON generates .mcp.json content for Claude Code.
func generateClaudeMcpJSON(servers []protocol.McpServerConfig) []byte {
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

// generateOpenCodeMcpJSON generates opencode.json MCP section for OpenCode.
func generateOpenCodeMcpJSON(servers []protocol.McpServerConfig) []byte {
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

	result := map[string]interface{}{
		"$schema": "https://opencode.ai/config.json",
		"mcp":     mcp,
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	_ = enc.Encode(result)
	return buf.Bytes()
}

// extractServersFromRawConfig parses a raw MCP config JSON and extracts server configs.
func extractServersFromRawConfig(content, provider string) []protocol.McpServerConfig {
	if provider == models.ProviderOpenCode {
		return extractFromOpenCodeConfig(content)
	}
	return extractFromClaudeConfig(content)
}

func extractFromClaudeConfig(content string) []protocol.McpServerConfig {
	var raw struct {
		McpServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return nil
	}

	var servers []protocol.McpServerConfig
	for name, data := range raw.McpServers {
		var entry struct {
			Type    string            `json:"type"`
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		}
		if err := json.Unmarshal(data, &entry); err != nil {
			continue
		}

		srv := protocol.McpServerConfig{Name: name}
		if entry.Type == "http" {
			srv.Transport = "http"
			srv.URL = entry.URL
			srv.Headers = entry.Headers
		} else if entry.URL != "" && entry.Command == "" {
			srv.Transport = "sse"
			srv.URL = entry.URL
			srv.Headers = entry.Headers
		} else {
			srv.Transport = "stdio"
			srv.Command = entry.Command
			srv.Args = entry.Args
			srv.Env = entry.Env
		}
		servers = append(servers, srv)
	}
	return servers
}

func extractFromOpenCodeConfig(content string) []protocol.McpServerConfig {
	var raw struct {
		Mcp map[string]json.RawMessage `json:"mcp"`
	}
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return nil
	}

	var servers []protocol.McpServerConfig
	for name, data := range raw.Mcp {
		var entry struct {
			Type        string            `json:"type"`
			Command     json.RawMessage   `json:"command"`
			URL         string            `json:"url"`
			Environment map[string]string `json:"environment"`
			Headers     map[string]string `json:"headers"`
		}
		if err := json.Unmarshal(data, &entry); err != nil {
			continue
		}

		srv := protocol.McpServerConfig{Name: name}
		if entry.Type == "remote" {
			srv.Transport = "http"
			srv.URL = entry.URL
			srv.Headers = entry.Headers
		} else {
			srv.Transport = "stdio"
			srv.Env = entry.Environment
			// Parse command array.
			var cmdArray []string
			if err := json.Unmarshal(entry.Command, &cmdArray); err == nil && len(cmdArray) > 0 {
				srv.Command = cmdArray[0]
				if len(cmdArray) > 1 {
					srv.Args = cmdArray[1:]
				}
			}
		}
		servers = append(servers, srv)
	}
	return servers
}

