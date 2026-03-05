package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"gorm.io/gorm"

	"github.com/helmcode/agent-crew/internal/models"
	"github.com/helmcode/agent-crew/internal/postaction"
	"github.com/helmcode/agent-crew/internal/protocol"
	"github.com/helmcode/agent-crew/internal/runtime"
)

// DefaultTimeout is the default schedule execution timeout (1 hour).
const DefaultTimeout = time.Hour

// MaxPromptSize is the maximum allowed prompt length in characters.
const MaxPromptSize = 50000

// Executor handles the lifecycle of a scheduled execution:
// create run → deploy team → send prompt → monitor → record result → stop team.
type Executor struct {
	DB      *gorm.DB
	Runtime runtime.AgentRuntime
	Timeout time.Duration

	// DeployTeamFunc deploys a team and blocks until running or error.
	// If nil, the executor uses its own implementation.
	DeployTeamFunc func(ctx context.Context, team models.Team) error

	// StopTeamFunc stops a running team.
	// If nil, the executor uses its own implementation.
	StopTeamFunc func(ctx context.Context, team models.Team) error

	// SendPromptFunc sends a prompt to the team's leader via NATS.
	// If nil, the executor uses its own implementation.
	SendPromptFunc func(ctx context.Context, teamName, message string) error

	// WaitForResponseFunc waits for a leader_response after sending a prompt.
	// If nil, the executor subscribes directly to NATS (default) instead of
	// polling the database.
	WaitForResponseFunc func(ctx context.Context, teamName string) error

	// LoadSettingsEnvFunc loads settings from DB as env vars for agent containers.
	// Required for deployment.
	LoadSettingsEnvFunc func() map[string]string

	// PollInterval controls how frequently the executor polls for state changes.
	// Defaults to 10 seconds if zero.
	PollInterval time.Duration

	// PostActionExec fires post-actions after schedule runs complete.
	PostActionExec *postaction.Executor
}

// NewExecutor creates an Executor with the given dependencies.
func NewExecutor(db *gorm.DB, rt runtime.AgentRuntime) *Executor {
	timeout := DefaultTimeout
	if envTimeout := os.Getenv("SCHEDULE_TIMEOUT"); envTimeout != "" {
		if d, err := time.ParseDuration(envTimeout); err == nil && d > 0 {
			timeout = d
		}
	}
	return &Executor{
		DB:             db,
		Runtime:        rt,
		Timeout:        timeout,
		PostActionExec: postaction.NewExecutor(db),
	}
}

