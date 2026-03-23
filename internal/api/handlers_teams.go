package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/helmcode/agent-crew/internal/crypto"
	"github.com/helmcode/agent-crew/internal/models"
	"github.com/helmcode/agent-crew/internal/protocol"
	"github.com/helmcode/agent-crew/internal/runtime"
)

// ListTeams returns all teams for the current organization.
func (s *Server) ListTeams(c *fiber.Ctx) error {
	var teams []models.Team
	if err := s.db.Scopes(OrgScope(c)).Preload("Agents").Find(&teams).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to list teams")
	}
	return c.JSON(teams)
}

// GetTeam returns a single team by ID.
func (s *Server) GetTeam(c *fiber.Ctx) error {
	id := c.Params("id")
	var team models.Team
	if err := s.db.Scopes(OrgScope(c)).Preload("Agents").First(&team, "id = ?", id).Error; err != nil {
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

	prov := req.Provider
	if prov == "" {
		prov = models.ProviderClaude
	}
	if prov != models.ProviderClaude && prov != models.ProviderOpenCode {
		return fiber.NewError(fiber.StatusBadRequest, "provider must be 'claude' or 'opencode'")
	}

	// Validate model_provider.
	if err := validateModelProvider(prov, req.ModelProvider); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}

	// Validate agent model consistency with model_provider.
	if err := validateAgentModelConsistency(req.ModelProvider, req.Agents); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}

	if err := validateAgentImage(req.AgentImage); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}

	team := models.Team{
		ID:            uuid.New().String(),
		OrgID:         GetOrgID(c),
		Name:          req.Name,
		Description:   req.Description,
		Status:        models.TeamStatusStopped,
		Runtime:       rt,
		Provider:      prov,
		ModelProvider: req.ModelProvider,
		WorkspacePath: req.WorkspacePath,
		AgentImage:    req.AgentImage,
	}

	// Validate and serialize MCP servers.
	if req.McpServers != nil {
		if err := validateMcpServers(req.McpServers); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		mcpData, _ := json.Marshal(req.McpServers)
		team.McpServers = models.JSON(mcpData)
	}

	// Check for duplicate agent names in the request.
	seen := map[string]struct{}{}
	for _, a := range req.Agents {
		if a.Name != "" {
			lower := strings.ToLower(a.Name)
			if _, exists := seen[lower]; exists {
				return fiber.NewError(fiber.StatusConflict, "duplicate agent name: "+a.Name)
			}
			seen[lower] = struct{}{}
		}
	}

	// Create agents if provided.
	for _, a := range req.Agents {
		if a.Name != "" {
			if err := validateName(a.Name); err != nil {
				return fiber.NewError(fiber.StatusBadRequest, "agent "+a.Name+": "+err.Error())
			}
		}
		agentLabel := a.Name
		if agentLabel == "" {
			agentLabel = "(unnamed)"
		}
		if len(a.SubAgentDescription) > maxDescriptionSize {
			return fiber.NewError(fiber.StatusBadRequest, fmt.Sprintf("agent %s: sub_agent_description exceeds maximum size of %d bytes", agentLabel, maxDescriptionSize))
		}
		if len(a.SubAgentInstructions) > maxInstructionsSize {
			return fiber.NewError(fiber.StatusBadRequest, fmt.Sprintf("agent %s: sub_agent_instructions exceeds maximum size of %d bytes", agentLabel, maxInstructionsSize))
		}
		if a.SubAgentSkills != nil {
			if err := validateSubAgentSkills(a.SubAgentSkills); err != nil {
				return fiber.NewError(fiber.StatusBadRequest, "agent "+agentLabel+": "+err.Error())
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

		// Backward compat: accept claude_md as alias for instructions_md.
		instructionsMD := a.InstructionsMD
		if instructionsMD == "" && a.ClaudeMD != "" {
			instructionsMD = a.ClaudeMD
		}

		team.Agents = append(team.Agents, models.Agent{
			ID:                  uuid.New().String(),
			Name:                a.Name,
			Role:                role,
			Specialty:           a.Specialty,
			SystemPrompt:        a.SystemPrompt,
			InstructionsMD:      instructionsMD,
			Skills:              models.JSON(skills),
			Permissions:         models.JSON(perms),
			Resources:           models.JSON(resources),
			SubAgentDescription:  a.SubAgentDescription,
			SubAgentInstructions: a.SubAgentInstructions,
			SubAgentModel:        subAgentModel,
			SubAgentSkills:       models.JSON(subAgentSkills),
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
	if err := s.db.Scopes(OrgScope(c)).First(&team, "id = ?", id).Error; err != nil {
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
	if req.Provider != nil {
		if *req.Provider != models.ProviderClaude && *req.Provider != models.ProviderOpenCode {
			return fiber.NewError(fiber.StatusBadRequest, "provider must be 'claude' or 'opencode'")
		}
		updates["provider"] = *req.Provider
		// Switching to Claude invalidates model_provider (Claude always uses Anthropic).
		if *req.Provider == models.ProviderClaude {
			updates["model_provider"] = ""
		}
	}

	// Validate and apply model_provider.
	if req.ModelProvider != nil {
		effectiveProvider := team.Provider
		if req.Provider != nil {
			effectiveProvider = *req.Provider
		}
		if err := validateModelProvider(effectiveProvider, *req.ModelProvider); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		updates["model_provider"] = *req.ModelProvider

		// If model_provider changed, reset all agent models to "inherit".
		if *req.ModelProvider != team.ModelProvider {
			s.db.Model(&models.Agent{}).Where("team_id = ?", team.ID).Update("sub_agent_model", "inherit")
		}
	}

	if req.AgentImage != nil {
		if err := validateAgentImage(*req.AgentImage); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		updates["agent_image"] = *req.AgentImage
	}
	if req.McpServers != nil {
		if err := validateMcpServers(req.McpServers); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		mcpData, _ := json.Marshal(req.McpServers)
		updates["mcp_servers"] = models.JSON(mcpData)
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
	if err := s.db.Scopes(OrgScope(c)).First(&team, "id = ?", id).Error; err != nil {
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
	if err := s.db.Scopes(OrgScope(c)).Preload("Agents").First(&team, "id = ?", id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "team not found")
	}

	if team.Status == models.TeamStatusRunning {
		return fiber.NewError(fiber.StatusConflict, "team is already running")
	}

	// Update status to deploying and clear any previous error message.
	s.db.Model(&team).Updates(map[string]interface{}{
		"status":         models.TeamStatusDeploying,
		"status_message": "",
	})

	// Deep copy agents for the background goroutine to avoid data races
	// with the JSON serialization of the response below.
	asyncTeam := team
	asyncTeam.Agents = make([]models.Agent, len(team.Agents))
	copy(asyncTeam.Agents, team.Agents)

	// Deploy in background.
	go s.deployTeamAsync(asyncTeam)

	team.Status = models.TeamStatusDeploying
	team.StatusMessage = ""
	return c.JSON(team)
}

func (s *Server) deployTeamAsync(team models.Team) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic in deployTeamAsync", "team", team.Name, "panic", r)
			s.db.Model(&team).Updates(map[string]interface{}{
				"status":         models.TeamStatusError,
				"status_message": "Unexpected error during deployment",
			})
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Load settings from DB to pass as environment variables to agent containers.
	envFromSettings := s.LoadSettingsEnv(team.OrgID)

	// Deploy infrastructure.
	infraCfg := runtime.InfraConfig{
		TeamName:      team.Name,
		NATSEnabled:   true,
		WorkspacePath: team.WorkspacePath,
	}

	if err := s.runtime.DeployInfra(ctx, infraCfg); err != nil {
		slog.Error("failed to deploy infrastructure", "team", team.Name, "error", err)
		s.db.Model(&team).Updates(map[string]interface{}{
			"status":         models.TeamStatusError,
			"status_message": "Failed to deploy infrastructure: " + err.Error(),
		})
		return
	}

	natsURL := s.runtime.GetNATSURL(team.Name)
	provider := team.Provider
	if provider == "" {
		provider = models.ProviderClaude
	}

	// If the team uses Ollama, set up the shared Ollama container.
	var ollamaSetupDone bool
	if team.ModelProvider == models.ModelProviderOllama {
		if om, ok := s.runtime.(runtime.OllamaManager); ok {
			s.db.Model(&team).Update("status_message", "Starting Ollama container...")

			containerID, err := om.EnsureOllama(ctx)
			if err != nil {
				slog.Error("failed to start ollama", "team", team.Name, "error", err)
				s.db.Model(&team).Updates(map[string]interface{}{
					"status":         models.TeamStatusError,
					"status_message": "Failed to start Ollama: " + err.Error(),
				})
				return
			}

			// Connect Ollama to team network so agent containers can resolve it.
			teamNetName := runtime.TeamNetworkName(SanitizeName(team.Name))
			if err := om.ConnectOllamaToNetwork(ctx, teamNetName); err != nil {
				slog.Error("failed to connect ollama to network", "team", team.Name, "error", err)
				s.db.Model(&team).Updates(map[string]interface{}{
					"status":         models.TeamStatusError,
					"status_message": "Failed to connect Ollama to network: " + err.Error(),
				})
				return
			}

			// Determine model to pull from leader's SubAgentModel (strip "ollama/" prefix).
			ollamaModel := ""
			for _, a := range team.Agents {
				if a.Role == models.AgentRoleLeader {
					ollamaModel = a.SubAgentModel
					break
				}
			}
			if ollamaModel != "" && ollamaModel != "inherit" {
				ollamaModel = strings.TrimPrefix(ollamaModel, "ollama/")
				s.db.Model(&team).Update("status_message", "Pulling Ollama model: "+ollamaModel+"...")

				if err := om.PullOllamaModel(ctx, ollamaModel, func(status string) {
					s.db.Model(&team).Update("status_message", "Pulling model: "+status)
				}); err != nil {
					slog.Error("failed to pull ollama model", "team", team.Name, "model", ollamaModel, "error", err)
					s.db.Model(&team).Updates(map[string]interface{}{
						"status":         models.TeamStatusError,
						"status_message": "Failed to pull Ollama model " + ollamaModel + ": " + err.Error(),
					})
					return
				}
			}

			// Track SharedInfra with thread-safe ref counting.
			s.ollamaMu.Lock()
			var infra models.SharedInfra
			result := s.db.Where("resource_type = ?", "ollama").First(&infra)
			if result.Error != nil {
				infra = models.SharedInfra{
					ID:           uuid.New().String(),
					ResourceType: "ollama",
					ContainerID:  containerID,
					Status:       "running",
					RefCount:     1,
				}
				s.db.Create(&infra)
			} else {
				s.db.Model(&infra).Updates(map[string]interface{}{
					"container_id": containerID,
					"status":       "running",
					"ref_count":    infra.RefCount + 1,
				})
			}
			s.ollamaMu.Unlock()

			ollamaSetupDone = true
			slog.Info("ollama setup complete for team", "team", team.Name, "container", containerID)
		} else {
			slog.Warn("runtime does not support Ollama management", "team", team.Name)
		}
	}
	_ = ollamaSetupDone // used for env injection below

	// Build team member list for the leader's instructions.
	var teamMembers []runtime.TeamMemberInfo
	for _, a := range team.Agents {
		teamMembers = append(teamMembers, runtime.TeamMemberInfo{
			Name:      SanitizeName(a.Name),
			Role:      a.Role,
			Specialty: a.Specialty,
		})
	}

	// Find the leader agent and extract its skills before building sub-agent files.
	var leaderSkills json.RawMessage
	var leaderSkillConfigs []protocol.SkillConfig
	for _, a := range team.Agents {
		if a.Role == models.AgentRoleLeader {
			if len(a.SubAgentSkills) > 0 && string(a.SubAgentSkills) != "null" {
				leaderSkills = json.RawMessage(a.SubAgentSkills)
				_ = json.Unmarshal(a.SubAgentSkills, &leaderSkillConfigs)
			}
			break
		}
	}

	// Setup workspace files for all agents and deploy only the leader container.
	var leader *models.Agent
	subAgentFiles := map[string]string{}
	var openCodeWorkers []runtime.SubAgentInfo // Collect workers for OpenCode host workspace setup.
	for i := range team.Agents {
		agent := &team.Agents[i]

		if agent.Role != models.AgentRoleLeader {
			if provider == models.ProviderOpenCode {
				// OpenCode sub-agent files go to .opencode/agents/
				subInfo := runtime.SubAgentInfo{
					Name:         agent.Name,
					Description:  agent.SubAgentDescription,
					Instructions: agent.SubAgentInstructions,
					Model:        agent.SubAgentModel,
					Skills:       json.RawMessage(agent.SubAgentSkills),
					ClaudeMD:     agent.InstructionsMD,
				}
				filename := runtime.SubAgentFileName(agent.Name)
				subAgentFiles[filename] = runtime.GenerateOpenCodeSubAgentContent(subInfo, leaderSkillConfigs)
				openCodeWorkers = append(openCodeWorkers, subInfo)
			} else {
				// Claude sub-agent files go to .claude/agents/
				info := runtime.AgentWorkspaceInfo{
					Name:         agent.Name,
					Role:         agent.Role,
					Specialty:    agent.Specialty,
					SystemPrompt: agent.SystemPrompt,
					ClaudeMD:     agent.InstructionsMD,
					Skills:       json.RawMessage(agent.Skills),
				}
				subInfo := runtime.SubAgentInfo{
					Name:         agent.Name,
					Description:  agent.SubAgentDescription,
					Instructions: agent.SubAgentInstructions,
					Model:        agent.SubAgentModel,
					Skills:       json.RawMessage(agent.SubAgentSkills),
					GlobalSkills: leaderSkills,
					ClaudeMD:     agent.InstructionsMD,
				}
				if subInfo.ClaudeMD == "" {
					subInfo.ClaudeMD = runtime.GenerateClaudeMD(info)
				}
				filename := runtime.SubAgentFileName(agent.Name)
				subAgentFiles[filename] = runtime.GenerateSubAgentContent(subInfo)

				if team.WorkspacePath != "" {
					if _, err := runtime.SetupSubAgentFile(team.WorkspacePath, subInfo); err != nil {
						slog.Error("failed to setup sub-agent file", "agent", agent.Name, "error", err)
					}
				}
			}
		} else {
			if team.WorkspacePath != "" && provider == models.ProviderClaude {
				info := runtime.AgentWorkspaceInfo{
					Name:         agent.Name,
					Role:         agent.Role,
					Specialty:    agent.Specialty,
					SystemPrompt: agent.SystemPrompt,
					ClaudeMD:     agent.InstructionsMD,
					Skills:       json.RawMessage(agent.Skills),
					TeamMembers:  teamMembers,
				}
				if _, err := runtime.SetupAgentWorkspace(team.WorkspacePath, info); err != nil {
					slog.Error("failed to setup agent workspace", "agent", agent.Name, "error", err)
				}
			}
			leader = agent
		}
	}

	// Write OpenCode workspace to host path so the directory exists before
	// Docker tries to bind-mount it (os.Stat in DeployAgent would fail otherwise).
	if team.WorkspacePath != "" && provider == models.ProviderOpenCode && leader != nil {
		leaderSub := runtime.SubAgentInfo{
			Name:        leader.Name,
			Description: leader.Specialty,
			Skills:      json.RawMessage(leader.Skills),
			ClaudeMD:    leader.InstructionsMD,
		}
		if err := runtime.SetupOpenCodeWorkspace(team.WorkspacePath, team.Name, leaderSub, openCodeWorkers, leaderSkillConfigs); err != nil {
			slog.Error("failed to setup opencode workspace", "team", team.Name, "error", err)
		}
	}

	if leader == nil {
		slog.Error("no leader agent found in team", "team", team.Name)
		s.db.Model(&team).Updates(map[string]interface{}{
			"status":         models.TeamStatusError,
			"status_message": "No leader agent found in team configuration",
		})
		return
	}

	var res runtime.ResourceConfig
	if len(leader.Resources) > 0 {
		_ = json.Unmarshal(leader.Resources, &res)
	}

	// Generate leader instructions content based on provider.
	var instructionsMDContent string
	if provider == models.ProviderOpenCode {
		if leader.InstructionsMD != "" {
			instructionsMDContent = leader.InstructionsMD
		} else {
			leaderSubInfo := runtime.SubAgentInfo{
				Name:        leader.Name,
				Description: leader.Specialty,
				Skills:      json.RawMessage(leader.Skills),
				ClaudeMD:    leader.InstructionsMD,
			}
			workers := make([]runtime.SubAgentInfo, 0)
			for _, a := range team.Agents {
				if a.Role != models.AgentRoleLeader {
					workers = append(workers, runtime.SubAgentInfo{
						Name:        a.Name,
						Description: a.SubAgentDescription,
					})
				}
			}
			instructionsMDContent = runtime.GenerateOpenCodeAgentsMD(team.Name, leaderSubInfo, workers)
		}
	} else {
		leaderInfo := runtime.AgentWorkspaceInfo{
			Name:         leader.Name,
			Role:         leader.Role,
			Specialty:    leader.Specialty,
			SystemPrompt: leader.SystemPrompt,
			ClaudeMD:     leader.InstructionsMD,
			Skills:       json.RawMessage(leader.Skills),
			TeamMembers:  teamMembers,
		}
		instructionsMDContent = leader.InstructionsMD
		if instructionsMDContent == "" {
			instructionsMDContent = runtime.GenerateClaudeMD(leaderInfo)
		}
	}

	// Collect all unique skills from all agents for sidecar installation.
	type skillKey struct{ RepoURL, SkillName string }
	skillsSet := map[skillKey]struct{}{}
	var allSkills []protocol.SkillConfig
	for _, a := range team.Agents {
		var agentSkills []protocol.SkillConfig
		if err := json.Unmarshal(a.SubAgentSkills, &agentSkills); err == nil {
			for _, s := range agentSkills {
				key := skillKey{s.RepoURL, s.SkillName}
				if s.RepoURL != "" && s.SkillName != "" {
					if _, exists := skillsSet[key]; !exists {
						skillsSet[key] = struct{}{}
						allSkills = append(allSkills, s)
					}
				}
			}
		} else {
			var strSkills []string
			if err := json.Unmarshal(a.SubAgentSkills, &strSkills); err == nil {
				for _, s := range strSkills {
					idx := strings.LastIndex(s, ":")
					if idx <= 0 || idx == len(s)-1 {
						continue
					}
					repoURL := s[:idx]
					skillName := s[idx+1:]
					if repoURL == "" || skillName == "" {
						continue
					}
					if !strings.HasPrefix(repoURL, "https://") {
						repoURL = "https://github.com/" + repoURL
					}
					cfg := protocol.SkillConfig{RepoURL: repoURL, SkillName: skillName}
					key := skillKey{cfg.RepoURL, cfg.SkillName}
					if _, exists := skillsSet[key]; !exists {
						skillsSet[key] = struct{}{}
						allSkills = append(allSkills, cfg)
					}
				}
			}
		}
	}
	skillsJSON, _ := json.Marshal(allSkills)

	agentEnv := envFromSettings
	if len(allSkills) > 0 {
		agentEnv["AGENT_SKILLS_INSTALL"] = string(skillsJSON)
	}

	// When model_provider is set, only inject the relevant API key to the container
	// instead of passing all provider keys. This prevents leaking unnecessary credentials.
	if team.ModelProvider != "" && provider == models.ProviderOpenCode {
		filterAPIKeysByModelProvider(agentEnv, team.ModelProvider)
	}

	// Inject OLLAMA_BASE_URL for Ollama-backed teams so agent containers
	// can reach the shared Ollama instance via Docker DNS.
	if team.ModelProvider == models.ModelProviderOllama {
		agentEnv["OLLAMA_BASE_URL"] = runtime.OllamaInternalURL
	}

	// Collect MCP servers from team config.
	if len(team.McpServers) > 0 && string(team.McpServers) != "null" && string(team.McpServers) != "[]" {
		agentEnv["AGENT_MCP_SERVERS"] = string(team.McpServers)
	}

	// Pass the leader's model to the agent container.
	leaderModel := leader.SubAgentModel
	if leaderModel != "" && leaderModel != "inherit" {
		if provider == models.ProviderOpenCode {
			// OpenCode uses OPENCODE_MODEL env var with "providerID/modelID" format.
			// Leader's SubAgentModel is already in that format for OpenCode teams.
			agentEnv["OPENCODE_MODEL"] = leaderModel
		} else {
			// Claude uses CLAUDE_MODEL env var. Map short names to full model IDs.
			if fullModel := claudeModelID(leaderModel); fullModel != "" {
				agentEnv["CLAUDE_MODEL"] = fullModel
			}
		}
	} else if provider == models.ProviderOpenCode {
		// Fallback: use OPENCODE_MODEL from Settings if the leader has no specific model.
		if m := envFromSettings["OPENCODE_MODEL"]; m != "" {
			agentEnv["OPENCODE_MODEL"] = m
		}
	}

	agentCfg := runtime.AgentConfig{
		Name:          leader.Name,
		TeamName:      team.Name,
		Role:          leader.Role,
		Provider:      provider,
		SystemPrompt:  leader.SystemPrompt,
		ClaudeMD:      instructionsMDContent,
		Resources:     res,
		NATSUrl:       natsURL,
		Image:         team.AgentImage,
		WorkspacePath: team.WorkspacePath,
		SubAgentFiles: subAgentFiles,
		Env:           agentEnv,
	}

	instance, err := s.runtime.DeployAgent(ctx, agentCfg)
	if err != nil {
		slog.Error("failed to deploy leader agent", "agent", leader.Name, "error", err)
		s.db.Model(leader).Updates(map[string]interface{}{
			"container_status": models.ContainerStatusError,
		})
		s.db.Model(&team).Updates(map[string]interface{}{
			"status":         models.TeamStatusError,
			"status_message": err.Error(),
		})
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

// apiKeysByProvider maps model_provider values to the env var names that hold their API keys.
var apiKeysByProvider = map[string][]string{
	models.ModelProviderAnthropic: {"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN"},
	models.ModelProviderOpenAI:    {"OPENAI_API_KEY"},
	models.ModelProviderGoogle:    {"GOOGLE_API_KEY", "GEMINI_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY"},
	models.ModelProviderOllama:    {}, // Ollama is local, no API key needed.
}

// allProviderAPIKeys returns a flat set of all known provider API key env vars.
func allProviderAPIKeys() map[string]bool {
	keys := make(map[string]bool)
	for _, envVars := range apiKeysByProvider {
		for _, k := range envVars {
			keys[k] = true
		}
	}
	return keys
}

// filterAPIKeysByModelProvider removes API keys from env that don't belong to the
// specified model_provider. This prevents unnecessary credential exposure.
func filterAPIKeysByModelProvider(env map[string]string, modelProvider string) {
	keepKeys := make(map[string]bool)
	if keys, ok := apiKeysByProvider[modelProvider]; ok {
		for _, k := range keys {
			keepKeys[k] = true
		}
	}

	allKeys := allProviderAPIKeys()
	for key := range env {
		if allKeys[key] && !keepKeys[key] {
			delete(env, key)
		}
	}
}

// claudeModelID maps the short model names used in SubAgentModel (sonnet, opus,
// haiku) to the full Claude Code CLI model IDs. Returns empty string for
// unrecognized values.
func claudeModelID(short string) string {
	switch short {
	case "sonnet":
		return "claude-sonnet-4-20250514"
	case "opus":
		return "claude-opus-4-20250514"
	case "haiku":
		return "claude-haiku-4-5-20251001"
	default:
		return ""
	}
}

// LoadSettingsEnv reads settings from the database for the given org and returns
// them as a string map suitable for passing to AgentConfig.Env. Secret values
// are decrypted so agent containers receive the real values.
func (s *Server) LoadSettingsEnv(orgID string) map[string]string {
	env := make(map[string]string)

	var settings []models.Settings
	if err := s.db.Where("org_id = ?", orgID).Find(&settings).Error; err != nil {
		slog.Error("failed to load settings for env", "org_id", orgID, "error", err)
		return env
	}

	for _, setting := range settings {
		if setting.Value == "" {
			continue
		}
		value := setting.Value
		if setting.IsSecret {
			decrypted, err := crypto.Decrypt(value)
			if err != nil {
				slog.Error("failed to decrypt setting", "key", setting.Key, "error", err)
				continue
			}
			value = decrypted
		}
		env[setting.Key] = value
	}

	// Aliases: map alternative key names users may have used in Settings.
	aliases := map[string]string{
		"ANTHROPIC_AUTH_TOKEN": "CLAUDE_CODE_OAUTH_TOKEN",
	}
	for alias, target := range aliases {
		if env[target] != "" {
			continue // already set from primary key
		}
		if v, ok := env[alias]; ok && v != "" {
			env[target] = v
		}
	}

	return env
}

// StopTeam tears down all team infrastructure.
func (s *Server) StopTeam(c *fiber.Ctx) error {
	id := c.Params("id")
	var team models.Team
	if err := s.db.Scopes(OrgScope(c)).Preload("Agents").First(&team, "id = ?", id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "team not found")
	}

	if team.Status != models.TeamStatusRunning && team.Status != models.TeamStatusError {
		return fiber.NewError(fiber.StatusConflict, "team is not running")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// If the team used Ollama, decrement ref count and disconnect from network
	// BEFORE TeardownInfra removes the network.
	if team.ModelProvider == models.ModelProviderOllama {
		if om, ok := s.runtime.(runtime.OllamaManager); ok {
			teamNetName := runtime.TeamNetworkName(SanitizeName(team.Name))
			if err := om.DisconnectOllamaFromNetwork(ctx, teamNetName); err != nil {
				slog.Error("failed to disconnect ollama from network", "team", team.Name, "error", err)
			}

			s.ollamaMu.Lock()
			var infra models.SharedInfra
			if err := s.db.Where("resource_type = ?", "ollama").First(&infra).Error; err == nil {
				newRefCount := infra.RefCount - 1
				if newRefCount < 0 {
					newRefCount = 0
				}
				if newRefCount == 0 {
					s.db.Model(&infra).Updates(map[string]interface{}{
						"ref_count": 0,
						"status":    "stopped",
					})
					// Stop the container (don't remove — preserve models).
					if err := om.StopOllama(ctx); err != nil {
						slog.Error("failed to stop ollama", "error", err)
					}
				} else {
					s.db.Model(&infra).Update("ref_count", newRefCount)
				}
			}
			s.ollamaMu.Unlock()
		}
	}

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

	s.db.Model(&team).Updates(map[string]interface{}{
		"status":         models.TeamStatusStopped,
		"status_message": "",
	})
	team.Status = models.TeamStatusStopped
	team.StatusMessage = ""
	return c.JSON(team)
}
