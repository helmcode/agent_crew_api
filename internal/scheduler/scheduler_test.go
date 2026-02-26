package scheduler

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/helmcode/agent-crew/internal/models"
)

func TestScheduler_TickExecutesDueSchedules(t *testing.T) {
	db, err := models.InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	// Create a team.
	team := models.Team{ID: "team-s1", Name: "sched-test-team", Status: models.TeamStatusStopped, Runtime: "docker"}
	db.Create(&team)

	// Create an enabled schedule that matches every minute.
	schedule := models.Schedule{
		ID:             "sched-s1",
		Name:           "every-minute",
		TeamID:         "team-s1",
		Prompt:         "Run the test",
		CronExpression: "* * * * *",
		Timezone:       "UTC",
		Enabled:        true,
		Status:         models.ScheduleStatusIdle,
	}
	db.Create(&schedule)

	var mu sync.Mutex
	var executed []string

	executeFn := func(ctx context.Context, sched models.Schedule) {
		mu.Lock()
		defer mu.Unlock()
		executed = append(executed, sched.ID)
	}

	sched := New(db, executeFn, 100*time.Millisecond)
	sched.Start()

	// Wait for at least one tick.
	time.Sleep(250 * time.Millisecond)
	sched.Stop()

	mu.Lock()
	defer mu.Unlock()
	if len(executed) == 0 {
		t.Error("expected at least one schedule execution")
	}
	if executed[0] != "sched-s1" {
		t.Errorf("expected executed schedule 'sched-s1', got %q", executed[0])
	}

	// Verify status was updated back to idle.
	var updated models.Schedule
	db.First(&updated, "id = ?", "sched-s1")
	if updated.Status != models.ScheduleStatusIdle {
		t.Errorf("expected status 'idle' after execution, got %q", updated.Status)
	}
	if updated.LastRunAt == nil {
		t.Error("expected last_run_at to be set")
	}
}

func TestScheduler_SkipsDisabledSchedules(t *testing.T) {
	db, err := models.InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	team := models.Team{ID: "team-s2", Name: "disabled-team", Status: models.TeamStatusStopped, Runtime: "docker"}
	db.Create(&team)

	// Create a schedule and then disable it explicitly.
	// Note: GORM ignores false bool zero-value on create, so we update after.
	schedule := models.Schedule{
		ID:             "sched-s2",
		Name:           "disabled-sched",
		TeamID:         "team-s2",
		Prompt:         "Should not run",
		CronExpression: "* * * * *",
		Timezone:       "UTC",
		Enabled:        true,
		Status:         models.ScheduleStatusIdle,
	}
	db.Create(&schedule)
	db.Model(&schedule).Update("enabled", false)

	var mu sync.Mutex
	var executed []string

	executeFn := func(ctx context.Context, sched models.Schedule) {
		mu.Lock()
		defer mu.Unlock()
		executed = append(executed, sched.ID)
	}

	sched := New(db, executeFn, 100*time.Millisecond)
	sched.Start()
	time.Sleep(250 * time.Millisecond)
	sched.Stop()

	mu.Lock()
	defer mu.Unlock()
	if len(executed) != 0 {
		t.Errorf("expected no executions for disabled schedule, got %d", len(executed))
	}
}

func TestScheduler_SkipsRunningSchedules(t *testing.T) {
	db, err := models.InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	team := models.Team{ID: "team-s3", Name: "running-team", Status: models.TeamStatusStopped, Runtime: "docker"}
	db.Create(&team)

	// Create a schedule that's already running.
	schedule := models.Schedule{
		ID:             "sched-s3",
		Name:           "running-sched",
		TeamID:         "team-s3",
		Prompt:         "Already running",
		CronExpression: "* * * * *",
		Timezone:       "UTC",
		Enabled:        true,
		Status:         models.ScheduleStatusRunning,
	}
	db.Create(&schedule)

	var mu sync.Mutex
	var executed []string

	executeFn := func(ctx context.Context, sched models.Schedule) {
		mu.Lock()
		defer mu.Unlock()
		executed = append(executed, sched.ID)
	}

	sched := New(db, executeFn, 100*time.Millisecond)
	sched.Start()
	time.Sleep(250 * time.Millisecond)
	sched.Stop()

	mu.Lock()
	defer mu.Unlock()
	if len(executed) != 0 {
		t.Errorf("expected no executions for already-running schedule, got %d", len(executed))
	}
}

