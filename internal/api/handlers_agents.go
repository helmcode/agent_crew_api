package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/helmcode/agent-crew/internal/models"
	"github.com/helmcode/agent-crew/internal/runtime"
)

// ListAgents returns all agents for a team.
func (s *Server) ListAgents(c *fiber.Ctx) error {
	teamID := c.Params("id")

	// Verify team exists and belongs to org.
	var team models.Team
	if err := s.db.Scopes(OrgScope(c)).First(&team, "id = ?", teamID).Error; err != nil {
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

	// Verify team belongs to org.
	var team models.Team
	if err := s.db.Scopes(OrgScope(c)).First(&team, "id = ?", teamID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "team not found")
	}

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
	if err := s.db.Scopes(OrgScope(c)).First(&team, "id = ?", teamID).Error; err != nil {
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

	// Check for duplicate agent name within the team.
	var count int64
	s.db.Model(&models.Agent{}).Where("team_id = ? AND LOWER(name) = LOWER(?)", teamID, req.Name).Count(&count)
	if count > 0 {
		return fiber.NewError(fiber.StatusConflict, "agent name already exists in this team: "+req.Name)
	}

	role := req.Role
	if role == "" {
		role = models.AgentRoleWorker
	}
	if role != models.AgentRoleLeader && role != models.AgentRoleWorker {
		return fiber.NewError(fiber.StatusBadRequest, "role must be 'leader' or 'worker'")
	}

	if req.SubAgentModel != "" && !isValidSubAgentModel(req.SubAgentModel) {
		// For OpenCode teams, allow provider/model format if it matches team's model_provider.
		if team.Provider != models.ProviderOpenCode || !isValidOpenCodeModel(req.SubAgentModel, team.ModelProvider) {
			return fiber.NewError(fiber.StatusBadRequest, "sub_agent_model must be one of: inherit, sonnet, opus, haiku")
		}
	}

	// Validate agent model against team's model_provider.
	if team.ModelProvider != "" && req.SubAgentModel != "" && req.SubAgentModel != "inherit" {
		agentInput := CreateAgentInput{Name: req.Name, SubAgentModel: req.SubAgentModel}
		if err := validateAgentModelConsistency(team.ModelProvider, []CreateAgentInput{agentInput}); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
	}

	if len(req.SubAgentDescription) > maxDescriptionSize {
		return fiber.NewError(fiber.StatusBadRequest, fmt.Sprintf("sub_agent_description exceeds maximum size of %d bytes", maxDescriptionSize))
	}
	if len(req.SubAgentInstructions) > maxInstructionsSize {
		return fiber.NewError(fiber.StatusBadRequest, fmt.Sprintf("sub_agent_instructions exceeds maximum size of %d bytes", maxInstructionsSize))
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

	// Backward compat: accept claude_md as alias for instructions_md.
	instructionsMD := req.InstructionsMD
	if instructionsMD == "" && req.ClaudeMD != "" {
		instructionsMD = req.ClaudeMD
	}

	agent := models.Agent{
		ID:                  uuid.New().String(),
		OrgID:               GetOrgID(c),
		TeamID:              teamID,
		Name:                req.Name,
		Role:                role,
		Specialty:           req.Specialty,
		SystemPrompt:        req.SystemPrompt,
		InstructionsMD:      instructionsMD,
		Skills:              models.JSON(skills),
		Permissions:         models.JSON(perms),
		Resources:           models.JSON(resources),
		SubAgentDescription:  req.SubAgentDescription,
		SubAgentInstructions: req.SubAgentInstructions,
		SubAgentModel:        subAgentModel,
		SubAgentSkills:       models.JSON(subAgentSkills),
	}

	if err := s.db.Create(&agent).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to create agent")
	}

	// If the team is running and the new agent is a worker, create the .md file
	// in the leader's container so it's immediately available.
	if team.Status == models.TeamStatusRunning && agent.Role == models.AgentRoleWorker {
		var leader models.Agent
		if err := s.db.Where("team_id = ? AND role = ? AND container_status = ?",
			teamID, models.AgentRoleLeader, models.ContainerStatusRunning).First(&leader).Error; err == nil {

			// Include leader's global skills in the new subagent's .md file.
			var globalSkills json.RawMessage
			if len(leader.SubAgentSkills) > 0 && string(leader.SubAgentSkills) != "null" {
				globalSkills = json.RawMessage(leader.SubAgentSkills)
			}

			subInfo := runtime.SubAgentInfo{
				Name:         agent.Name,
				Description:  agent.SubAgentDescription,
				Instructions: agent.SubAgentInstructions,
				Model:        agent.SubAgentModel,
				Skills:       json.RawMessage(agent.SubAgentSkills),
				GlobalSkills: globalSkills,
				ClaudeMD:     agent.InstructionsMD,
			}
			content := runtime.GenerateSubAgentContent(subInfo)

			filename := runtime.SubAgentFileName(agent.Name)
			agentsDir := agentsContainerDir(team.Provider)
			filePath := agentsDir + "/" + filename

			encoded := base64.StdEncoding.EncodeToString([]byte(content))
			writeCmd := []string{"sh", "-c", fmt.Sprintf("mkdir -p '%s' && printf '%%s' '%s' | base64 -d > '%s'", agentsDir, encoded, filePath)}

			if _, err := s.runtime.ExecInContainer(c.Context(), leader.ContainerID, writeCmd); err != nil {
				slog.Error("failed to create agent .md file in container", "agent", agent.Name, "error", err)
			} else {
				slog.Info("created agent .md file in container", "agent", agent.Name, "path", filePath)
			}
		}
	}

	return c.Status(fiber.StatusCreated).JSON(agent)
}

// UpdateAgent updates an agent's configuration.
func (s *Server) UpdateAgent(c *fiber.Ctx) error {
	teamID := c.Params("id")
	agentID := c.Params("agentId")

	// Verify team belongs to org.
	var team models.Team
	if err := s.db.Scopes(OrgScope(c)).First(&team, "id = ?", teamID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "team not found")
	}

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
		// Check for duplicate agent name within the team (exclude self).
		var count int64
		s.db.Model(&models.Agent{}).Where("team_id = ? AND LOWER(name) = LOWER(?) AND id != ?", agent.TeamID, *req.Name, agent.ID).Count(&count)
		if count > 0 {
			return fiber.NewError(fiber.StatusConflict, "agent name already exists in this team: "+*req.Name)
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
	if req.InstructionsMD != nil {
		updates["instructions_md"] = *req.InstructionsMD
	} else if req.ClaudeMD != nil {
		// Backward compat: accept claude_md as alias for instructions_md.
		updates["instructions_md"] = *req.ClaudeMD
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
		if len(*req.SubAgentDescription) > maxDescriptionSize {
			return fiber.NewError(fiber.StatusBadRequest, fmt.Sprintf("sub_agent_description exceeds maximum size of %d bytes", maxDescriptionSize))
		}
		updates["sub_agent_description"] = *req.SubAgentDescription
	}
	if req.SubAgentInstructions != nil {
		if len(*req.SubAgentInstructions) > maxInstructionsSize {
			return fiber.NewError(fiber.StatusBadRequest, fmt.Sprintf("sub_agent_instructions exceeds maximum size of %d bytes", maxInstructionsSize))
		}
		updates["sub_agent_instructions"] = *req.SubAgentInstructions
	}
	if req.SubAgentModel != nil {
		if *req.SubAgentModel != "" && !isValidSubAgentModel(*req.SubAgentModel) {
			// For OpenCode teams, allow provider/model format if it matches team's model_provider.
			if team.Provider != models.ProviderOpenCode || !isValidOpenCodeModel(*req.SubAgentModel, team.ModelProvider) {
				return fiber.NewError(fiber.StatusBadRequest, "sub_agent_model must be one of: inherit, sonnet, opus, haiku")
			}
		}
		// Validate against team's model_provider.
		if team.ModelProvider != "" && *req.SubAgentModel != "" && *req.SubAgentModel != "inherit" {
			agentName := agent.Name
			if req.Name != nil {
				agentName = *req.Name
			}
			agentInput := CreateAgentInput{Name: agentName, SubAgentModel: *req.SubAgentModel}
			if err := validateAgentModelConsistency(team.ModelProvider, []CreateAgentInput{agentInput}); err != nil {
				return fiber.NewError(fiber.StatusBadRequest, err.Error())
			}
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

// isValidOpenCodeModel returns true if v is a valid "provider/model" format for OpenCode teams.
// If teamModelProvider is set, the model's provider prefix must match it.
func isValidOpenCodeModel(v, teamModelProvider string) bool {
	parts := strings.SplitN(v, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	if teamModelProvider != "" && parts[0] != teamModelProvider {
		return false
	}
	return true
}

// InstallAgentSkill installs a skill into a running agent's container via exec,
// updates the agent's sub_agent_skills in the database, regenerates the worker's
// .md file in the container, and returns the updated skill list.
func (s *Server) InstallAgentSkill(c *fiber.Ctx) error {
	teamID := c.Params("id")
	agentID := c.Params("agentId")

	// Find team and verify it's running.
	var team models.Team
	if err := s.db.Scopes(OrgScope(c)).First(&team, "id = ?", teamID).Error; err != nil {
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

	// Update skill_statuses so the UI reflects the newly installed skill.
	type skillStatus struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}
	var currentStatuses []skillStatus
	if len(agent.SkillStatuses) > 0 && string(agent.SkillStatuses) != "null" {
		_ = json.Unmarshal([]byte(agent.SkillStatuses), &currentStatuses)
	}
	pkg := req.RepoURL + ":" + req.SkillName
	statusExists := false
	for i, st := range currentStatuses {
		if st.Name == pkg {
			currentStatuses[i].Status = "installed"
			currentStatuses[i].Error = ""
			statusExists = true
			break
		}
	}
	if !statusExists {
		currentStatuses = append(currentStatuses, skillStatus{Name: pkg, Status: "installed"})
	}
	statusJSON, _ := json.Marshal(currentStatuses)
	if err := s.db.Model(&agent).Update("skill_statuses", models.JSON(statusJSON)).Error; err != nil {
		slog.Error("failed to update agent skill_statuses in DB", "error", err)
	}

	// If the target is a worker agent, regenerate its .md file in the container.
	if agent.Role == models.AgentRoleWorker {
		// Include leader's global skills in the worker's .md file.
		var workerLeaderSkills json.RawMessage
		if len(leader.SubAgentSkills) > 0 && string(leader.SubAgentSkills) != "null" {
			workerLeaderSkills = json.RawMessage(leader.SubAgentSkills)
		}

		subInfo := runtime.SubAgentInfo{
			Name:         agent.Name,
			Description:  agent.SubAgentDescription,
			Instructions: agent.SubAgentInstructions,
			Model:        agent.SubAgentModel,
			Skills:       json.RawMessage(updatedSkillsJSON),
			GlobalSkills: workerLeaderSkills,
			ClaudeMD:     agent.InstructionsMD,
		}
		content := runtime.GenerateSubAgentContent(subInfo)

		// Write via exec using base64 to avoid shell escaping issues.
		encoded := base64.StdEncoding.EncodeToString([]byte(content))
		filename := runtime.SubAgentFileName(agent.Name)
		agentsDir := agentsContainerDir(team.Provider)
		filePath := agentsDir + "/" + filename
		writeCmd := []string{"sh", "-c", fmt.Sprintf("printf '%%s' '%s' | base64 -d > '%s'", encoded, filePath)}

		if _, err := s.runtime.ExecInContainer(c.Context(), leader.ContainerID, writeCmd); err != nil {
			slog.Error("failed to update agent .md file in container", "agent", agent.Name, "error", err)
		} else {
			slog.Info("updated agent .md file in container", "agent", agent.Name, "path", filePath)
		}
	}

	// If the target is a leader agent, the skill is global — regenerate all
	// worker sub-agent .md files in the container to include the new skill.
	if agent.Role == models.AgentRoleLeader {
		var workers []models.Agent
		s.db.Where("team_id = ? AND role = ?", teamID, models.AgentRoleWorker).Find(&workers)

		// The freshly updated leader skills.
		globalSkills := json.RawMessage(updatedSkillsJSON)
		agentsDir := agentsContainerDir(team.Provider)

		for _, w := range workers {
			subInfo := runtime.SubAgentInfo{
				Name:         w.Name,
				Description:  w.SubAgentDescription,
				Instructions: w.SubAgentInstructions,
				Model:        w.SubAgentModel,
				Skills:       json.RawMessage(w.SubAgentSkills),
				GlobalSkills: globalSkills,
				ClaudeMD:     w.InstructionsMD,
			}
			content := runtime.GenerateSubAgentContent(subInfo)
			encoded := base64.StdEncoding.EncodeToString([]byte(content))
			filename := runtime.SubAgentFileName(w.Name)
			filePath := agentsDir + "/" + filename
			writeCmd := []string{"sh", "-c", fmt.Sprintf("printf '%%s' '%s' | base64 -d > '%s'", encoded, filePath)}

			if _, err := s.runtime.ExecInContainer(c.Context(), leader.ContainerID, writeCmd); err != nil {
				slog.Error("failed to update worker .md file after leader skill install", "worker", w.Name, "error", err)
			} else {
				slog.Info("updated worker .md file after leader skill install", "worker", w.Name, "path", filePath)
			}
		}
	}

	return c.JSON(InstallSkillResponse{
		Output:        output,
		UpdatedSkills: existingSkills,
	})
}

// maxInstructionsSize is the maximum allowed size for agent instructions content (100KB).
const maxInstructionsSize = 100 * 1024

// maxDescriptionSize is the maximum allowed size for sub-agent description (2KB).
const maxDescriptionSize = 2 * 1024

// GetInstructions reads the instructions file from a running agent's container.
func (s *Server) GetInstructions(c *fiber.Ctx) error {
	teamID := c.Params("id")
	agentID := c.Params("agentId")

	var team models.Team
	if err := s.db.Scopes(OrgScope(c)).First(&team, "id = ?", teamID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "team not found")
	}
	if team.Status != models.TeamStatusRunning {
		return fiber.NewError(fiber.StatusConflict, "team is not running")
	}

	var agent models.Agent
	if err := s.db.Where("id = ? AND team_id = ?", agentID, teamID).First(&agent).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "agent not found")
	}

	containerID, err := s.resolveAgentContainerID(teamID, agent)
	if err != nil {
		return err
	}

	absPath, relPath := agentInstructionsPath(agent, team.Provider)

	content, err := s.runtime.ReadFile(c.Context(), containerID, absPath)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to read instructions: "+err.Error())
	}

	return c.JSON(InstructionsResponse{
		Content: string(content),
		Path:    relPath,
	})
}

// UpdateInstructions writes updated instructions to a running agent's container.
func (s *Server) UpdateInstructions(c *fiber.Ctx) error {
	teamID := c.Params("id")
	agentID := c.Params("agentId")

	var team models.Team
	if err := s.db.Scopes(OrgScope(c)).First(&team, "id = ?", teamID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "team not found")
	}
	if team.Status != models.TeamStatusRunning {
		return fiber.NewError(fiber.StatusConflict, "team is not running")
	}

	var agent models.Agent
	if err := s.db.Where("id = ? AND team_id = ?", agentID, teamID).First(&agent).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "agent not found")
	}

	containerID, err := s.resolveAgentContainerID(teamID, agent)
	if err != nil {
		return err
	}

	var req UpdateInstructionsRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	if strings.TrimSpace(req.Content) == "" {
		return fiber.NewError(fiber.StatusBadRequest, "content is required")
	}
	if len(req.Content) > maxInstructionsSize {
		return fiber.NewError(fiber.StatusBadRequest, fmt.Sprintf("content exceeds maximum size of %d bytes", maxInstructionsSize))
	}

	absPath, relPath := agentInstructionsPath(agent, team.Provider)

	if err := s.runtime.WriteFile(c.Context(), containerID, absPath, []byte(req.Content)); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to write instructions: "+err.Error())
	}

	// Persist to database so redeployments use the user's latest edits
	// instead of regenerating from defaults.
	if err := s.db.Model(&agent).Update("instructions_md", req.Content).Error; err != nil {
		slog.Error("failed to persist instructions to database", "agent", agent.Name, "error", err)
		// Non-fatal: the container file was updated successfully.
	}

	slog.Info("agent instructions updated", "agent", agent.Name, "team", teamID, "path", relPath)

	return c.JSON(InstructionsResponse{
		Content: req.Content,
		Path:    relPath,
	})
}

