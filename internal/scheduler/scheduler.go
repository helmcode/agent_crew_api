package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/helmcode/agent-crew/internal/models"
)

// ExecuteFunc is the callback invoked when a schedule is due.
// It receives the schedule that should be executed.
type ExecuteFunc func(ctx context.Context, schedule models.Schedule)

// Scheduler checks for due schedules every tick interval and triggers execution.
type Scheduler struct {
	db       *gorm.DB
	execute  ExecuteFunc
	interval time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates a new Scheduler. The execute function is called for each schedule
// that is due. The scheduler ticks every interval (default 60s if zero).
func New(db *gorm.DB, execute ExecuteFunc, interval time.Duration) *Scheduler {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	return &Scheduler{
		db:       db,
		execute:  execute,
		interval: interval,
	}
}

// Start begins the scheduler loop in a background goroutine.
// It is safe to call Start only once. Call Stop to shut down.
func (s *Scheduler) Start() {
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.wg.Add(1)
	go s.loop()
	slog.Info("scheduler started", "interval", s.interval.String())
}

// Stop gracefully shuts down the scheduler and waits for the loop to exit.
func (s *Scheduler) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	slog.Info("scheduler stopped")
}

// loop is the main scheduler loop that ticks every interval.
func (s *Scheduler) loop() {
	defer s.wg.Done()

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// Run an immediate tick on startup.
	s.tick()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.tick()
		}
	}
}

// tick queries for due schedules and triggers execution for each.
func (s *Scheduler) tick() {
	now := time.Now()

	var schedules []models.Schedule
	if err := s.db.Where("enabled = ? AND status != ?", true, models.ScheduleStatusRunning).
		Find(&schedules).Error; err != nil {
		slog.Error("scheduler: failed to query schedules", "error", err)
		return
	}

	for _, sched := range schedules {
		if !IsDue(sched.CronExpression, sched.Timezone, now) {
			continue
		}

		slog.Info("scheduler: schedule is due", "id", sched.ID, "name", sched.Name)

		// Update status to running and set last_run_at.
		if err := s.db.Model(&sched).Updates(map[string]interface{}{
			"status":      models.ScheduleStatusRunning,
			"last_run_at": now,
		}).Error; err != nil {
			slog.Error("scheduler: failed to update schedule status", "id", sched.ID, "error", err)
			continue
		}

		// Execute in a separate goroutine for non-blocking parallel execution.
		schedCopy := sched
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.execute(s.ctx, schedCopy)

			// After execution, update next_run_at.
			nextRun := NextRun(schedCopy.CronExpression, schedCopy.Timezone)
			updates := map[string]interface{}{
				"status": models.ScheduleStatusIdle,
			}
			if !nextRun.IsZero() {
				utc := nextRun.UTC()
				updates["next_run_at"] = utc
			}
			if err := s.db.Model(&models.Schedule{}).Where("id = ?", schedCopy.ID).Updates(updates).Error; err != nil {
				slog.Error("scheduler: failed to update next_run_at", "id", schedCopy.ID, "error", err)
			}
		}()
	}
}
