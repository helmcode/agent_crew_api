package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/helmcode/agent-crew/internal/models"
	"github.com/helmcode/agent-crew/internal/runtime"
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

	if req.SubAgentSkills != nil {
		if err := validateSubAgentSkills(req.SubAgentSkills); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
	}

	skills, _ := json.Marshal(req.Skills)
	perms, _ := json.Marshal(req.Permissions)
	resources, _ := json.Marshal(req.Resources)
	subAgentSkills, _ := json.Marshal(req.SubAgentSkills)

	subAgentModel := req.SubAgentModel
	if subAgentModel == "" {
		subAgentModel = "inherit"
	}

	agent := models.Agent{
		ID:                  uuid.New().String(),
		TeamID:              teamID,
		Name:                req.Name,
		Role:                role,
		Specialty:           req.Specialty,
		SystemPrompt:        req.SystemPrompt,
		ClaudeMD:            req.ClaudeMD,
		Skills:              models.JSON(skills),
		Permissions:         models.JSON(perms),
		Resources:           models.JSON(resources),
		SubAgentDescription: req.SubAgentDescription,
		SubAgentModel:       subAgentModel,
		SubAgentSkills:      models.JSON(subAgentSkills),
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
	if req.SubAgentModel != nil {
		if *req.SubAgentModel != "" && !isValidSubAgentModel(*req.SubAgentModel) {
			return fiber.NewError(fiber.StatusBadRequest, "sub_agent_model must be one of: inherit, sonnet, opus, haiku")
		}
		updates["sub_agent_model"] = *req.SubAgentModel
	}
	if req.SubAgentSkills != nil {
		if err := validateSubAgentSkills(req.SubAgentSkills); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		raw, _ := json.Marshal(req.SubAgentSkills)
		updates["sub_agent_skills"] = models.JSON(raw)
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

// InstallAgentSkill installs a skill into a running agent's container via exec,
// updates the agent's sub_agent_skills in the database, regenerates the worker's
// .md file in the container, and returns the updated skill list.
func (s *Server) InstallAgentSkill(c *fiber.Ctx) error {
	teamID := c.Params("id")
	agentID := c.Params("agentId")

	// Find team and verify it's running.
	var team models.Team
	if err := s.db.First(&team, "id = ?", teamID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "team not found")
	}
	if team.Status != models.TeamStatusRunning {
		return fiber.NewError(fiber.StatusConflict, "team is not running")
	}

	// Find the target agent.
	var agent models.Agent
	if err := s.db.Where("id = ? AND team_id = ?", agentID, teamID).First(&agent).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "agent not found")
	}

	// Find the leader agent (the one with a running container) to exec into.
	var leader models.Agent
	if err := s.db.Where("team_id = ? AND role = ? AND container_status = ?",
		teamID, models.AgentRoleLeader, models.ContainerStatusRunning).First(&leader).Error; err != nil {
		return fiber.NewError(fiber.StatusConflict, "no running leader agent found for this team")
	}

	var req InstallSkillRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	if err := validateSingleSkillConfig(req.RepoURL, req.SkillName); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}

	// Install the skill in the leader's container.
	cmd := []string{"npx", "skills", "add", req.RepoURL, "--skill", req.SkillName, "--agent", "claude-code", "-y"}
	output, err := s.runtime.ExecInContainer(c.Context(), leader.ContainerID, cmd)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(InstallSkillResponse{
			Output: output,
			Error:  err.Error(),
		})
	}

	// Build the new skill config entry.
	newSkill := map[string]string{
		"repo_url":   req.RepoURL,
		"skill_name": req.SkillName,
	}

	// Append the new skill to the agent's sub_agent_skills in the DB.
	var existingSkills []map[string]string
	if len(agent.SubAgentSkills) > 0 && string(agent.SubAgentSkills) != "null" {
		_ = json.Unmarshal([]byte(agent.SubAgentSkills), &existingSkills)
	}

	// Avoid duplicates.
	alreadyExists := false
	for _, sk := range existingSkills {
		if sk["repo_url"] == req.RepoURL && sk["skill_name"] == req.SkillName {
			alreadyExists = true
			break
		}
	}
	if !alreadyExists {
		existingSkills = append(existingSkills, newSkill)
	}

	updatedSkillsJSON, _ := json.Marshal(existingSkills)
	if err := s.db.Model(&agent).Update("sub_agent_skills", models.JSON(updatedSkillsJSON)).Error; err != nil {
		slog.Error("failed to update agent sub_agent_skills in DB", "error", err)
	}

	// If the target is a worker agent, regenerate its .md file in the container.
	if agent.Role == models.AgentRoleWorker {
		subInfo := runtime.SubAgentInfo{
			Name:        agent.Name,
			Description: agent.SubAgentDescription,
			Model:       agent.SubAgentModel,
			Skills:      json.RawMessage(updatedSkillsJSON),
			ClaudeMD:    agent.ClaudeMD,
		}
		content := runtime.GenerateSubAgentContent(subInfo)

		// Write via exec using base64 to avoid shell escaping issues.
		encoded := base64.StdEncoding.EncodeToString([]byte(content))
		filename := runtime.SubAgentFileName(agent.Name)
		filePath := fmt.Sprintf("/workspace/.claude/agents/%s", filename)
		writeCmd := []string{"sh", "-c", fmt.Sprintf("printf '%%s' '%s' | base64 -d > %s", encoded, filePath)}

		if _, err := s.runtime.ExecInContainer(c.Context(), leader.ContainerID, writeCmd); err != nil {
			slog.Error("failed to update agent .md file in container", "agent", agent.Name, "error", err)
		} else {
			slog.Info("updated agent .md file in container", "agent", agent.Name, "path", filePath)
		}
	}

	return c.JSON(InstallSkillResponse{
		Output:        output,
		UpdatedSkills: existingSkills,
	})
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