// Execute runs a scheduled task: deploy team, send prompt, wait for completion,
// and tear down. It creates a ScheduleRun record and always cleans up.
func (e *Executor) Execute(ctx context.Context, schedule models.Schedule) {
	// H2 FIX: Validate prompt size before starting execution.
	if len(schedule.Prompt) > MaxPromptSize {
		slog.Error("executor: prompt exceeds maximum size",
			"schedule_id", schedule.ID, "prompt_length", len(schedule.Prompt),
			"max_size", MaxPromptSize)
		e.markScheduleError(schedule.ID, fmt.Sprintf("prompt size %d exceeds maximum %d", len(schedule.Prompt), MaxPromptSize))
		return
	}

	runID := uuid.New().String()
	now := time.Now()

	// Create the ScheduleRun record.
	run := models.ScheduleRun{
		ID:         runID,
		ScheduleID: schedule.ID,
		StartedAt:  now,
		Status:     models.ScheduleRunStatusRunning,
	}
	if err := e.DB.Create(&run).Error; err != nil {
		slog.Error("executor: failed to create schedule run",
			"schedule_id", schedule.ID, "error", err)
		e.markScheduleError(schedule.ID, "failed to create run record: "+err.Error())
		return
	}

	slog.Info("executor: starting schedule execution",
		"schedule_id", schedule.ID,
		"run_id", runID,
		"team_id", schedule.TeamID,
	)

	// Execute with timeout.
	execCtx, cancel := context.WithTimeout(ctx, e.Timeout)
	defer cancel()

	err := e.executeWithCleanup(execCtx, schedule, runID)

	// Record result.
	finished := time.Now()
	runUpdates := map[string]interface{}{
		"finished_at": finished,
	}

	if err != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			runUpdates["status"] = models.ScheduleRunStatusTimeout
			runUpdates["error"] = "execution timed out after " + e.Timeout.String()
			slog.Warn("executor: schedule execution timed out",
				"schedule_id", schedule.ID, "run_id", runID)
		} else {
			runUpdates["status"] = models.ScheduleRunStatusFailed
			// H3+H4 FIX: Sanitize error before storing in DB.
			runUpdates["error"] = sanitizeError(err.Error())
			slog.Error("executor: schedule execution failed",
				"schedule_id", schedule.ID, "run_id", runID, "error", err)
		}
	} else {
		runUpdates["status"] = models.ScheduleRunStatusSuccess
		slog.Info("executor: schedule execution succeeded",
			"schedule_id", schedule.ID, "run_id", runID)
	}

	if dbErr := e.DB.Model(&models.ScheduleRun{}).
		Where("id = ?", runID).
		Updates(runUpdates).Error; dbErr != nil {
		slog.Error("executor: failed to update run record",
			"run_id", runID, "error", dbErr)
	}

	// Fire post-actions (fire-and-forget).
	if e.PostActionExec != nil {
		// Look up team name for the post-action context.
		var team models.Team
		teamName := ""
		if dbErr := e.DB.First(&team, "id = ?", schedule.TeamID).Error; dbErr == nil {
			teamName = team.Name
		}

		// Read the finalized run to get response_received.
		var finalRun models.ScheduleRun
		response := ""
		if dbErr := e.DB.First(&finalRun, "id = ?", runID).Error; dbErr == nil {
			response = finalRun.ResponseReceived
		}

		runStatus, _ := runUpdates["status"].(string)
		runError, _ := runUpdates["error"].(string)

		e.PostActionExec.ExecutePostActions(postaction.PostActionContext{
			SourceType:  "schedule",
			TriggerID:   schedule.ID,
			RunID:       runID,
			Status:      runStatus,
			Response:    response,
			Error:       runError,
			TriggerName: schedule.Name,
			TeamName:    teamName,
			Prompt:      schedule.Prompt,
			StartedAt:   now.Format(time.RFC3339),
			FinishedAt:  finished.Format(time.RFC3339),
		})
	}
}

// executeWithCleanup performs the deploy → prompt → monitor → teardown cycle.
// It always attempts to stop the team, even on error.
func (e *Executor) executeWithCleanup(ctx context.Context, schedule models.Schedule, runID string) error {
	// Load the team with agents.
	var team models.Team
	if err := e.DB.Preload("Agents").First(&team, "id = ?", schedule.TeamID).Error; err != nil {
		return fmt.Errorf("loading team: %w", err)
	}

	// Update the run with team deployment info.
	e.DB.Model(&models.ScheduleRun{}).Where("id = ?", runID).
		Update("team_deployment_id", team.ID)

	// Only deploy if the team is not already running.
	needsTeardown := false
	if team.Status != models.TeamStatusRunning {
		slog.Info("executor: deploying team", "team_id", team.ID, "team_name", team.Name)

		if err := e.deployTeam(ctx, team); err != nil {
			return fmt.Errorf("deploying team: %w", err)
		}
		needsTeardown = true

		// Wait for team to be running (poll every 5 seconds, up to 5 minutes).
		if err := e.waitForTeamRunning(ctx, team.ID, 5*time.Minute); err != nil {
			// Try to clean up even if deploy failed.
			e.stopTeam(context.Background(), team)
			return fmt.Errorf("waiting for team to be running: %w", err)
		}
	}

	// Ensure we always clean up.
	defer func() {
		if needsTeardown {
			slog.Info("executor: tearing down team", "team_id", team.ID)
			if err := e.stopTeam(context.Background(), team); err != nil {
				slog.Error("executor: failed to stop team during cleanup",
					"team_id", team.ID, "error", err)
			}
		}
	}()

	// FIX #1: Sanitize team name for NATS subjects (must match sidecar/bridge naming).
	sanitizedName := sanitizeTeamName(team.Name)
	slog.Info("executor: sending prompt",
		"team_id", team.ID,
		"team_name", team.Name,
		"sanitized_name", sanitizedName,
		"prompt_length", len(schedule.Prompt),
	)

	// Store prompt in the run record.
	e.DB.Model(&models.ScheduleRun{}).Where("id = ?", runID).
		Update("prompt_sent", schedule.Prompt)

	// Send prompt and wait for response, capturing the response text.
	responseText, err := e.sendPromptAndWait(ctx, sanitizedName, schedule.Prompt, runID)
	if err != nil {
		return fmt.Errorf("prompt/response: %w", err)
	}

	// Store the response in the run record.
	if responseText != "" {
		e.DB.Model(&models.ScheduleRun{}).Where("id = ?", runID).
			Update("response_received", responseText)
	}

	return nil
}

