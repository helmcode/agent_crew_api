package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/helmcode/agent-crew/internal/models"
	"github.com/helmcode/agent-crew/internal/protocol"
)

func TestExecutor_Execute_Success(t *testing.T) {
	db, err := models.InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	team := models.Team{
		ID:      "team-ex1",
		Name:    "exec-team",
		Status:  models.TeamStatusStopped,
		Runtime: "docker",
	}
	db.Create(&team)
	db.Create(&models.Agent{
		ID:     "agent-ex1",
		TeamID: "team-ex1",
		Name:   "leader",
		Role:   models.AgentRoleLeader,
	})

	schedule := models.Schedule{
		ID:             "sched-ex1",
		Name:           "test-exec",
		TeamID:         "team-ex1",
		Prompt:         "Run the test",
		CronExpression: "* * * * *",
		Timezone:       "UTC",
		Enabled:        true,
		Status:         models.ScheduleStatusRunning,
	}
	db.Create(&schedule)

	executor := &Executor{
		DB:           db,
		Timeout:      30 * time.Second,
		PollInterval: 200 * time.Millisecond,
		DeployTeamFunc: func(ctx context.Context, team models.Team) error {
			// Simulate successful deploy.
			db.Model(&team).Update("status", models.TeamStatusRunning)
			return nil
		},
		StopTeamFunc: func(ctx context.Context, team models.Team) error {
			db.Model(&team).Update("status", models.TeamStatusStopped)
			return nil
		},
		SendPromptFunc: func(ctx context.Context, teamName, message string) error {
			// Simulate sending prompt and receiving a response after a short delay.
			go func() {
				time.Sleep(100 * time.Millisecond)
				content, _ := json.Marshal(map[string]string{"content": "response"})
				db.Create(&models.TaskLog{
					ID:          "log-ex1",
					TeamID:      "team-ex1",
					FromAgent:   "leader",
					ToAgent:     "user",
					MessageType: string(protocol.TypeLeaderResponse),
					Payload:     models.JSON(content),
				})
			}()
			return nil
		},
	}

	executor.Execute(context.Background(), schedule)

	// Verify a ScheduleRun was created and succeeded.
	var runs []models.ScheduleRun
	db.Where("schedule_id = ?", "sched-ex1").Find(&runs)
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	if runs[0].Status != models.ScheduleRunStatusSuccess {
		t.Errorf("expected run status 'success', got %q", runs[0].Status)
	}
	if runs[0].FinishedAt == nil {
		t.Error("expected finished_at to be set")
	}
	if runs[0].Error != "" {
		t.Errorf("expected no error, got %q", runs[0].Error)
	}

	// Verify team was stopped (cleanup).
	var updatedTeam models.Team
	db.First(&updatedTeam, "id = ?", "team-ex1")
	if updatedTeam.Status != models.TeamStatusStopped {
		t.Errorf("expected team status 'stopped' after cleanup, got %q", updatedTeam.Status)
	}
}

func TestExecutor_Execute_DeployFailure(t *testing.T) {
	db, err := models.InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	team := models.Team{
		ID:      "team-ex2",
		Name:    "fail-team",
		Status:  models.TeamStatusStopped,
		Runtime: "docker",
	}
	db.Create(&team)
	db.Create(&models.Agent{
		ID:     "agent-ex2",
		TeamID: "team-ex2",
		Name:   "leader",
		Role:   models.AgentRoleLeader,
	})

	schedule := models.Schedule{
		ID:             "sched-ex2",
		Name:           "fail-exec",
		TeamID:         "team-ex2",
		Prompt:         "Run it",
		CronExpression: "* * * * *",
		Timezone:       "UTC",
		Enabled:        true,
		Status:         models.ScheduleStatusRunning,
	}
	db.Create(&schedule)

	executor := &Executor{
		DB:      db,
		Timeout: 10 * time.Second,
		DeployTeamFunc: func(ctx context.Context, team models.Team) error {
			return fmt.Errorf("docker daemon not available")
		},
		StopTeamFunc: func(ctx context.Context, team models.Team) error {
			return nil
		},
	}

	executor.Execute(context.Background(), schedule)

	var runs []models.ScheduleRun
	db.Where("schedule_id = ?", "sched-ex2").Find(&runs)
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	if runs[0].Status != models.ScheduleRunStatusFailed {
		t.Errorf("expected run status 'failed', got %q", runs[0].Status)
	}
	if runs[0].Error == "" {
		t.Error("expected error message to be set")
	}
}