func TestScheduler_GracefulShutdown(t *testing.T) {
	db, err := models.InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	executeFn := func(ctx context.Context, sched models.Schedule) {}

	sched := New(db, executeFn, time.Hour)
	sched.Start()

	// Stop should return quickly (not hang).
	done := make(chan struct{})
	go func() {
		sched.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Success.
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return within 2 seconds")
	}
}

func TestScheduler_ParallelExecution(t *testing.T) {
	db, err := models.InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	team := models.Team{ID: "team-s4", Name: "parallel-team", Status: models.TeamStatusStopped, Runtime: "docker"}
	db.Create(&team)

	// Create two schedules that are both due.
	for _, id := range []string{"sched-p1", "sched-p2"} {
		db.Create(&models.Schedule{
			ID:             id,
			Name:           id,
			TeamID:         "team-s4",
			Prompt:         "test",
			CronExpression: "* * * * *",
			Timezone:       "UTC",
			Enabled:        true,
			Status:         models.ScheduleStatusIdle,
		})
	}

	var mu sync.Mutex
	var executed []string

	executeFn := func(ctx context.Context, sched models.Schedule) {
		// Simulate some work.
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		executed = append(executed, sched.ID)
	}

	sched := New(db, executeFn, 100*time.Millisecond)
	sched.Start()
	time.Sleep(300 * time.Millisecond)
	sched.Stop()

	mu.Lock()
	defer mu.Unlock()
	if len(executed) < 2 {
		t.Errorf("expected at least 2 parallel executions, got %d", len(executed))
	}
}

func TestScheduler_UpdatesNextRunAt(t *testing.T) {
	db, err := models.InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	team := models.Team{ID: "team-s5", Name: "nextrun-team", Status: models.TeamStatusStopped, Runtime: "docker"}
	db.Create(&team)

	schedule := models.Schedule{
		ID:             "sched-s5",
		Name:           "nextrun-sched",
		TeamID:         "team-s5",
		Prompt:         "test",
		CronExpression: "* * * * *",
		Timezone:       "UTC",
		Enabled:        true,
		Status:         models.ScheduleStatusIdle,
	}
	db.Create(&schedule)

	executeFn := func(ctx context.Context, sched models.Schedule) {}

	sched := New(db, executeFn, 100*time.Millisecond)
	sched.Start()
	time.Sleep(250 * time.Millisecond)
	sched.Stop()

	var updated models.Schedule
	db.First(&updated, "id = ?", "sched-s5")
	if updated.NextRunAt == nil {
		t.Error("expected next_run_at to be updated after execution")
	}
	if updated.NextRunAt != nil && updated.NextRunAt.Before(time.Now()) {
		t.Error("expected next_run_at to be in the future")
	}
}

func TestScheduler_PanicRecovery(t *testing.T) {
	db, err := models.InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	team := models.Team{ID: "team-panic", Name: "panic-team", Status: models.TeamStatusStopped, Runtime: "docker"}
	db.Create(&team)

	schedule := models.Schedule{
		ID:             "sched-panic",
		Name:           "panic-sched",
		TeamID:         "team-panic",
		Prompt:         "test",
		CronExpression: "* * * * *",
		Timezone:       "UTC",
		Enabled:        true,
		Status:         models.ScheduleStatusIdle,
	}
	db.Create(&schedule)

	executeFn := func(ctx context.Context, sched models.Schedule) {
		panic("simulated panic in execution")
	}

	sched := New(db, executeFn, 100*time.Millisecond)
	sched.Start()
	time.Sleep(300 * time.Millisecond)
	sched.Stop()

	// Verify the schedule was reset to idle (not stuck in running).
	var updated models.Schedule
	db.First(&updated, "id = ?", "sched-panic")
	if updated.Status != models.ScheduleStatusIdle {
		t.Errorf("expected status 'idle' after panic recovery, got %q", updated.Status)
	}
}

func TestScheduler_AtomicClaim(t *testing.T) {
	db, err := models.InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	team := models.Team{ID: "team-atomic", Name: "atomic-team", Status: models.TeamStatusStopped, Runtime: "docker"}
	db.Create(&team)

	// Create a schedule that's in "error" status (not idle).
	schedule := models.Schedule{
		ID:             "sched-atomic",
		Name:           "atomic-sched",
		TeamID:         "team-atomic",
		Prompt:         "test",
		CronExpression: "* * * * *",
		Timezone:       "UTC",
		Enabled:        true,
		Status:         models.ScheduleStatusError,
	}
	db.Create(&schedule)

	var mu sync.Mutex
	var executed []string

	executeFn := func(ctx context.Context, sched models.Schedule) {
		mu.Lock()
		defer mu.Unlock()
		executed = append(executed, sched.ID)
	}

	sched := New(db, executeFn, 100*time.Millisecond)
	sched.Start()
	time.Sleep(250 * time.Millisecond)
	sched.Stop()

	mu.Lock()
	defer mu.Unlock()
	// The schedule has status "error" (not "idle"), so the atomic claim should skip it.
	if len(executed) != 0 {
		t.Errorf("expected no executions for non-idle schedule, got %d", len(executed))
	}
}