// deployTeam deploys a team using the configured function or default implementation.
func (e *Executor) deployTeam(ctx context.Context, team models.Team) error {
	if e.DeployTeamFunc != nil {
		return e.DeployTeamFunc(ctx, team)
	}

	// Default: update status to deploying and call runtime.
	e.DB.Model(&team).Update("status", models.TeamStatusDeploying)

	// Deploy infrastructure.
	infraCfg := runtime.InfraConfig{
		TeamName:      team.Name,
		NATSEnabled:   true,
		WorkspacePath: team.WorkspacePath,
	}

	if err := e.Runtime.DeployInfra(ctx, infraCfg); err != nil {
		e.DB.Model(&team).Update("status", models.TeamStatusError)
		return fmt.Errorf("deploying infrastructure: %w", err)
	}

	provider := team.Provider
	if provider == "" {
		provider = models.ProviderClaude
	}

	// Load settings from DB for environment variables.
	env := map[string]string{}
	if e.LoadSettingsEnvFunc != nil {
		env = e.LoadSettingsEnvFunc()
	}

	natsURL := e.Runtime.GetNATSURL(team.Name)

	// Find the leader and extract leader skills.
	var leader *models.Agent
	var leaderSkills json.RawMessage
	var leaderSkillConfigs []protocol.SkillConfig
	for i := range team.Agents {
		if team.Agents[i].Role == models.AgentRoleLeader {
			leader = &team.Agents[i]
			if len(leader.SubAgentSkills) > 0 && string(leader.SubAgentSkills) != "null" {
				leaderSkills = json.RawMessage(leader.SubAgentSkills)
				_ = json.Unmarshal(leader.SubAgentSkills, &leaderSkillConfigs)
			}
			break
		}
	}
	if leader == nil {
		e.DB.Model(&team).Update("status", models.TeamStatusError)
		return fmt.Errorf("no leader agent found in team")
	}

	// Build team member list for the leader's instructions.
	var teamMembers []runtime.TeamMemberInfo
	for _, a := range team.Agents {
		teamMembers = append(teamMembers, runtime.TeamMemberInfo{
			Name:      sanitizeTeamName(a.Name),
			Role:      a.Role,
			Specialty: a.Specialty,
		})
	}

	// Generate sub-agent files for workers based on provider.
	subAgentFiles := map[string]string{}
	var openCodeWorkers []runtime.SubAgentInfo
	for i := range team.Agents {
		agent := &team.Agents[i]
		if agent.Role == models.AgentRoleLeader {
			continue
		}

		if provider == models.ProviderOpenCode {
			subInfo := runtime.SubAgentInfo{
				Name:        agent.Name,
				Description: agent.SubAgentDescription,
				Model:       agent.SubAgentModel,
				Skills:      json.RawMessage(agent.SubAgentSkills),
				ClaudeMD:    agent.InstructionsMD,
			}
			filename := runtime.SubAgentFileName(agent.Name)
			subAgentFiles[filename] = runtime.GenerateOpenCodeSubAgentContent(subInfo, leaderSkillConfigs)
			openCodeWorkers = append(openCodeWorkers, subInfo)
		} else {
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
					slog.Error("executor: failed to setup sub-agent file", "agent", agent.Name, "error", err)
				}
			}
		}
	}

	// Setup host workspace for the leader based on provider.
	if team.WorkspacePath != "" {
		if provider == models.ProviderOpenCode {
			leaderSub := runtime.SubAgentInfo{
				Name:        leader.Name,
				Description: leader.Specialty,
				Skills:      json.RawMessage(leader.Skills),
				ClaudeMD:    leader.InstructionsMD,
			}
			if err := runtime.SetupOpenCodeWorkspace(team.WorkspacePath, team.Name, leaderSub, openCodeWorkers, leaderSkillConfigs); err != nil {
				slog.Error("executor: failed to setup opencode workspace", "team", team.Name, "error", err)
			}
		} else {
			info := runtime.AgentWorkspaceInfo{
				Name:         leader.Name,
				Role:         leader.Role,
				Specialty:    leader.Specialty,
				SystemPrompt: leader.SystemPrompt,
				ClaudeMD:     leader.InstructionsMD,
				Skills:       json.RawMessage(leader.Skills),
				TeamMembers:  teamMembers,
			}
			if _, err := runtime.SetupAgentWorkspace(team.WorkspacePath, info); err != nil {
				slog.Error("executor: failed to setup agent workspace", "agent", leader.Name, "error", err)
			}
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
		}
	}
	if len(allSkills) > 0 {
		skillsJSON, _ := json.Marshal(allSkills)
		env["AGENT_SKILLS_INSTALL"] = string(skillsJSON)
	}

	// Set model env var based on provider.
	leaderModel := leader.SubAgentModel
	if leaderModel != "" && leaderModel != "inherit" {
		if provider == models.ProviderOpenCode {
			env["OPENCODE_MODEL"] = leaderModel
		} else {
			if fullModel := schedulerClaudeModelID(leaderModel); fullModel != "" {
				env["CLAUDE_MODEL"] = fullModel
			}
		}
	} else if provider == models.ProviderOpenCode {
		if m := env["OPENCODE_MODEL"]; m != "" {
			env["OPENCODE_MODEL"] = m
		}
	}

	// Validate OpenCode model credentials before deployment.
	if provider == models.ProviderOpenCode {
		effectiveModel := env["OPENCODE_MODEL"]
		if err := schedulerValidateOpenCodeCredentials(effectiveModel, env); err != nil {
			e.DB.Model(&team).Update("status", models.TeamStatusError)
			return fmt.Errorf("credential validation: %w", err)
		}
		for _, a := range team.Agents {
			if a.Role == models.AgentRoleWorker && a.SubAgentModel != "" && a.SubAgentModel != "inherit" {
				if err := schedulerValidateOpenCodeCredentials(a.SubAgentModel, env); err != nil {
					e.DB.Model(&team).Update("status", models.TeamStatusError)
					return fmt.Errorf("credential validation for worker %s: %w", a.Name, err)
				}
			}
		}
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

	agentCfg := runtime.AgentConfig{
		Name:          leader.Name,
		TeamName:      team.Name,
		Role:          leader.Role,
		Provider:      provider,
		SystemPrompt:  leader.SystemPrompt,
		ClaudeMD:      instructionsMDContent,
		NATSUrl:       natsURL,
		WorkspacePath: team.WorkspacePath,
		SubAgentFiles: subAgentFiles,
		Env:           env,
	}

	instance, err := e.Runtime.DeployAgent(ctx, agentCfg)
	if err != nil {
		e.DB.Model(&team).Update("status", models.TeamStatusError)
		return fmt.Errorf("deploying leader: %w", err)
	}

	e.DB.Model(leader).Updates(map[string]interface{}{
		"container_id":     instance.ID,
		"container_status": models.ContainerStatusRunning,
	})
	e.DB.Model(&team).Update("status", models.TeamStatusRunning)

	return nil
}