func TestExecutor_Execute_Timeout(t *testing.T) {
	db, err := models.InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	team := models.Team{
		ID:      "team-ex3",
		Name:    "timeout-team",
		Status:  models.TeamStatusStopped,
		Runtime: "docker",
	}
	db.Create(&team)
	db.Create(&models.Agent{
		ID:     "agent-ex3",
		TeamID: "team-ex3",
		Name:   "leader",
		Role:   models.AgentRoleLeader,
	})

	schedule := models.Schedule{
		ID:             "sched-ex3",
		Name:           "timeout-exec",
		TeamID:         "team-ex3",
		Prompt:         "Run it",
		CronExpression: "* * * * *",
		Timezone:       "UTC",
		Enabled:        true,
		Status:         models.ScheduleStatusRunning,
	}
	db.Create(&schedule)

	executor := &Executor{
		DB:      db,
		Timeout: 500 * time.Millisecond, // Very short timeout.
		DeployTeamFunc: func(ctx context.Context, team models.Team) error {
			db.Model(&team).Update("status", models.TeamStatusRunning)
			return nil
		},
		StopTeamFunc: func(ctx context.Context, team models.Team) error {
			db.Model(&team).Update("status", models.TeamStatusStopped)
			return nil
		},
		SendPromptFunc: func(ctx context.Context, teamName, message string) error {
			// Don't create a response — let it time out waiting.
			return nil
		},
	}

	executor.Execute(context.Background(), schedule)

	var runs []models.ScheduleRun
	db.Where("schedule_id = ?", "sched-ex3").Find(&runs)
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	if runs[0].Status != models.ScheduleRunStatusTimeout {
		t.Errorf("expected run status 'timeout', got %q", runs[0].Status)
	}
}

func TestExecutor_Execute_TeamAlreadyRunning(t *testing.T) {
	db, err := models.InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	team := models.Team{
		ID:      "team-ex4",
		Name:    "already-running",
		Status:  models.TeamStatusRunning, // Already running.
		Runtime: "docker",
	}
	db.Create(&team)
	db.Create(&models.Agent{
		ID:     "agent-ex4",
		TeamID: "team-ex4",
		Name:   "leader",
		Role:   models.AgentRoleLeader,
	})

	schedule := models.Schedule{
		ID:             "sched-ex4",
		Name:           "no-deploy",
		TeamID:         "team-ex4",
		Prompt:         "Just send prompt",
		CronExpression: "* * * * *",
		Timezone:       "UTC",
		Enabled:        true,
		Status:         models.ScheduleStatusRunning,
	}
	db.Create(&schedule)

	deployCalled := false
	executor := &Executor{
		DB:           db,
		Timeout:      30 * time.Second,
		PollInterval: 200 * time.Millisecond,
		DeployTeamFunc: func(ctx context.Context, team models.Team) error {
			deployCalled = true
			return nil
		},
		StopTeamFunc: func(ctx context.Context, team models.Team) error {
			return nil
		},
		SendPromptFunc: func(ctx context.Context, teamName, message string) error {
			go func() {
				time.Sleep(100 * time.Millisecond)
				content, _ := json.Marshal(map[string]string{"content": "done"})
				db.Create(&models.TaskLog{
					ID:          "log-ex4",
					TeamID:      "team-ex4",
					FromAgent:   "leader",
					ToAgent:     "user",
					MessageType: string(protocol.TypeLeaderResponse),
					Payload:     models.JSON(content),
				})
			}()
			return nil
		},
	}

	executor.Execute(context.Background(), schedule)

	if deployCalled {
		t.Error("expected deploy NOT to be called for already-running team")
	}

	var runs []models.ScheduleRun
	db.Where("schedule_id = ?", "sched-ex4").Find(&runs)
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	if runs[0].Status != models.ScheduleRunStatusSuccess {
		t.Errorf("expected run status 'success', got %q", runs[0].Status)
	}
}

