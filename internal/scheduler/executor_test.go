package scheduler

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/helmcode/agent-crew/internal/models"
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
		DB:      db,
		Timeout: 30 * time.Second,
		DeployTeamFunc: func(ctx context.Context, team models.Team) error {
			db.Model(&team).Update("status", models.TeamStatusRunning)
			return nil
		},
		StopTeamFunc: func(ctx context.Context, team models.Team) error {
			db.Model(&team).Update("status", models.TeamStatusStopped)
			return nil
		},
		SendPromptFunc: func(ctx context.Context, teamName, message string) error {
			return nil
		},
		WaitForResponseFunc: func(ctx context.Context, teamName string) error {
			// Simulate a short delay then success.
			time.Sleep(50 * time.Millisecond)
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
			return nil
		},
		WaitForResponseFunc: func(ctx context.Context, teamName string) error {
			// Never respond — wait for context to expire.
			<-ctx.Done()
			return ctx.Err()
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
		DB:      db,
		Timeout: 30 * time.Second,
		DeployTeamFunc: func(ctx context.Context, team models.Team) error {
			deployCalled = true
			return nil
		},
		StopTeamFunc: func(ctx context.Context, team models.Team) error {
			return nil
		},
		SendPromptFunc: func(ctx context.Context, teamName, message string) error {
			return nil
		},
		WaitForResponseFunc: func(ctx context.Context, teamName string) error {
			time.Sleep(50 * time.Millisecond)
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
		WaitForResponseFunc: func(ctx context.Context, teamName string) error {
			return nil
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
		StopTeamFunc: func(ctx context.Context, team models.Team) error {
			return nil
		},
	}

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

func TestExecutor_Execute_StoresPromptAndResponse(t *testing.T) {
	db, err := models.InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	team := models.Team{
		ID:      "team-ex7",
		Name:    "store-team",
		Status:  models.TeamStatusStopped,
		Runtime: "docker",
	}
	db.Create(&team)
	db.Create(&models.Agent{
		ID:     "agent-ex7",
		TeamID: "team-ex7",
		Name:   "leader",
		Role:   models.AgentRoleLeader,
	})

	schedule := models.Schedule{
		ID:             "sched-ex7",
		Name:           "store-test",
		TeamID:         "team-ex7",
		Prompt:         "What is 2+2?",
		CronExpression: "* * * * *",
		Timezone:       "UTC",
		Enabled:        true,
		Status:         models.ScheduleStatusRunning,
	}
	db.Create(&schedule)

	executor := &Executor{
		DB:      db,
		Timeout: 30 * time.Second,
		DeployTeamFunc: func(ctx context.Context, team models.Team) error {
			db.Model(&team).Update("status", models.TeamStatusRunning)
			return nil
		},
		StopTeamFunc: func(ctx context.Context, team models.Team) error {
			db.Model(&team).Update("status", models.TeamStatusStopped)
			return nil
		},
		SendPromptFunc: func(ctx context.Context, teamName, message string) error {
			return nil
		},
		WaitForResponseFunc: func(ctx context.Context, teamName string) error {
			return nil
		},
	}

	executor.Execute(context.Background(), schedule)

	var runs []models.ScheduleRun
	db.Where("schedule_id = ?", "sched-ex7").Find(&runs)
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	if runs[0].PromptSent != "What is 2+2?" {
		t.Errorf("expected prompt_sent 'What is 2+2?', got %q", runs[0].PromptSent)
	}
}

func TestExecutor_Execute_SanitizesTeamName(t *testing.T) {
	db, err := models.InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	// Use a display name with spaces to verify sanitization.
	team := models.Team{
		ID:      "team-ex8",
		Name:    "My Test Team",
		Status:  models.TeamStatusRunning,
		Runtime: "docker",
	}
	db.Create(&team)
	db.Create(&models.Agent{
		ID:     "agent-ex8",
		TeamID: "team-ex8",
		Name:   "leader",
		Role:   models.AgentRoleLeader,
	})

	schedule := models.Schedule{
		ID:             "sched-ex8",
		Name:           "sanitize-test",
		TeamID:         "team-ex8",
		Prompt:         "test prompt",
		CronExpression: "* * * * *",
		Timezone:       "UTC",
		Enabled:        true,
		Status:         models.ScheduleStatusRunning,
	}
	db.Create(&schedule)

	var capturedTeamName string
	executor := &Executor{
		DB:      db,
		Timeout: 30 * time.Second,
		StopTeamFunc: func(ctx context.Context, team models.Team) error {
			return nil
		},
		SendPromptFunc: func(ctx context.Context, teamName, message string) error {
			capturedTeamName = teamName
			return nil
		},
		WaitForResponseFunc: func(ctx context.Context, teamName string) error {
			return nil
		},
	}

	executor.Execute(context.Background(), schedule)

	// The team name passed to SendPromptFunc should be sanitized.
	expected := "my-test-team"
	if capturedTeamName != expected {
		t.Errorf("expected sanitized team name %q, got %q", expected, capturedTeamName)
	}
}

func TestSanitizeTeamName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"My Team", "my-team"},
		{"simple", "simple"},
		{"Hello  World", "hello-world"},
		{"test@team!", "testteam"},
		{"", "team"},
		{"  spaces  ", "spaces"},
		{"UPPER-case", "upper-case"},
	}

	for _, tt := range tests {
		got := sanitizeTeamName(tt.input)
		if got != tt.expected {
			t.Errorf("sanitizeTeamName(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