// schedulerClaudeModelID maps short model names to full Claude model IDs.
func schedulerClaudeModelID(short string) string {
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

// schedulerValidateOpenCodeCredentials checks that the required API key for the
// given OpenCode model is present in the environment.
func schedulerValidateOpenCodeCredentials(model string, env map[string]string) error {
	if model == "" || model == "inherit" {
		return nil
	}
	parts := strings.SplitN(model, "/", 2)
	if len(parts) < 2 {
		return nil
	}
	required := map[string]string{
		"anthropic": "ANTHROPIC_API_KEY",
		"openai":    "OPENAI_API_KEY",
		"google":    "GOOGLE_GENERATIVE_AI_API_KEY",
		"ollama":    "OLLAMA_BASE_URL",
		"lmstudio":  "LM_STUDIO_BASE_URL",
	}
	key, ok := required[parts[0]]
	if !ok {
		return nil
	}
	if env[key] == "" {
		return fmt.Errorf("missing credential for model %q: set %s in the Settings page", model, key)
	}
	return nil
}

// stopTeam stops a running team.
func (e *Executor) stopTeam(ctx context.Context, team models.Team) error {
	if e.StopTeamFunc != nil {
		return e.StopTeamFunc(ctx, team)
	}

	if err := e.Runtime.TeardownInfra(ctx, team.Name); err != nil {
		slog.Error("executor: teardown failed", "team", team.Name, "error", err)
	}

	// Clear container state for leader.
	for i := range team.Agents {
		if team.Agents[i].Role == models.AgentRoleLeader {
			e.DB.Model(&team.Agents[i]).Updates(map[string]interface{}{
				"container_id":     "",
				"container_status": models.ContainerStatusStopped,
			})
			break
		}
	}

	e.DB.Model(&team).Update("status", models.TeamStatusStopped)
	return nil
}

// sendPromptAndWait connects to the team's NATS, subscribes to the leader
// channel for a response, sends the prompt, and blocks until a
// TypeLeaderResponse is received or the context expires.
// Returns the response text (result or error) from the leader.
// teamName must already be sanitized for NATS subject compatibility.
func (e *Executor) sendPromptAndWait(ctx context.Context, teamName, message, runID string) (string, error) {
	// If both injectable functions are provided, use them (for testing).
	if e.SendPromptFunc != nil && e.WaitForResponseFunc != nil {
		if err := e.SendPromptFunc(ctx, teamName, message); err != nil {
			return "", fmt.Errorf("sending prompt: %w", err)
		}
		if err := e.WaitForResponseFunc(ctx, teamName); err != nil {
			return "", fmt.Errorf("waiting for response: %w", err)
		}
		return "", nil
	}

	natsURL, err := e.Runtime.GetNATSConnectURL(ctx, teamName)
	if err != nil {
		return "", fmt.Errorf("resolving NATS URL: %w", err)
	}

	token := os.Getenv("NATS_AUTH_TOKEN")
	opts := []nats.Option{
		nats.Name("agentcrew-scheduler"),
		nats.Timeout(5 * time.Second),
	}
	if token != "" {
		opts = append(opts, nats.Token(token))
	}

	nc, err := nats.Connect(natsURL, opts...)
	if err != nil {
		return "", fmt.Errorf("connecting to NATS: %w", err)
	}
	defer nc.Close()

	// Subscribe to the leader channel BEFORE sending the prompt to avoid
	// missing the response in a race.
	subject, err := protocol.TeamLeaderChannel(teamName)
	if err != nil {
		return "", fmt.Errorf("building leader channel: %w", err)
	}

	slog.Info("executor: subscribing to NATS subject",
		"subject", subject, "team_name", teamName, "run_id", runID)

	type leaderResult struct {
		text string
	}
	responseCh := make(chan leaderResult, 1)
	sub, err := nc.Subscribe(subject, func(msg *nats.Msg) {
		var protoMsg protocol.Message
		if err := json.Unmarshal(msg.Data, &protoMsg); err != nil {
			slog.Warn("executor: failed to unmarshal NATS message",
				"subject", subject, "error", err)
			return
		}

		slog.Debug("executor: received NATS message",
			"subject", subject, "type", protoMsg.Type,
			"from", protoMsg.From, "to", protoMsg.To)

		if protoMsg.Type == protocol.TypeLeaderResponse {
			// Extract the response text from the payload.
			var payload protocol.LeaderResponsePayload
			responseText := ""
			if err := json.Unmarshal(protoMsg.Payload, &payload); err == nil {
				if payload.Error != "" {
					responseText = "Error: " + payload.Error
				} else {
					responseText = payload.Result
				}
			}

			// Strict filter: only accept responses tagged with our exact run ID.
			// Responses from chat (empty ScheduledRunID) or other scheduled
			// runs are ignored.
			if payload.ScheduledRunID != runID {
				slog.Debug("executor: ignoring response for different run",
					"expected_run_id", runID, "got_run_id", payload.ScheduledRunID)
				return
			}

			slog.Info("executor: received leader response",
				"subject", subject, "status", payload.Status,
				"run_id", runID, "response_length", len(responseText))

			select {
			case responseCh <- leaderResult{text: responseText}:
			default:
			}
		}
	})
	if err != nil {
		return "", fmt.Errorf("subscribing to leader channel: %w", err)
	}
	defer sub.Unsubscribe()

	// Build and send the prompt with scheduler metadata.
	protoMsg, err := protocol.NewMessage("scheduler", "leader", protocol.TypeUserMessage, protocol.UserMessagePayload{
		Content:        message,
		Source:         "scheduler",
		ScheduledRunID: runID,
	})
	if err != nil {
		return "", fmt.Errorf("building protocol message: %w", err)
	}

	data, err := json.Marshal(protoMsg)
	if err != nil {
		return "", fmt.Errorf("marshaling message: %w", err)
	}

	if err := nc.Publish(subject, data); err != nil {
		return "", fmt.Errorf("publishing prompt: %w", err)
	}
	if err := nc.Flush(); err != nil {
		return "", fmt.Errorf("flushing prompt: %w", err)
	}

	slog.Info("executor: prompt sent, waiting for leader response via NATS",
		"team", teamName, "subject", subject, "run_id", runID)

	// Wait for the response or context cancellation.
	select {
	case result := <-responseCh:
		return result.text, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// pollInterval returns the configured poll interval or the default (10s).
func (e *Executor) pollInterval() time.Duration {
	if e.PollInterval > 0 {
		return e.PollInterval
	}
	return 10 * time.Second
}

// waitForTeamRunning polls the team status until it's "running" or the context expires.
func (e *Executor) waitForTeamRunning(ctx context.Context, teamID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	pollInt := e.pollInterval()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for team to be running")
		}

		var team models.Team
		if err := e.DB.First(&team, "id = ?", teamID).Error; err != nil {
			return fmt.Errorf("querying team: %w", err)
		}

		switch team.Status {
		case models.TeamStatusRunning:
			return nil
		case models.TeamStatusError:
			return fmt.Errorf("team entered error state")
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInt):
		}
	}
}

