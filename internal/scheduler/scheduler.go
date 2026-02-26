package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/helmcode/agent-crew/internal/models"
)

// DefaultMaxConcurrent is the default maximum number of concurrent schedule executions.
const DefaultMaxConcurrent = 10

// ExecuteFunc is the callback invoked when a schedule is due.
// It receives the schedule that should be executed.
type ExecuteFunc func(ctx context.Context, schedule models.Schedule)

// Scheduler checks for due schedules every tick interval and triggers execution.
type Scheduler struct {
	db             *gorm.DB
	execute        ExecuteFunc
	interval       time.Duration
	maxConcurrent  chan struct{} // Semaphore to limit concurrent executions.

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates a new Scheduler. The execute function is called for each schedule
// that is due. The scheduler ticks every interval (default 60s if zero).
// The maximum number of concurrent executions can be set via the
// SCHEDULER_MAX_CONCURRENT environment variable (default 10).
func New(db *gorm.DB, execute ExecuteFunc, interval time.Duration) *Scheduler {
	if interval <= 0 {
		interval = 60 * time.Second
	}

	maxConcurrent := DefaultMaxConcurrent
	if envMax := os.Getenv("SCHEDULER_MAX_CONCURRENT"); envMax != "" {
		if n, err := strconv.Atoi(envMax); err == nil && n > 0 {
			maxConcurrent = n
		}
	}

	return &Scheduler{
		db:            db,
		execute:       execute,
		interval:      interval,
		maxConcurrent: make(chan struct{}, maxConcurrent),
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

		// C2 FIX: Atomic claim — only update if status is still idle.
		// This prevents double-fire when two ticks overlap.
		result := s.db.Model(&models.Schedule{}).
			Where("id = ? AND status = ?", sched.ID, models.ScheduleStatusIdle).
			Updates(map[string]interface{}{
				"status":      models.ScheduleStatusRunning,
				"last_run_at": now,
			})
		if result.Error != nil {
			slog.Error("scheduler: failed to claim schedule", "id", sched.ID, "error", result.Error)
			continue
		}
		if result.RowsAffected == 0 {
			// Another tick already claimed this schedule.
			slog.Info("scheduler: schedule already claimed by another tick", "id", sched.ID)
			continue
		}

		// C3 FIX: Concurrency limiter — try to acquire a slot.
		select {
		case s.maxConcurrent <- struct{}{}:
			// Slot acquired.
		default:
			// All slots busy — revert schedule to idle so it can be picked up next tick.
			slog.Warn("scheduler: max concurrent executions reached, skipping",
				"id", sched.ID, "name", sched.Name)
			s.db.Model(&models.Schedule{}).Where("id = ?", sched.ID).
				Update("status", models.ScheduleStatusIdle)
			continue
		}

		// Execute in a separate goroutine for non-blocking parallel execution.
		schedCopy := sched
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() { <-s.maxConcurrent }() // Release semaphore slot.

			// C1 FIX: Panic recovery — reset schedule status on panic.
			defer func() {
				if r := recover(); r != nil {
					slog.Error("scheduler: panic during execution",
						"id", schedCopy.ID, "name", schedCopy.Name,
						"panic", fmt.Sprintf("%v", r))
					s.db.Model(&models.Schedule{}).Where("id = ?", schedCopy.ID).
						Update("status", models.ScheduleStatusIdle)
				}
			}()

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
