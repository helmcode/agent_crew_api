package api

import (
	"os"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/helmcode/agent-crew/internal/models"
)

// GetScheduleConfig returns the schedule configuration visible to the frontend.
func (s *Server) GetScheduleConfig(c *fiber.Ctx) error {
	timeout := "1h"
	if v := os.Getenv("SCHEDULE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			timeout = d.String()
		}
	}
	return c.JSON(fiber.Map{"timeout": timeout})
}

// ListSchedules returns all schedules with their associated team name.
func (s *Server) ListSchedules(c *fiber.Ctx) error {
	var schedules []models.Schedule
	if err := s.db.Scopes(OrgScope(c)).Preload("Team").Find(&schedules).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to list schedules")
	}
	return c.JSON(schedules)
}

// GetSchedule returns a single schedule by ID.
func (s *Server) GetSchedule(c *fiber.Ctx) error {
	id := c.Params("id")
	var schedule models.Schedule
	if err := s.db.Scopes(OrgScope(c)).Preload("Team").First(&schedule, "id = ?", id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "schedule not found")
	}
	return c.JSON(schedule)
}

// CreateSchedule creates a new schedule.
func (s *Server) CreateSchedule(c *fiber.Ctx) error {
	var req CreateScheduleRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	if req.Name == "" {
		return fiber.NewError(fiber.StatusBadRequest, "name is required")
	}
	if req.TeamID == "" {
		return fiber.NewError(fiber.StatusBadRequest, "team_id is required")
	}
	if req.Prompt == "" {
		return fiber.NewError(fiber.StatusBadRequest, "prompt is required")
	}
	if len(req.Prompt) > 50000 {
		return fiber.NewError(fiber.StatusBadRequest, "prompt exceeds maximum length of 50000 characters")
	}
	if req.CronExpression == "" {
		return fiber.NewError(fiber.StatusBadRequest, "cron_expression is required")
	}

	// Validate team exists and belongs to org.
	var team models.Team
	if err := s.db.Scopes(OrgScope(c)).First(&team, "id = ?", req.TeamID).Error; err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "team_id references a non-existent team")
	}

	// Validate cron expression.
	if err := validateCronExpression(req.CronExpression); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid cron_expression: "+err.Error())
	}

	// Validate and default timezone.
	tz := req.Timezone
	if tz == "" {
		tz = "UTC"
	}
	if err := validateTimezone(tz); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid timezone: "+err.Error())
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	// Calculate next run time.
	nextRun := calculateNextRun(req.CronExpression, tz)

	schedule := models.Schedule{
		ID:             uuid.New().String(),
		OrgID:          GetOrgID(c),
		Name:           req.Name,
		TeamID:         req.TeamID,
		Prompt:         req.Prompt,
		CronExpression: req.CronExpression,
		Timezone:       tz,
		Enabled:        enabled,
		NextRunAt:      nextRun,
		Status:         models.ScheduleStatusIdle,
	}

	if err := s.db.Create(&schedule).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to create schedule")
	}

	// Reload with team preloaded.
	s.db.Preload("Team").First(&schedule, "id = ?", schedule.ID)
	return c.Status(fiber.StatusCreated).JSON(schedule)
}