// markScheduleError sets a schedule to error status.
func (e *Executor) markScheduleError(scheduleID, errMsg string) {
	e.DB.Model(&models.Schedule{}).Where("id = ?", scheduleID).
		Update("status", models.ScheduleStatusError)
	slog.Error("executor: schedule error", "schedule_id", scheduleID, "error", errMsg)
}

// invalidSlugChars matches any character that is not lowercase alphanumeric, hyphen, or underscore.
var invalidSlugChars = regexp.MustCompile(`[^a-z0-9_-]`)

// sanitizeTeamName converts a display name into a Docker/K8s/NATS-safe slug.
// This must produce the same output as api.SanitizeName to match the sidecar's naming.
func sanitizeTeamName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, " ", "-")
	s = invalidSlugChars.ReplaceAllString(s, "")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-")
	if len(s) > 62 {
		s = s[:62]
		s = strings.TrimRight(s, "-")
	}
	if s == "" {
		s = "team"
	}
	return s
}

// sanitizeError removes sensitive information from error messages before
// storing them in the database. It redacts tokens, URLs with credentials,
// and internal paths.
func sanitizeError(errMsg string) string {
	// Redact NATS auth tokens.
	if token := os.Getenv("NATS_AUTH_TOKEN"); token != "" && len(token) > 4 {
		errMsg = strings.ReplaceAll(errMsg, token, "[REDACTED]")
	}

	// Redact nats:// URLs that may contain credentials.
	// Pattern: nats://token@host:port → nats://[REDACTED]@host:port
	if idx := strings.Index(errMsg, "nats://"); idx >= 0 {
		rest := errMsg[idx+7:]
		if atIdx := strings.Index(rest, "@"); atIdx >= 0 {
			errMsg = errMsg[:idx+7] + "[REDACTED]" + rest[atIdx:]
		}
	}

	return errMsg
}
