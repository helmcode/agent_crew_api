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
		subAgentSkills, _ := json.Marshal(a.SubAgentSkills)

		subAgentModel := a.SubAgentModel
		if subAgentModel == "" {
			subAgentModel = "inherit"
		}

		team.Agents = append(team.Agents, models.Agent{
			ID:                  uuid.New().String(),
			Name:                a.Name,
			Role:                role,
			Specialty:           a.Specialty,
			SystemPrompt:        a.SystemPrompt,
			ClaudeMD:            a.ClaudeMD,
			Skills:              models.JSON(skills),
			Permissions:         models.JSON(perms),
			Resources:           models.JSON(resources),
			SubAgentDescription: a.SubAgentDescription,
			SubAgentModel:       subAgentModel,
			SubAgentSkills:      models.JSON(subAgentSkills),
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

	// Build team member list for the leader's CLAUDE.md.
	var teamMembers []runtime.TeamMemberInfo
	for _, a := range team.Agents {
		teamMembers = append(teamMembers, runtime.TeamMemberInfo{
			Name:      SanitizeName(a.Name),
			Role:      a.Role,
			Specialty: a.Specialty,
		})
	}

	// Setup workspace files for all agents and deploy only the leader container.
	// Non-leader agents are sub-agent files only — no containers.
	var leader *models.Agent
	for i := range team.Agents {
		agent := &team.Agents[i]

		// Build agent workspace info for CLAUDE.md generation.
		info := runtime.AgentWorkspaceInfo{
			Name:         agent.Name,
			Role:         agent.Role,
			Specialty:    agent.Specialty,
			SystemPrompt: agent.SystemPrompt,
			ClaudeMD:     agent.ClaudeMD,
			Skills:       json.RawMessage(agent.Skills),
		}
		// Give the leader the full team roster so it can delegate tasks.
		if agent.Role == models.AgentRoleLeader {
			info.TeamMembers = teamMembers
		}

		// Write workspace files to disk (works when API runs on host).
		if team.WorkspacePath != "" {
			if agent.Role == models.AgentRoleLeader {
				// Leader gets a CLAUDE.md at .claude/{name}/CLAUDE.md.
				if _, err := runtime.SetupAgentWorkspace(team.WorkspacePath, info); err != nil {
					slog.Error("failed to setup agent workspace", "agent", agent.Name, "error", err)
				}
			} else {
				// Non-leader agents get a sub-agent file at .claude/agents/{name}.md.
				subInfo := runtime.SubAgentInfo{
					Name:        agent.Name,
					Description: agent.SubAgentDescription,
					Model:       agent.SubAgentModel,
					Skills:      json.RawMessage(agent.SubAgentSkills),
					ClaudeMD:    agent.ClaudeMD,
				}
				if subInfo.ClaudeMD == "" {
					subInfo.ClaudeMD = runtime.GenerateClaudeMD(info)
				}
				if _, err := runtime.SetupSubAgentFile(team.WorkspacePath, subInfo); err != nil {
					slog.Error("failed to setup sub-agent file", "agent", agent.Name, "error", err)
				}
			}
		}

		if agent.Role == models.AgentRoleLeader {
			leader = agent
		}
	}

	// Only the leader gets a container. Sub-agents are file-based.
	if leader == nil {
		slog.Error("no leader agent found in team", "team", team.Name)
		s.db.Model(&team).Update("status", models.TeamStatusError)
		return
	}

	// Build leader workspace info for CLAUDE.md content.
	leaderInfo := runtime.AgentWorkspaceInfo{
		Name:         leader.Name,
		Role:         leader.Role,
		Specialty:    leader.Specialty,
		SystemPrompt: leader.SystemPrompt,
		ClaudeMD:     leader.ClaudeMD,
		Skills:       json.RawMessage(leader.Skills),
		TeamMembers:  teamMembers,
	}

	var res runtime.ResourceConfig
	if len(leader.Resources) > 0 {
		_ = json.Unmarshal(leader.Resources, &res)
	}

	claudeMDContent := leader.ClaudeMD
	if claudeMDContent == "" {
		claudeMDContent = runtime.GenerateClaudeMD(leaderInfo)
	}

	// Collect all unique skills from non-leader agents so the sidecar can
	// install them via `skills add` before the leader process starts.
	skillsSet := map[string]struct{}{}
	for _, a := range team.Agents {
		if a.Role == models.AgentRoleLeader {
			continue
		}
		var agentSkills []string
		if err := json.Unmarshal(a.SubAgentSkills, &agentSkills); err == nil {
			for _, s := range agentSkills {
				if s != "" {
					skillsSet[s] = struct{}{}
				}
			}
		}
	}
	var allSkills []string
	for s := range skillsSet {
		allSkills = append(allSkills, s)
	}
	skillsJSON, _ := json.Marshal(allSkills)

	// Merge settings env with skills install list.
	agentEnv := envFromSettings
	if len(allSkills) > 0 {
		agentEnv["AGENT_SKILLS_INSTALL"] = string(skillsJSON)
	}

	agentCfg := runtime.AgentConfig{
		Name:          leader.Name,
		TeamName:      team.Name,
		Role:          leader.Role,
		SystemPrompt:  leader.SystemPrompt,
		ClaudeMD:      claudeMDContent,
		Resources:     res,
		NATSUrl:       natsURL,
		WorkspacePath: team.WorkspacePath,
		Env:           agentEnv,
	}

	instance, err := s.runtime.DeployAgent(ctx, agentCfg)
	if err != nil {
		slog.Error("failed to deploy leader agent", "agent", leader.Name, "error", err)
		s.db.Model(leader).Updates(map[string]interface{}{
			"container_status": models.ContainerStatusError,
		})
		s.db.Model(&team).Update("status", models.TeamStatusError)
		return
	}

	s.db.Model(leader).Updates(map[string]interface{}{
		"container_id":     instance.ID,
		"container_status": models.ContainerStatusRunning,
	})

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

	// Primary keys forwarded directly to agent containers.
	keys := []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"}
	for _, key := range keys {
		var setting models.Settings
		if err := s.db.Where("key = ?", key).First(&setting).Error; err == nil && setting.Value != "" {
			env[key] = setting.Value
		}
	}

	// Aliases: map alternative key names users may have used in Settings.
	aliases := map[string]string{
		"ANTHROPIC_AUTH_TOKEN": "CLAUDE_CODE_OAUTH_TOKEN",
	}
	for alias, target := range aliases {
		if env[target] != "" {
			continue // already set from primary key
		}
		var setting models.Settings
		if err := s.db.Where("key = ?", alias).First(&setting).Error; err == nil && setting.Value != "" {
			env[target] = setting.Value
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

	// Clear container state for the leader agent only (non-leaders have no containers).
	for i := range team.Agents {
		if team.Agents[i].Role == models.AgentRoleLeader {
			s.db.Model(&team.Agents[i]).Updates(map[string]interface{}{
				"container_id":     "",
				"container_status": models.ContainerStatusStopped,
			})
			break
		}
	}

	// Stop the relay goroutine for this team.
	s.stopTeamRelay(team.ID)

	s.db.Model(&team).Update("status", models.TeamStatusStopped)
	team.Status = models.TeamStatusStopped
	return c.JSON(team)
}
