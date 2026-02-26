package models

import (
	"encoding/json"
	"testing"
	"time"
)

func TestInitDB_InMemory(t *testing.T) {
	db, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("getting sql.DB: %v", err)
	}
	defer sqlDB.Close()

	if err := sqlDB.Ping(); err != nil {
		t.Fatalf("database ping failed: %v", err)
	}
}

func TestTeam_CRUD(t *testing.T) {
	db, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	team := Team{
		ID:          "team-001",
		Name:        "test-team",
		Description: "A test team",
		Status:      TeamStatusStopped,
		Runtime:     "docker",
	}

	// Create.
	if err := db.Create(&team).Error; err != nil {
		t.Fatalf("creating team: %v", err)
	}

	// Read.
	var found Team
	if err := db.First(&found, "id = ?", "team-001").Error; err != nil {
		t.Fatalf("finding team: %v", err)
	}
	if found.Name != "test-team" {
		t.Errorf("expected name 'test-team', got %q", found.Name)
	}

	// Update.
	if err := db.Model(&found).Update("status", TeamStatusRunning).Error; err != nil {
		t.Fatalf("updating team: %v", err)
	}
	db.First(&found, "id = ?", "team-001")
	if found.Status != TeamStatusRunning {
		t.Errorf("expected status 'running', got %q", found.Status)
	}

	// Delete.
	if err := db.Delete(&found).Error; err != nil {
		t.Fatalf("deleting team: %v", err)
	}
	result := db.First(&Team{}, "id = ?", "team-001")
	if result.Error == nil {
		t.Error("expected team to be deleted")
	}
}

func TestAgent_CRUD(t *testing.T) {
	db, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	team := Team{ID: "team-002", Name: "agent-test-team", Status: TeamStatusStopped, Runtime: "docker"}
	db.Create(&team)

	skills, _ := json.Marshal([]string{"terraform", "kubectl"})
	agent := Agent{
		ID:       "agent-001",
		TeamID:   "team-002",
		Name:     "devops-agent",
		Role:     AgentRoleWorker,
		Skills:   JSON(skills),
	}

	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("creating agent: %v", err)
	}

	var found Agent
	if err := db.First(&found, "id = ?", "agent-001").Error; err != nil {
		t.Fatalf("finding agent: %v", err)
	}
	if found.Name != "devops-agent" {
		t.Errorf("expected name 'devops-agent', got %q", found.Name)
	}

	// Verify JSON skills round-trip.
	var parsedSkills []string
	if err := json.Unmarshal(found.Skills, &parsedSkills); err != nil {
		t.Fatalf("unmarshaling skills: %v", err)
	}
	if len(parsedSkills) != 2 || parsedSkills[0] != "terraform" {
		t.Errorf("unexpected skills: %v", parsedSkills)
	}
}

func TestTeam_CascadeDeleteAgents(t *testing.T) {
	db, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	team := Team{ID: "team-003", Name: "cascade-team", Status: TeamStatusStopped, Runtime: "docker"}
	db.Create(&team)

	agent := Agent{ID: "agent-002", TeamID: "team-003", Name: "worker", Role: AgentRoleWorker}
	db.Create(&agent)

	// Delete team should cascade to agents.
	db.Select("Agents").Delete(&team)

	var count int64
	db.Model(&Agent{}).Where("team_id = ?", "team-003").Count(&count)
	if count != 0 {
		t.Errorf("expected 0 agents after cascade delete, got %d", count)
	}
}

func TestTaskLog_Create(t *testing.T) {
	db, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	payload, _ := json.Marshal(map[string]string{"status": "completed", "result": "done"})
	log := TaskLog{
		ID:          "log-001",
		TeamID:      "team-001",
		MessageID:   "msg-001",
		FromAgent:   "leader",
		ToAgent:     "user",
		MessageType: "leader_response",
		Payload:     JSON(payload),
	}

	if err := db.Create(&log).Error; err != nil {
		t.Fatalf("creating task log: %v", err)
	}

	var found TaskLog
	db.First(&found, "id = ?", "log-001")
	if found.FromAgent != "leader" {
		t.Errorf("expected from_agent 'leader', got %q", found.FromAgent)
	}
}

func TestSettings_UniqueKey(t *testing.T) {
	db, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	s1 := Settings{Key: "api_port", Value: "8080"}
	if err := db.Create(&s1).Error; err != nil {
		t.Fatalf("creating setting: %v", err)
	}

	// Duplicate key should fail.
	s2 := Settings{Key: "api_port", Value: "9090"}
	if err := db.Create(&s2).Error; err == nil {
		t.Error("expected unique constraint error for duplicate key")
	}
}