// resolveAgentContainerID returns the container ID to use for file operations.
// Leaders use their own container; workers use the leader's container since
// worker agent files live in the leader's shared workspace.
func (s *Server) resolveAgentContainerID(teamID string, agent models.Agent) (string, error) {
	if agent.Role == models.AgentRoleLeader {
		if agent.ContainerStatus != models.ContainerStatusRunning {
			return "", fiber.NewError(fiber.StatusConflict, "agent is not running")
		}
		return agent.ContainerID, nil
	}

	// Workers live inside the leader's container workspace.
	var leader models.Agent
	if err := s.db.Where("team_id = ? AND role = ?", teamID, models.AgentRoleLeader).First(&leader).Error; err != nil {
		return "", fiber.NewError(fiber.StatusNotFound, "team leader not found")
	}
	if leader.ContainerStatus != models.ContainerStatusRunning {
		return "", fiber.NewError(fiber.StatusConflict, "team leader is not running")
	}
	return leader.ContainerID, nil
}

// agentsContainerDir returns the absolute container path of the agents directory
// based on the provider: /workspace/.claude/agents for Claude, /workspace/.opencode/agents for OpenCode.
func agentsContainerDir(provider string) string {
	if provider == models.ProviderOpenCode {
		return "/workspace/.opencode/agents"
	}
	return "/workspace/.claude/agents"
}