func TestExecutor_Execute_PromptFailure(t *testing.T) {
	db, err := models.InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	team := models.Team{
		ID:      "team-ex5",
		Name:    "prompt-fail",
		Status:  models.TeamStatusStopped,
		Runtime: "docker",
	}
	db.Create(&team)
	db.Create(&models.Agent{
		ID:     "agent-ex5",
		TeamID: "team-ex5",
		Name:   "leader",
		Role:   models.AgentRoleLeader,
	})

	schedule := models.Schedule{
		ID:             "sched-ex5",
		Name:           "prompt-fail",
		TeamID:         "team-ex5",
		Prompt:         "Fail here",
		CronExpression: "* * * * *",
		Timezone:       "UTC",
		Enabled:        true,
		Status:         models.ScheduleStatusRunning,
	}
	db.Create(&schedule)

	executor := &Executor{
		DB:      db,
		Timeout: 10 * time.Second,
		DeployTeamFunc: func(ctx context.Context, team models.Team) error {
			db.Model(&team).Update("status", models.TeamStatusRunning)
			return nil
		},
		StopTeamFunc: func(ctx context.Context, team models.Team) error {
			db.Model(&team).Update("status", models.TeamStatusStopped)
			return nil
		},
		SendPromptFunc: func(ctx context.Context, teamName, message string) error {
			return fmt.Errorf("NATS connection refused")
		},
	}

	executor.Execute(context.Background(), schedule)

	var runs []models.ScheduleRun
	db.Where("schedule_id = ?", "sched-ex5").Find(&runs)
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	if runs[0].Status != models.ScheduleRunStatusFailed {
		t.Errorf("expected run status 'failed', got %q", runs[0].Status)
	}
	if runs[0].Error == "" {
		t.Error("expected error message")
	}

	// Verify team was cleaned up.
	var updatedTeam models.Team
	db.First(&updatedTeam, "id = ?", "team-ex5")
	if updatedTeam.Status != models.TeamStatusStopped {
		t.Errorf("expected team to be stopped after cleanup, got %q", updatedTeam.Status)
	}
}

func TestExecutor_Execute_NoLeaderAgent(t *testing.T) {
	db, err := models.InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	// Create team with only worker agents (no leader).
	team := models.Team{ID: "team-ex6", Name: "no-leader", Status: models.TeamStatusStopped, Runtime: "docker"}
	db.Create(&team)
	db.Create(&models.Agent{
		ID:     "agent-ex6",
		TeamID: "team-ex6",
		Name:   "worker",
		Role:   models.AgentRoleWorker,
	})

	schedule := models.Schedule{
		ID:             "sched-ex6",
		Name:           "no-leader-exec",
		TeamID:         "team-ex6",
		Prompt:         "test",
		CronExpression: "* * * * *",
		Timezone:       "UTC",
		Enabled:        true,
		Status:         models.ScheduleStatusRunning,
	}
	db.Create(&schedule)

	executor := &Executor{
		DB:           db,
		Timeout:      10 * time.Second,
		PollInterval: 200 * time.Millisecond,
		// Use default deploy which will fail because no leader.
		StopTeamFunc: func(ctx context.Context, team models.Team) error {
			return nil
		},
	}

	// DeployTeamFunc will use default which creates infra then looks for leader.
	// Since there's no runtime, we mock the deploy to simulate the no-leader error.
	executor.DeployTeamFunc = func(ctx context.Context, team models.Team) error {
		return fmt.Errorf("no leader agent found in team")
	}

	executor.Execute(context.Background(), schedule)

	var runs []models.ScheduleRun
	db.Where("schedule_id = ?", "sched-ex6").Find(&runs)
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	if runs[0].Status != models.ScheduleRunStatusFailed {
		t.Errorf("expected run status 'failed', got %q", runs[0].Status)
	}
	if runs[0].Error == "" {
		t.Error("expected error message about no leader")
	}
}

func TestNewExecutor_DefaultTimeout(t *testing.T) {
	db, err := models.InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	executor := NewExecutor(db, nil)
	if executor.Timeout != DefaultTimeout {
		t.Errorf("expected default timeout %v, got %v", DefaultTimeout, executor.Timeout)
	}
}
