package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/helmcode/agent-crew/internal/models"
	"github.com/helmcode/agent-crew/internal/runtime"
)

// ListTeams returns all teams.
func (s *Server) ListTeams(c *fiber.Ctx) error {
	var teams []models.Team
	if err := s.db.Preload("Agents").Find(&teams).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to list teams")
	}
	return c.JSON(teams)
}

// GetTeam returns a single team by ID.
func (s *Server) GetTeam(c *fiber.Ctx) error {
	id := c.Params("id")
	var team models.Team
	if err := s.db.Preload("Agents").First(&team, "id = ?", id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "team not found")
	}
	return c.JSON(team)
}

// CreateTeam creates a new team with optional agents.
func (s *Server) CreateTeam(c *fiber.Ctx) error {
	var req CreateTeamRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	if req.Name == "" {
		return fiber.NewError(fiber.StatusBadRequest, "name is required")
	}
	if err := validateName(req.Name); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}

	rt := req.Runtime
	if rt == "" {
		rt = "docker"
	}

	team := models.Team{
		ID:            uuid.New().String(),
		Name:          req.Name,
		Description:   req.Description,
		Status:        models.TeamStatusStopped,
		Runtime:       rt,
		WorkspacePath: req.WorkspacePath,
	}

	// Create agents if provided.
	for _, a := range req.Agents {
		if a.Name != "" {
			if err := validateName(a.Name); err != nil {
				return fiber.NewError(fiber.StatusBadRequest, "agent "+a.Name+": "+err.Error())
			}
		}
		role := a.Role
		if role == "" {
			role = models.AgentRoleWorker
		}
		skills, _ := json.Marshal(a.Skills)
		perms, _ := json.Marshal(a.Permissions)
		resources, _ := json.Marshal(a.Resources)

		team.Agents = append(team.Agents, models.Agent{
			ID:           uuid.New().String(),
			Name:         a.Name,
			Role:         role,
			Specialty:    a.Specialty,
			SystemPrompt: a.SystemPrompt,
			Skills:       models.JSON(skills),
			Permissions:  models.JSON(perms),
			Resources:    models.JSON(resources),
		})
	}

	if err := s.db.Create(&team).Error; err != nil {
		return fiber.NewError(fiber.StatusConflict, "team name already exists")
	}

	return c.Status(fiber.StatusCreated).JSON(team)
}

// UpdateTeam updates a team's metadata.
func (s *Server) UpdateTeam(c *fiber.Ctx) error {
	id := c.Params("id")
	var team models.Team
	if err := s.db.First(&team, "id = ?", id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "team not found")
	}

	var req UpdateTeamRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	updates := map[string]interface{}{}
	if req.Name != nil {
		if err := validateName(*req.Name); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		updates["name"] = *req.Name
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.WorkspacePath != nil {
		updates["workspace_path"] = *req.WorkspacePath
	}

	if len(updates) > 0 {
		if err := s.db.Model(&team).Updates(updates).Error; err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "failed to update team")
		}
	}

	s.db.Preload("Agents").First(&team, "id = ?", id)
	return c.JSON(team)
}

// DeleteTeam removes a team and cascades to agents.
func (s *Server) DeleteTeam(c *fiber.Ctx) error {
	id := c.Params("id")
	var team models.Team
	if err := s.db.First(&team, "id = ?", id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "team not found")
	}

	if team.Status == models.TeamStatusRunning {
		return fiber.NewError(fiber.StatusConflict, "stop the team before deleting")
	}

	if err := s.db.Select("Agents").Delete(&team).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to delete team")
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// DeployTeam deploys team infrastructure and all agents.
func (s *Server) DeployTeam(c *fiber.Ctx) error {
	id := c.Params("id")
	var team models.Team
	if err := s.db.Preload("Agents").First(&team, "id = ?", id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "team not found")
	}

	if team.Status == models.TeamStatusRunning {
		return fiber.NewError(fiber.StatusConflict, "team is already running")
	}

	// Update status to deploying.
	s.db.Model(&team).Update("status", models.TeamStatusDeploying)

	// Deep copy agents for the background goroutine to avoid data races
	// with the JSON serialization of the response below.
	asyncTeam := team
	asyncTeam.Agents = make([]models.Agent, len(team.Agents))
	copy(asyncTeam.Agents, team.Agents)

	// Deploy in background.
	go s.deployTeamAsync(asyncTeam)

	team.Status = models.TeamStatusDeploying
	return c.JSON(team)
}