// agentInstructionsPath returns the absolute container path and relative display
// path for an agent's instructions file. The provider determines the directory
// layout: Claude uses .claude/, OpenCode uses .opencode/.
func agentInstructionsPath(agent models.Agent, provider string) (absPath, relPath string) {
	if provider == models.ProviderOpenCode {
		if agent.Role == models.AgentRoleLeader {
			return "/workspace/.opencode/AGENTS.MD", ".opencode/AGENTS.MD"
		}
		filename := runtime.SubAgentFileName(agent.Name)
		return "/workspace/.opencode/agents/" + filename, ".opencode/agents/" + filename
	}
	// Claude provider (default).
	if agent.Role == models.AgentRoleLeader {
		return "/workspace/.claude/CLAUDE.md", ".claude/CLAUDE.md"
	}
	filename := runtime.SubAgentFileName(agent.Name)
	return "/workspace/.claude/agents/" + filename, ".claude/agents/" + filename
}

// DeleteAgent removes an agent from a team.
func (s *Server) DeleteAgent(c *fiber.Ctx) error {
	teamID := c.Params("id")
	agentID := c.Params("agentId")

	// Verify team belongs to org.
	var team models.Team
	if err := s.db.Scopes(OrgScope(c)).First(&team, "id = ?", teamID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "team not found")
	}

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
