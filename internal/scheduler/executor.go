package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
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
			runUpdates["error"] = err.Error()
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

	// Send the prompt.
	slog.Info("executor: sending prompt", "team_id", team.ID, "prompt_length", len(schedule.Prompt))
	if err := e.sendPrompt(ctx, team.Name, schedule.Prompt); err != nil {
		return fmt.Errorf("sending prompt: %w", err)
	}

	// Wait for the agent to respond. We poll for a leader_response message
	// in the task logs that was created after the prompt was sent.
	promptSentAt := time.Now()
	if err := e.waitForResponse(ctx, team.ID, promptSentAt); err != nil {
		return fmt.Errorf("waiting for response: %w", err)
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
		ClaudeMD:      leader.ClaudeMD,
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

// sendPrompt sends a prompt to the team via NATS.
func (e *Executor) sendPrompt(ctx context.Context, teamName, message string) error {
	if e.SendPromptFunc != nil {
		return e.SendPromptFunc(ctx, teamName, message)
	}

	natsURL, err := e.Runtime.GetNATSConnectURL(ctx, teamName)
	if err != nil {
		return fmt.Errorf("resolving NATS URL: %w", err)
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
		return fmt.Errorf("connecting to NATS: %w", err)
	}
	defer nc.Close()

	msg, err := protocol.NewMessage("scheduler", "leader", protocol.TypeUserMessage, protocol.UserMessagePayload{
		Content: message,
	})
	if err != nil {
		return fmt.Errorf("building protocol message: %w", err)
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshaling message: %w", err)
	}

	subject, err := protocol.TeamLeaderChannel(teamName)
	if err != nil {
		return fmt.Errorf("building leader channel: %w", err)
	}

	if err := nc.Publish(subject, data); err != nil {
		return fmt.Errorf("publishing: %w", err)
	}
	return nc.Flush()
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

// waitForResponse polls for a leader_response message in the task logs.
// It waits up to the context deadline for the agent to reply.
func (e *Executor) waitForResponse(ctx context.Context, teamID string, after time.Time) error {
	pollInt := e.pollInterval()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		var count int64
		e.DB.Model(&models.TaskLog{}).
			Where("team_id = ? AND message_type = ? AND created_at > ?",
				teamID, string(protocol.TypeLeaderResponse), after).
			Count(&count)

		if count > 0 {
			return nil
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