func TestScheduler_ConcurrencyLimit(t *testing.T) {
	db, err := models.InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	team := models.Team{ID: "team-conc", Name: "conc-team", Status: models.TeamStatusStopped, Runtime: "docker"}
	db.Create(&team)

	// Create 5 schedules.
	for i := 0; i < 5; i++ {
		id := "sched-conc-" + string(rune('a'+i))
		db.Create(&models.Schedule{
			ID:             id,
			Name:           id,
			TeamID:         "team-conc",
			Prompt:         "test",
			CronExpression: "* * * * *",
			Timezone:       "UTC",
			Enabled:        true,
			Status:         models.ScheduleStatusIdle,
		})
	}

	var mu sync.Mutex
	var maxConcurrent int
	var currentConcurrent int

	executeFn := func(ctx context.Context, sched models.Schedule) {
		mu.Lock()
		currentConcurrent++
		if currentConcurrent > maxConcurrent {
			maxConcurrent = currentConcurrent
		}
		mu.Unlock()

		time.Sleep(200 * time.Millisecond)

		mu.Lock()
		currentConcurrent--
		mu.Unlock()
	}

	// Create scheduler with a concurrency limit of 2 (override default).
	sched := New(db, executeFn, 100*time.Millisecond)
	sched.maxConcurrent = make(chan struct{}, 2)
	sched.Start()
	time.Sleep(500 * time.Millisecond)
	sched.Stop()

	mu.Lock()
	defer mu.Unlock()
	if maxConcurrent > 2 {
		t.Errorf("expected max 2 concurrent executions, got %d", maxConcurrent)
	}
}

func TestSanitizeError(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		envToken string
		want     string
	}{
		{
			name:     "no sensitive data",
			input:    "connection refused",
			envToken: "",
			want:     "connection refused",
		},
		{
			name:     "redacts NATS token",
			input:    "connecting to NATS: auth error with token secrettoken123",
			envToken: "secrettoken123",
			want:     "connecting to NATS: auth error with token [REDACTED]",
		},
		{
			name:     "redacts nats URL credentials",
			input:    "connecting to nats://mytoken@localhost:4222 failed",
			envToken: "",
			want:     "connecting to nats://[REDACTED]@localhost:4222 failed",
		},
		{
			name:     "redacts both token and URL",
			input:    "connecting to nats://secret@host:4222 with token secret",
			envToken: "secret",
			want:     "connecting to nats://[REDACTED]@host:4222 with token [REDACTED]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envToken != "" {
				t.Setenv("NATS_AUTH_TOKEN", tt.envToken)
			}
			got := sanitizeError(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeError() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExecutor_Execute_PromptTooLarge(t *testing.T) {
	db, err := models.InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	team := models.Team{ID: "team-big", Name: "big-prompt", Status: models.TeamStatusStopped, Runtime: "docker"}
	db.Create(&team)

	largePrompt := strings.Repeat("x", MaxPromptSize+1)
	schedule := models.Schedule{
		ID:             "sched-big",
		Name:           "big-prompt",
		TeamID:         "team-big",
		Prompt:         largePrompt,
		CronExpression: "* * * * *",
		Timezone:       "UTC",
		Enabled:        true,
		Status:         models.ScheduleStatusRunning,
	}
	db.Create(&schedule)

	executor := &Executor{
		DB:      db,
		Timeout: 10 * time.Second,
	}

	executor.Execute(context.Background(), schedule)

	// No ScheduleRun should be created — the executor rejects early.
	var runs []models.ScheduleRun
	db.Where("schedule_id = ?", "sched-big").Find(&runs)
	if len(runs) != 0 {
		t.Errorf("expected 0 runs for oversized prompt, got %d", len(runs))
	}

	// The schedule should be in error status.
	var updated models.Schedule
	db.First(&updated, "id = ?", "sched-big")
	if updated.Status != models.ScheduleStatusError {
		t.Errorf("expected schedule status 'error', got %q", updated.Status)
	}
}