// UpdateSchedule updates a schedule's fields.
func (s *Server) UpdateSchedule(c *fiber.Ctx) error {
	id := c.Params("id")
	var schedule models.Schedule
	if err := s.db.Scopes(OrgScope(c)).First(&schedule, "id = ?", id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "schedule not found")
	}

	var req UpdateScheduleRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	updates := map[string]interface{}{}

	if req.Name != nil {
		if *req.Name == "" {
			return fiber.NewError(fiber.StatusBadRequest, "name cannot be empty")
		}
		updates["name"] = *req.Name
	}
	if req.TeamID != nil {
		var team models.Team
		if err := s.db.Scopes(OrgScope(c)).First(&team, "id = ?", *req.TeamID).Error; err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "team_id references a non-existent team")
		}
		updates["team_id"] = *req.TeamID
	}
	if req.Prompt != nil {
		if *req.Prompt == "" {
			return fiber.NewError(fiber.StatusBadRequest, "prompt cannot be empty")
		}
		if len(*req.Prompt) > 50000 {
			return fiber.NewError(fiber.StatusBadRequest, "prompt exceeds maximum length of 50000 characters")
		}
		updates["prompt"] = *req.Prompt
	}

	// Track whether cron or timezone changed so we recalculate next_run_at.
	cronChanged := false
	newCron := schedule.CronExpression
	newTZ := schedule.Timezone

	if req.CronExpression != nil {
		if err := validateCronExpression(*req.CronExpression); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid cron_expression: "+err.Error())
		}
		updates["cron_expression"] = *req.CronExpression
		newCron = *req.CronExpression
		cronChanged = true
	}
	if req.Timezone != nil {
		if err := validateTimezone(*req.Timezone); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid timezone: "+err.Error())
		}
		updates["timezone"] = *req.Timezone
		newTZ = *req.Timezone
		cronChanged = true
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}

	if cronChanged {
		updates["next_run_at"] = calculateNextRun(newCron, newTZ)
	}

	if len(updates) > 0 {
		if err := s.db.Model(&schedule).Updates(updates).Error; err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "failed to update schedule")
		}
	}

	s.db.Preload("Team").First(&schedule, "id = ?", id)
	return c.JSON(schedule)
}

// DeleteSchedule removes a schedule and cascades to its runs.
func (s *Server) DeleteSchedule(c *fiber.Ctx) error {
	id := c.Params("id")
	var schedule models.Schedule
	if err := s.db.Scopes(OrgScope(c)).First(&schedule, "id = ?", id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "schedule not found")
	}

	if err := s.db.Select("Runs").Delete(&schedule).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to delete schedule")
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// ToggleSchedule toggles a schedule's enabled state.
func (s *Server) ToggleSchedule(c *fiber.Ctx) error {
	id := c.Params("id")
	var schedule models.Schedule
	if err := s.db.Scopes(OrgScope(c)).First(&schedule, "id = ?", id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "schedule not found")
	}

	newEnabled := !schedule.Enabled
	updates := map[string]interface{}{
		"enabled": newEnabled,
	}

	// Recalculate next_run_at when enabling.
	if newEnabled {
		updates["next_run_at"] = calculateNextRun(schedule.CronExpression, schedule.Timezone)
	}

	if err := s.db.Model(&schedule).Updates(updates).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to toggle schedule")
	}

	s.db.Preload("Team").First(&schedule, "id = ?", id)
	return c.JSON(schedule)
}

// ListScheduleRuns returns paginated runs for a schedule, newest first.
func (s *Server) ListScheduleRuns(c *fiber.Ctx) error {
	id := c.Params("id")

	// Verify schedule exists.
	var schedule models.Schedule
	if err := s.db.Scopes(OrgScope(c)).First(&schedule, "id = ?", id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "schedule not found")
	}

	page, _ := strconv.Atoi(c.Query("page", "1"))
	perPage, _ := strconv.Atoi(c.Query("per_page", "20"))
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}

	var total int64
	s.db.Model(&models.ScheduleRun{}).Where("schedule_id = ?", id).Count(&total)

	var runs []models.ScheduleRun
	offset := (page - 1) * perPage
	if err := s.db.Where("schedule_id = ?", id).
		Order("started_at DESC").
		Limit(perPage).
		Offset(offset).
		Find(&runs).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to list schedule runs")
	}

	return c.JSON(fiber.Map{
		"data":     runs,
		"total":    total,
		"page":     page,
		"per_page": perPage,
	})
}

// GetScheduleRun returns a single run by schedule and run ID.
func (s *Server) GetScheduleRun(c *fiber.Ctx) error {
	scheduleID := c.Params("id")
	runID := c.Params("runId")

	// Verify schedule exists.
	var schedule models.Schedule
	if err := s.db.Scopes(OrgScope(c)).First(&schedule, "id = ?", scheduleID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "schedule not found")
	}

	var run models.ScheduleRun
	if err := s.db.First(&run, "id = ? AND schedule_id = ?", runID, scheduleID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "schedule run not found")
	}

	return c.JSON(run)
}