func (s *Server) deployTeamAsync(team models.Team) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic in deployTeamAsync", "team", team.Name, "panic", r)
			s.db.Model(&team).Update("status", models.TeamStatusError)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Load settings from DB to pass as environment variables to agent containers.
	envFromSettings := s.loadSettingsEnv()

	// Deploy infrastructure.
	infraCfg := runtime.InfraConfig{
		TeamName:      team.Name,
		NATSEnabled:   true,
		WorkspacePath: team.WorkspacePath,
	}

	if err := s.runtime.DeployInfra(ctx, infraCfg); err != nil {
		slog.Error("failed to deploy infrastructure", "team", team.Name, "error", err)
		s.db.Model(&team).Update("status", models.TeamStatusError)
		return
	}

	natsURL := s.runtime.GetNATSURL(team.Name)

	// Deploy each agent.
	var failedAgents int
	for i := range team.Agents {
		agent := &team.Agents[i]

		var perms json.RawMessage
		if len(agent.Permissions) > 0 {
			perms = json.RawMessage(agent.Permissions)
		}
		var res runtime.ResourceConfig
		if len(agent.Resources) > 0 {
			_ = json.Unmarshal(agent.Resources, &res)
		}
		_ = perms // permissions parsed by runtime

		agentCfg := runtime.AgentConfig{
			Name:          agent.Name,
			TeamName:      team.Name,
			Role:          agent.Role,
			SystemPrompt:  agent.SystemPrompt,
			Resources:     res,
			NATSUrl:       natsURL,
			WorkspacePath: team.WorkspacePath,
			Env:           envFromSettings,
		}

		instance, err := s.runtime.DeployAgent(ctx, agentCfg)
		if err != nil {
			slog.Error("failed to deploy agent", "agent", agent.Name, "error", err)
			s.db.Model(agent).Updates(map[string]interface{}{
				"container_status": models.ContainerStatusError,
			})
			failedAgents++
			continue
		}

		s.db.Model(agent).Updates(map[string]interface{}{
			"container_id":     instance.ID,
			"container_status": models.ContainerStatusRunning,
		})
	}

	if failedAgents > 0 {
		slog.Error("team deployed with errors", "team", team.Name, "failed_agents", failedAgents, "total_agents", len(team.Agents))
		s.db.Model(&team).Update("status", models.TeamStatusError)
		return
	}

	s.db.Model(&team).Update("status", models.TeamStatusRunning)
	slog.Info("team deployed successfully", "team", team.Name)

	// Start relay goroutine: subscribes to team NATS and saves agent
	// responses as TaskLogs so StreamActivity WebSocket delivers them to UI.
	s.startTeamRelay(team.ID, team.Name)
}

// loadSettingsEnv reads known settings from the database and returns them as a
// string map suitable for passing to AgentConfig.Env.
func (s *Server) loadSettingsEnv() map[string]string {
	env := make(map[string]string)

	// Keys that should be forwarded to agent containers.
	keys := []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"}

	for _, key := range keys {
		var setting models.Settings
		if err := s.db.Where("key = ?", key).First(&setting).Error; err == nil && setting.Value != "" {
			env[key] = setting.Value
		}
	}

	return env
}

// StopTeam tears down all team infrastructure.
func (s *Server) StopTeam(c *fiber.Ctx) error {
	id := c.Params("id")
	var team models.Team
	if err := s.db.Preload("Agents").First(&team, "id = ?", id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "team not found")
	}

	if team.Status != models.TeamStatusRunning && team.Status != models.TeamStatusError {
		return fiber.NewError(fiber.StatusConflict, "team is not running")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := s.runtime.TeardownInfra(ctx, team.Name); err != nil {
		slog.Error("failed to teardown infrastructure", "team", team.Name, "error", err)
	}

	// Update all agents to stopped.
	for i := range team.Agents {
		s.db.Model(&team.Agents[i]).Updates(map[string]interface{}{
			"container_id":     "",
			"container_status": models.ContainerStatusStopped,
		})
	}

	// Stop the relay goroutine for this team.
	s.stopTeamRelay(team.ID)

	s.db.Model(&team).Update("status", models.TeamStatusStopped)
	team.Status = models.TeamStatusStopped
	return c.JSON(team)
}
