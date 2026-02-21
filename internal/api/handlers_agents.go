package api

import (
	"encoding/json"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/helmcode/agent-crew/internal/models"
)

// ListAgents returns all agents for a team.
func (s *Server) ListAgents(c *fiber.Ctx) error {
	teamID := c.Params("id")

	// Verify team exists.
	var team models.Team
	if err := s.db.First(&team, "id = ?", teamID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "team not found")
	}

	var agents []models.Agent
	if err := s.db.Where("team_id = ?", teamID).Find(&agents).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to list agents")
	}
	return c.JSON(agents)
}

// GetAgent returns a single agent.
func (s *Server) GetAgent(c *fiber.Ctx) error {
	teamID := c.Params("id")
	agentID := c.Params("agentId")

	var agent models.Agent
	if err := s.db.Where("id = ? AND team_id = ?", agentID, teamID).First(&agent).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "agent not found")
	}
	return c.JSON(agent)
}

// CreateAgent adds a new agent to a team.
func (s *Server) CreateAgent(c *fiber.Ctx) error {
	teamID := c.Params("id")

	var team models.Team
	if err := s.db.First(&team, "id = ?", teamID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "team not found")
	}

	var req CreateAgentRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	if req.Name == "" {
		return fiber.NewError(fiber.StatusBadRequest, "name is required")
	}
	if err := validateName(req.Name); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}

	role := req.Role
	if role == "" {
		role = models.AgentRoleWorker
	}
	if role != models.AgentRoleLeader && role != models.AgentRoleWorker {
		return fiber.NewError(fiber.StatusBadRequest, "role must be 'leader' or 'worker'")
	}

	if req.SubAgentModel != "" && !isValidSubAgentModel(req.SubAgentModel) {
		return fiber.NewError(fiber.StatusBadRequest, "sub_agent_model must be one of: inherit, sonnet, opus, haiku")
	}
	if req.SubAgentPermissionMode != "" && !isValidSubAgentPermissionMode(req.SubAgentPermissionMode) {
		return fiber.NewError(fiber.StatusBadRequest, "sub_agent_permission_mode must be one of: default, acceptEdits, bypassPermissions")
	}

	skills, _ := json.Marshal(req.Skills)
	perms, _ := json.Marshal(req.Permissions)
	resources, _ := json.Marshal(req.Resources)

	subAgentModel := req.SubAgentModel
	if subAgentModel == "" {
		subAgentModel = "inherit"
	}

	agent := models.Agent{
		ID:                     uuid.New().String(),
		TeamID:                 teamID,
		Name:                   req.Name,
		Role:                   role,
		Specialty:              req.Specialty,
		SystemPrompt:           req.SystemPrompt,
		ClaudeMD:               req.ClaudeMD,
		Skills:                 models.JSON(skills),
		Permissions:            models.JSON(perms),
		Resources:              models.JSON(resources),
		SubAgentDescription:    req.SubAgentDescription,
		SubAgentTools:          req.SubAgentTools,
		SubAgentModel:          subAgentModel,
		SubAgentPermissionMode: req.SubAgentPermissionMode,
	}

	if err := s.db.Create(&agent).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to create agent")
	}

	return c.Status(fiber.StatusCreated).JSON(agent)
}

// UpdateAgent updates an agent's configuration.
func (s *Server) UpdateAgent(c *fiber.Ctx) error {
	teamID := c.Params("id")
	agentID := c.Params("agentId")

	var agent models.Agent
	if err := s.db.Where("id = ? AND team_id = ?", agentID, teamID).First(&agent).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "agent not found")
	}

	var req UpdateAgentRequest
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
	if req.Role != nil {
		updates["role"] = *req.Role
	}
	if req.Specialty != nil {
		updates["specialty"] = *req.Specialty
	}
	if req.SystemPrompt != nil {
		updates["system_prompt"] = *req.SystemPrompt
	}
	if req.ClaudeMD != nil {
		updates["claude_md"] = *req.ClaudeMD
	}
	if req.Skills != nil {
		raw, _ := json.Marshal(req.Skills)
		updates["skills"] = models.JSON(raw)
	}
	if req.Permissions != nil {
		raw, _ := json.Marshal(req.Permissions)
		updates["permissions"] = models.JSON(raw)
	}
	if req.Resources != nil {
		raw, _ := json.Marshal(req.Resources)
		updates["resources"] = models.JSON(raw)
	}
	if req.SubAgentDescription != nil {
		updates["sub_agent_description"] = *req.SubAgentDescription
	}
	if req.SubAgentTools != nil {
		updates["sub_agent_tools"] = *req.SubAgentTools
	}
	if req.SubAgentModel != nil {
		if *req.SubAgentModel != "" && !isValidSubAgentModel(*req.SubAgentModel) {
			return fiber.NewError(fiber.StatusBadRequest, "sub_agent_model must be one of: inherit, sonnet, opus, haiku")
		}
		updates["sub_agent_model"] = *req.SubAgentModel
	}
	if req.SubAgentPermissionMode != nil {
		if *req.SubAgentPermissionMode != "" && !isValidSubAgentPermissionMode(*req.SubAgentPermissionMode) {
			return fiber.NewError(fiber.StatusBadRequest, "sub_agent_permission_mode must be one of: default, acceptEdits, bypassPermissions")
		}
		updates["sub_agent_permission_mode"] = *req.SubAgentPermissionMode
	}

	if len(updates) > 0 {
		if err := s.db.Model(&agent).Updates(updates).Error; err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "failed to update agent")
		}
	}

	s.db.Where("id = ? AND team_id = ?", agentID, teamID).First(&agent)
	return c.JSON(agent)
}

// isValidSubAgentModel returns true if v is a recognized Claude Code model value.
func isValidSubAgentModel(v string) bool {
	switch v {
	case "inherit", "sonnet", "opus", "haiku":
		return true
	}
	return false
}

// isValidSubAgentPermissionMode returns true if v is a recognized Claude Code permission mode.
func isValidSubAgentPermissionMode(v string) bool {
	switch v {
	case "default", "acceptEdits", "bypassPermissions":
		return true
	}
	return false
}

// DeleteAgent removes an agent from a team.
func (s *Server) DeleteAgent(c *fiber.Ctx) error {
	teamID := c.Params("id")
	agentID := c.Params("agentId")

	var agent models.Agent
	if err := s.db.Where("id = ? AND team_id = ?", agentID, teamID).First(&agent).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "agent not found")
	}

	if agent.ContainerStatus == models.ContainerStatusRunning {
		return fiber.NewError(fiber.StatusConflict, "stop the agent before deleting")
	}

	if err := s.db.Delete(&agent).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to delete agent")
	}

	return c.SendStatus(fiber.StatusNoContent)
}
