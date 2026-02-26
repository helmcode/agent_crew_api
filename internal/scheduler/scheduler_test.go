package scheduler

import (
	"context"
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