// validateCronExpression checks that a cron expression has 5 fields and each field is non-empty.
func validateCronExpression(expr string) error {
	fields := splitCronFields(expr)
	if len(fields) != 5 {
		return fiber.NewError(fiber.StatusBadRequest, "cron expression must have exactly 5 fields (minute hour day month weekday)")
	}
	return nil
}

// splitCronFields splits a cron expression into fields by whitespace.
func splitCronFields(expr string) []string {
	var fields []string
	field := ""
	for _, c := range expr {
		if c == ' ' || c == '\t' {
			if field != "" {
				fields = append(fields, field)
				field = ""
			}
		} else {
			field += string(c)
		}
	}
	if field != "" {
		fields = append(fields, field)
	}
	return fields
}

// validateTimezone checks that a timezone string is a valid IANA timezone.
func validateTimezone(tz string) error {
	_, err := time.LoadLocation(tz)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "unknown IANA timezone: "+tz)
	}
	return nil
}

// calculateNextRun computes the next run time for a cron expression in the given timezone.
// Returns nil if the cron expression or timezone is invalid.
func calculateNextRun(cronExpr, tz string) *time.Time {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil
	}

	now := time.Now().In(loc)
	fields := splitCronFields(cronExpr)
	if len(fields) != 5 {
		return nil
	}

	// Simple next-run calculation: try each minute for the next 48 hours.
	candidate := now.Truncate(time.Minute).Add(time.Minute)
	limit := candidate.Add(48 * time.Hour)
	for candidate.Before(limit) {
		if cronMatchesTime(fields, candidate) {
			utc := candidate.UTC()
			return &utc
		}
		candidate = candidate.Add(time.Minute)
	}

	return nil
}

// cronMatchesTime checks if a time matches a 5-field cron expression.
func cronMatchesTime(fields []string, t time.Time) bool {
	return cronFieldMatches(fields[0], t.Minute(), 0, 59) &&
		cronFieldMatches(fields[1], t.Hour(), 0, 23) &&
		cronFieldMatches(fields[2], t.Day(), 1, 31) &&
		cronFieldMatches(fields[3], int(t.Month()), 1, 12) &&
		cronFieldMatches(fields[4], int(t.Weekday()), 0, 6)
}

// cronFieldMatches checks if a value matches a single cron field.
// Supports: * (any), exact number, comma-separated values, ranges (e.g. 1-5), and steps (e.g. */5).
func cronFieldMatches(field string, value, min, max int) bool {
	if field == "*" {
		return true
	}

	// Handle comma-separated values.
	for _, part := range splitByComma(field) {
		// Handle step: */N or range/N.
		if idx := indexByte(part, '/'); idx >= 0 {
			base := part[:idx]
			stepStr := part[idx+1:]
			step, err := strconv.Atoi(stepStr)
			if err != nil || step <= 0 {
				continue
			}
			if base == "*" {
				if (value-min)%step == 0 {
					return true
				}
			} else if rangeIdx := indexByte(base, '-'); rangeIdx >= 0 {
				lo, err1 := strconv.Atoi(base[:rangeIdx])
				hi, err2 := strconv.Atoi(base[rangeIdx+1:])
				if err1 != nil || err2 != nil {
					continue
				}
				if value >= lo && value <= hi && (value-lo)%step == 0 {
					return true
				}
			}
			continue
		}

		// Handle range: N-M.
		if rangeIdx := indexByte(part, '-'); rangeIdx >= 0 {
			lo, err1 := strconv.Atoi(part[:rangeIdx])
			hi, err2 := strconv.Atoi(part[rangeIdx+1:])
			if err1 != nil || err2 != nil {
				continue
			}
			if value >= lo && value <= hi {
				return true
			}
			continue
		}

		// Exact match.
		n, err := strconv.Atoi(part)
		if err != nil {
			continue
		}
		if n == value {
			return true
		}
	}

	return false
}

// splitByComma splits a string by commas.
func splitByComma(s string) []string {
	var parts []string
	part := ""
	for _, c := range s {
		if c == ',' {
			if part != "" {
				parts = append(parts, part)
			}
			part = ""
		} else {
			part += string(c)
		}
	}
	if part != "" {
		parts = append(parts, part)
	}
	return parts
}

// indexByte returns the index of the first occurrence of b in s, or -1.
func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