func TestJSON_NilHandling(t *testing.T) {
	var j JSON

	// Scan nil.
	if err := j.Scan(nil); err != nil {
		t.Fatalf("Scan(nil): %v", err)
	}
	if string(j) != "null" {
		t.Errorf("expected 'null', got %q", string(j))
	}

	// MarshalJSON on empty.
	var empty JSON
	data, err := empty.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if string(data) != "null" {
		t.Errorf("expected 'null', got %q", string(data))
	}
}

func TestSchedule_CRUD(t *testing.T) {
	db, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	team := Team{ID: "team-sched-001", Name: "schedule-team", Status: TeamStatusStopped, Runtime: "docker"}
	db.Create(&team)

	schedule := Schedule{
		ID:             "sched-001",
		Name:           "daily-report",
		TeamID:         "team-sched-001",
		Prompt:         "Generate daily report",
		CronExpression: "0 9 * * *",
		Timezone:       "UTC",
		Enabled:        true,
		Status:         ScheduleStatusIdle,
	}

	// Create.
	if err := db.Create(&schedule).Error; err != nil {
		t.Fatalf("creating schedule: %v", err)
	}

	// Read.
	var found Schedule
	if err := db.First(&found, "id = ?", "sched-001").Error; err != nil {
		t.Fatalf("finding schedule: %v", err)
	}
	if found.Name != "daily-report" {
		t.Errorf("expected name 'daily-report', got %q", found.Name)
	}
	if found.TeamID != "team-sched-001" {
		t.Errorf("expected team_id 'team-sched-001', got %q", found.TeamID)
	}
	if found.CronExpression != "0 9 * * *" {
		t.Errorf("expected cron '0 9 * * *', got %q", found.CronExpression)
	}
	if !found.Enabled {
		t.Error("expected enabled to be true")
	}
	if found.Status != ScheduleStatusIdle {
		t.Errorf("expected status 'idle', got %q", found.Status)
	}

	// Update.
	if err := db.Model(&found).Update("enabled", false).Error; err != nil {
		t.Fatalf("updating schedule: %v", err)
	}
	db.First(&found, "id = ?", "sched-001")
	if found.Enabled {
		t.Error("expected enabled to be false after update")
	}

	// Delete.
	if err := db.Delete(&found).Error; err != nil {
		t.Fatalf("deleting schedule: %v", err)
	}
	result := db.First(&Schedule{}, "id = ?", "sched-001")
	if result.Error == nil {
		t.Error("expected schedule to be deleted")
	}
}

func TestScheduleRun_CRUD(t *testing.T) {
	db, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	team := Team{ID: "team-run-001", Name: "run-team", Status: TeamStatusStopped, Runtime: "docker"}
	db.Create(&team)

	schedule := Schedule{
		ID:             "sched-run-001",
		Name:           "test-schedule",
		TeamID:         "team-run-001",
		Prompt:         "Run tests",
		CronExpression: "*/5 * * * *",
		Timezone:       "UTC",
		Status:         ScheduleStatusIdle,
	}
	db.Create(&schedule)

	now := time.Now()
	run := ScheduleRun{
		ID:               "run-001",
		ScheduleID:       "sched-run-001",
		TeamDeploymentID: "deploy-001",
		StartedAt:        now,
		Status:           ScheduleRunStatusRunning,
	}

	// Create.
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("creating schedule run: %v", err)
	}

	// Read.
	var found ScheduleRun
	if err := db.First(&found, "id = ?", "run-001").Error; err != nil {
		t.Fatalf("finding schedule run: %v", err)
	}
	if found.ScheduleID != "sched-run-001" {
		t.Errorf("expected schedule_id 'sched-run-001', got %q", found.ScheduleID)
	}
	if found.Status != ScheduleRunStatusRunning {
		t.Errorf("expected status 'running', got %q", found.Status)
	}

	// Update — mark as success.
	finished := time.Now()
	if err := db.Model(&found).Updates(map[string]interface{}{
		"status":      ScheduleRunStatusSuccess,
		"finished_at": finished,
	}).Error; err != nil {
		t.Fatalf("updating schedule run: %v", err)
	}
	db.First(&found, "id = ?", "run-001")
	if found.Status != ScheduleRunStatusSuccess {
		t.Errorf("expected status 'success', got %q", found.Status)
	}
	if found.FinishedAt == nil {
		t.Error("expected finished_at to be set")
	}
}

