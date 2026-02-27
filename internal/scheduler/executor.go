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
		DB:      db,
		Runtime: rt,
		Timeout: timeout,
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

	// Simplified deployment — just deploy infrastructure and leader.
	infraCfg := runtime.InfraConfig{
		TeamName:      team.Name,
		NATSEnabled:   true,
		WorkspacePath: team.WorkspacePath,
	}

	if err := e.Runtime.DeployInfra(ctx, infraCfg); err != nil {
		e.DB.Model(&team).Update("status", models.TeamStatusError)
		return fmt.Errorf("deploying infrastructure: %w", err)
	}

	// Find the leader.
	var leader *models.Agent
	for i := range team.Agents {
		if team.Agents[i].Role == models.AgentRoleLeader {
			leader = &team.Agents[i]
			break
		}
	}
	if leader == nil {
		e.DB.Model(&team).Update("status", models.TeamStatusError)
		return fmt.Errorf("no leader agent found in team")
	}

	env := map[string]string{}
	if e.LoadSettingsEnvFunc != nil {
		env = e.LoadSettingsEnvFunc()
	}

	natsURL := e.Runtime.GetNATSURL(team.Name)
	agentCfg := runtime.AgentConfig{
		Name:          leader.Name,
		TeamName:      team.Name,
		Role:          leader.Role,
		SystemPrompt:  leader.SystemPrompt,
		ClaudeMD:      leader.InstructionsMD,
		NATSUrl:       natsURL,
		WorkspacePath: team.WorkspacePath,
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