func TestSchedule_TeamForeignKey(t *testing.T) {
	db, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	team := Team{ID: "team-fk-001", Name: "fk-team", Status: TeamStatusStopped, Runtime: "docker"}
	db.Create(&team)

	schedule := Schedule{
		ID:             "sched-fk-001",
		Name:           "fk-schedule",
		TeamID:         "team-fk-001",
		Prompt:         "test",
		CronExpression: "0 * * * *",
		Timezone:       "UTC",
		Status:         ScheduleStatusIdle,
	}
	db.Create(&schedule)

	// Load schedule with team preloaded.
	var found Schedule
	if err := db.Preload("Team").First(&found, "id = ?", "sched-fk-001").Error; err != nil {
		t.Fatalf("finding schedule with team: %v", err)
	}
	if found.Team.Name != "fk-team" {
		t.Errorf("expected preloaded team name 'fk-team', got %q", found.Team.Name)
	}
}

func TestScheduleRun_ScheduleForeignKey(t *testing.T) {
	db, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	team := Team{ID: "team-rfk-001", Name: "rfk-team", Status: TeamStatusStopped, Runtime: "docker"}
	db.Create(&team)

	schedule := Schedule{
		ID:             "sched-rfk-001",
		Name:           "rfk-schedule",
		TeamID:         "team-rfk-001",
		Prompt:         "test",
		CronExpression: "0 * * * *",
		Timezone:       "UTC",
		Status:         ScheduleStatusIdle,
	}
	db.Create(&schedule)

	run := ScheduleRun{
		ID:         "run-rfk-001",
		ScheduleID: "sched-rfk-001",
		StartedAt:  time.Now(),
		Status:     ScheduleRunStatusRunning,
	}
	db.Create(&run)

	// Load runs via schedule preload.
	var found Schedule
	if err := db.Preload("Runs").First(&found, "id = ?", "sched-rfk-001").Error; err != nil {
		t.Fatalf("finding schedule with runs: %v", err)
	}
	if len(found.Runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(found.Runs))
	}
	if found.Runs[0].ID != "run-rfk-001" {
		t.Errorf("expected run ID 'run-rfk-001', got %q", found.Runs[0].ID)
	}
}

func TestSchedule_CascadeDeleteRuns(t *testing.T) {
	db, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	team := Team{ID: "team-cd-001", Name: "cascade-del-team", Status: TeamStatusStopped, Runtime: "docker"}
	db.Create(&team)

	schedule := Schedule{
		ID:             "sched-cd-001",
		Name:           "cascade-schedule",
		TeamID:         "team-cd-001",
		Prompt:         "test",
		CronExpression: "0 * * * *",
		Timezone:       "UTC",
		Status:         ScheduleStatusIdle,
	}
	db.Create(&schedule)

	run1 := ScheduleRun{ID: "run-cd-001", ScheduleID: "sched-cd-001", StartedAt: time.Now(), Status: ScheduleRunStatusSuccess}
	run2 := ScheduleRun{ID: "run-cd-002", ScheduleID: "sched-cd-001", StartedAt: time.Now(), Status: ScheduleRunStatusFailed}
	db.Create(&run1)
	db.Create(&run2)

	// Delete schedule should cascade to runs.
	db.Select("Runs").Delete(&schedule)

	var count int64
	db.Model(&ScheduleRun{}).Where("schedule_id = ?", "sched-cd-001").Count(&count)
	if count != 0 {
		t.Errorf("expected 0 runs after cascade delete, got %d", count)
	}
}

func TestSchedule_StatusConstants(t *testing.T) {
	if ScheduleStatusIdle != "idle" {
		t.Errorf("expected ScheduleStatusIdle to be 'idle', got %q", ScheduleStatusIdle)
	}
	if ScheduleStatusRunning != "running" {
		t.Errorf("expected ScheduleStatusRunning to be 'running', got %q", ScheduleStatusRunning)
	}
	if ScheduleStatusError != "error" {
		t.Errorf("expected ScheduleStatusError to be 'error', got %q", ScheduleStatusError)
	}
}

func TestScheduleRun_StatusConstants(t *testing.T) {
	if ScheduleRunStatusRunning != "running" {
		t.Errorf("expected ScheduleRunStatusRunning to be 'running', got %q", ScheduleRunStatusRunning)
	}
	if ScheduleRunStatusSuccess != "success" {
		t.Errorf("expected ScheduleRunStatusSuccess to be 'success', got %q", ScheduleRunStatusSuccess)
	}
	if ScheduleRunStatusFailed != "failed" {
		t.Errorf("expected ScheduleRunStatusFailed to be 'failed', got %q", ScheduleRunStatusFailed)
	}
	if ScheduleRunStatusTimeout != "timeout" {
		t.Errorf("expected ScheduleRunStatusTimeout to be 'timeout', got %q", ScheduleRunStatusTimeout)
	}
}

func TestJSON_RoundTrip(t *testing.T) {
	input := `{"key":"value","nested":{"n":42}}`
	var j JSON
	if err := j.UnmarshalJSON([]byte(input)); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}

	val, err := j.Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	if val != input {
		t.Errorf("expected %q, got %q", input, val)
	}
}
