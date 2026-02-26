package api

import (
	"testing"

	"github.com/helmcode/agent-crew/internal/models"
)

// --- Schedule CRUD ---

func TestCreateSchedule(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Create a team first.
	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "sched-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	enabled := true
	body := CreateScheduleRequest{
		Name:           "daily-report",
		TeamID:         team.ID,
		Prompt:         "Generate daily report",
		CronExpression: "0 9 * * *",
		Timezone:       "UTC",
		Enabled:        &enabled,
	}
	rec := doRequest(srv, "POST", "/api/schedules", body)

	if rec.Code != 201 {
		t.Fatalf("status: got %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}

	var schedule models.Schedule
	parseJSON(t, rec, &schedule)

	if schedule.Name != "daily-report" {
		t.Errorf("name: got %q, want 'daily-report'", schedule.Name)
	}
	if schedule.TeamID != team.ID {
		t.Errorf("team_id: got %q, want %q", schedule.TeamID, team.ID)
	}
	if schedule.CronExpression != "0 9 * * *" {
		t.Errorf("cron_expression: got %q, want '0 9 * * *'", schedule.CronExpression)
	}
	if schedule.Timezone != "UTC" {
		t.Errorf("timezone: got %q, want 'UTC'", schedule.Timezone)
	}
	if !schedule.Enabled {
		t.Error("expected enabled to be true")
	}
	if schedule.Status != "idle" {
		t.Errorf("status: got %q, want 'idle'", schedule.Status)
	}
	if schedule.ID == "" {
		t.Error("expected non-empty ID")
	}
	if schedule.NextRunAt == nil {
		t.Error("expected next_run_at to be set")
	}
}

func TestCreateSchedule_DefaultTimezone(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "sched-team-tz"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	body := CreateScheduleRequest{
		Name:           "no-tz",
		TeamID:         team.ID,
		Prompt:         "test",
		CronExpression: "0 * * * *",
	}
	rec := doRequest(srv, "POST", "/api/schedules", body)

	if rec.Code != 201 {
		t.Fatalf("status: got %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}

	var schedule models.Schedule
	parseJSON(t, rec, &schedule)
	if schedule.Timezone != "UTC" {
		t.Errorf("timezone: got %q, want 'UTC' (default)", schedule.Timezone)
	}
}

func TestCreateSchedule_DefaultEnabled(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "sched-team-en"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	body := CreateScheduleRequest{
		Name:           "default-enabled",
		TeamID:         team.ID,
		Prompt:         "test",
		CronExpression: "0 * * * *",
	}
	rec := doRequest(srv, "POST", "/api/schedules", body)

	if rec.Code != 201 {
		t.Fatalf("status: got %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}

	var schedule models.Schedule
	parseJSON(t, rec, &schedule)
	if !schedule.Enabled {
		t.Error("expected enabled to default to true")
	}
}

func TestCreateSchedule_MissingName(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "sched-team-mn"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	body := CreateScheduleRequest{
		TeamID:         team.ID,
		Prompt:         "test",
		CronExpression: "0 * * * *",
	}
	rec := doRequest(srv, "POST", "/api/schedules", body)
	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400", rec.Code)
	}
}

func TestCreateSchedule_MissingTeamID(t *testing.T) {
	srv, _ := setupTestServer(t)

	body := CreateScheduleRequest{
		Name:           "no-team",
		Prompt:         "test",
		CronExpression: "0 * * * *",
	}
	rec := doRequest(srv, "POST", "/api/schedules", body)
	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400", rec.Code)
	}
}

func TestCreateSchedule_MissingPrompt(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "sched-team-mp"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	body := CreateScheduleRequest{
		Name:           "no-prompt",
		TeamID:         team.ID,
		CronExpression: "0 * * * *",
	}
	rec := doRequest(srv, "POST", "/api/schedules", body)
	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400", rec.Code)
	}
}

func TestCreateSchedule_MissingCron(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "sched-team-mc"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	body := CreateScheduleRequest{
		Name:   "no-cron",
		TeamID: team.ID,
		Prompt: "test",
	}
	rec := doRequest(srv, "POST", "/api/schedules", body)
	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400", rec.Code)
	}
}

func TestCreateSchedule_InvalidCron(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "sched-team-ic"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	body := CreateScheduleRequest{
		Name:           "bad-cron",
		TeamID:         team.ID,
		Prompt:         "test",
		CronExpression: "not a cron",
	}
	rec := doRequest(srv, "POST", "/api/schedules", body)
	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400 for invalid cron", rec.Code)
	}
}

func TestCreateSchedule_InvalidTimezone(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "sched-team-itz"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	body := CreateScheduleRequest{
		Name:           "bad-tz",
		TeamID:         team.ID,
		Prompt:         "test",
		CronExpression: "0 * * * *",
		Timezone:       "Not/A/Timezone",
	}
	rec := doRequest(srv, "POST", "/api/schedules", body)
	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400 for invalid timezone", rec.Code)
	}
}

func TestCreateSchedule_NonExistentTeam(t *testing.T) {
	srv, _ := setupTestServer(t)

	body := CreateScheduleRequest{
		Name:           "orphan-sched",
		TeamID:         "nonexistent-team-id",
		Prompt:         "test",
		CronExpression: "0 * * * *",
	}
	rec := doRequest(srv, "POST", "/api/schedules", body)
	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400 for non-existent team", rec.Code)
	}
}

func TestCreateSchedule_TeamPreloaded(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "preload-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	body := CreateScheduleRequest{
		Name:           "preload-test",
		TeamID:         team.ID,
		Prompt:         "test",
		CronExpression: "0 * * * *",
	}
	rec := doRequest(srv, "POST", "/api/schedules", body)
	var schedule models.Schedule
	parseJSON(t, rec, &schedule)

	if schedule.Team.Name != "preload-team" {
		t.Errorf("expected preloaded team name 'preload-team', got %q", schedule.Team.Name)
	}
}

func TestListSchedules(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "list-sched-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	doRequest(srv, "POST", "/api/schedules", CreateScheduleRequest{
		Name: "s1", TeamID: team.ID, Prompt: "p1", CronExpression: "0 * * * *",
	})
	doRequest(srv, "POST", "/api/schedules", CreateScheduleRequest{
		Name: "s2", TeamID: team.ID, Prompt: "p2", CronExpression: "0 * * * *",
	})

	rec := doRequest(srv, "GET", "/api/schedules", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var schedules []models.Schedule
	parseJSON(t, rec, &schedules)
	if len(schedules) != 2 {
		t.Fatalf("schedules: got %d, want 2", len(schedules))
	}
}

func TestListSchedules_Empty(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "GET", "/api/schedules", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var schedules []models.Schedule
	parseJSON(t, rec, &schedules)
	if len(schedules) != 0 {
		t.Fatalf("schedules: got %d, want 0", len(schedules))
	}
}

func TestGetSchedule(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "get-sched-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	createRec := doRequest(srv, "POST", "/api/schedules", CreateScheduleRequest{
		Name: "get-me", TeamID: team.ID, Prompt: "test", CronExpression: "0 * * * *",
	})
	var created models.Schedule
	parseJSON(t, createRec, &created)

	rec := doRequest(srv, "GET", "/api/schedules/"+created.ID, nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var schedule models.Schedule
	parseJSON(t, rec, &schedule)
	if schedule.Name != "get-me" {
		t.Errorf("name: got %q, want 'get-me'", schedule.Name)
	}
}

func TestGetSchedule_NotFound(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "GET", "/api/schedules/nonexistent-id", nil)
	if rec.Code != 404 {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
}

func TestUpdateSchedule(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "upd-sched-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	createRec := doRequest(srv, "POST", "/api/schedules", CreateScheduleRequest{
		Name: "update-me", TeamID: team.ID, Prompt: "old prompt", CronExpression: "0 * * * *",
	})
	var created models.Schedule
	parseJSON(t, createRec, &created)

	newName := "updated-schedule"
	newPrompt := "new prompt"
	newCron := "30 8 * * 1-5"
	rec := doRequest(srv, "PUT", "/api/schedules/"+created.ID, UpdateScheduleRequest{
		Name:           &newName,
		Prompt:         &newPrompt,
		CronExpression: &newCron,
	})

	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	var updated models.Schedule
	parseJSON(t, rec, &updated)
	if updated.Name != "updated-schedule" {
		t.Errorf("name: got %q, want 'updated-schedule'", updated.Name)
	}
	if updated.Prompt != "new prompt" {
		t.Errorf("prompt: got %q, want 'new prompt'", updated.Prompt)
	}
	if updated.CronExpression != "30 8 * * 1-5" {
		t.Errorf("cron_expression: got %q, want '30 8 * * 1-5'", updated.CronExpression)
	}
}

func TestUpdateSchedule_NotFound(t *testing.T) {
	srv, _ := setupTestServer(t)

	name := "test"
	rec := doRequest(srv, "PUT", "/api/schedules/nonexistent", UpdateScheduleRequest{Name: &name})
	if rec.Code != 404 {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
}

func TestUpdateSchedule_InvalidCron(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "upd-sched-ic"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	createRec := doRequest(srv, "POST", "/api/schedules", CreateScheduleRequest{
		Name: "ic-sched", TeamID: team.ID, Prompt: "test", CronExpression: "0 * * * *",
	})
	var created models.Schedule
	parseJSON(t, createRec, &created)

	badCron := "invalid"
	rec := doRequest(srv, "PUT", "/api/schedules/"+created.ID, UpdateScheduleRequest{
		CronExpression: &badCron,
	})
	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400 for invalid cron", rec.Code)
	}
}

func TestUpdateSchedule_InvalidTimezone(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "upd-sched-itz"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	createRec := doRequest(srv, "POST", "/api/schedules", CreateScheduleRequest{
		Name: "itz-sched", TeamID: team.ID, Prompt: "test", CronExpression: "0 * * * *",
	})
	var created models.Schedule
	parseJSON(t, createRec, &created)

	badTZ := "Invalid/Zone"
	rec := doRequest(srv, "PUT", "/api/schedules/"+created.ID, UpdateScheduleRequest{
		Timezone: &badTZ,
	})
	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400 for invalid timezone", rec.Code)
	}
}

func TestUpdateSchedule_NonExistentTeam(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "upd-sched-net"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	createRec := doRequest(srv, "POST", "/api/schedules", CreateScheduleRequest{
		Name: "net-sched", TeamID: team.ID, Prompt: "test", CronExpression: "0 * * * *",
	})
	var created models.Schedule
	parseJSON(t, createRec, &created)

	badTeam := "nonexistent-team"
	rec := doRequest(srv, "PUT", "/api/schedules/"+created.ID, UpdateScheduleRequest{
		TeamID: &badTeam,
	})
	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400 for non-existent team", rec.Code)
	}
}

func TestDeleteSchedule(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "del-sched-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	createRec := doRequest(srv, "POST", "/api/schedules", CreateScheduleRequest{
		Name: "delete-me", TeamID: team.ID, Prompt: "test", CronExpression: "0 * * * *",
	})
	var created models.Schedule
	parseJSON(t, createRec, &created)

	rec := doRequest(srv, "DELETE", "/api/schedules/"+created.ID, nil)
	if rec.Code != 204 {
		t.Fatalf("status: got %d, want 204", rec.Code)
	}

	// Verify deletion.
	getRec := doRequest(srv, "GET", "/api/schedules/"+created.ID, nil)
	if getRec.Code != 404 {
		t.Fatalf("expected 404 after delete, got %d", getRec.Code)
	}
}

func TestDeleteSchedule_NotFound(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "DELETE", "/api/schedules/nonexistent", nil)
	if rec.Code != 404 {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
}

func TestDeleteSchedule_CascadeRuns(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "cascade-sched-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	createRec := doRequest(srv, "POST", "/api/schedules", CreateScheduleRequest{
		Name: "cascade-sched", TeamID: team.ID, Prompt: "test", CronExpression: "0 * * * *",
	})
	var schedule models.Schedule
	parseJSON(t, createRec, &schedule)

	// Manually create some runs in the DB.
	srv.db.Create(&models.ScheduleRun{ID: "run-c1", ScheduleID: schedule.ID, Status: "success"})
	srv.db.Create(&models.ScheduleRun{ID: "run-c2", ScheduleID: schedule.ID, Status: "failed"})

	// Delete schedule.
	doRequest(srv, "DELETE", "/api/schedules/"+schedule.ID, nil)

	// Verify runs are deleted.
	var count int64
	srv.db.Model(&models.ScheduleRun{}).Where("schedule_id = ?", schedule.ID).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 runs after cascade delete, got %d", count)
	}
}

// --- Toggle ---

func TestToggleSchedule(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "toggle-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	createRec := doRequest(srv, "POST", "/api/schedules", CreateScheduleRequest{
		Name: "toggle-me", TeamID: team.ID, Prompt: "test", CronExpression: "0 * * * *",
	})
	var schedule models.Schedule
	parseJSON(t, createRec, &schedule)

	if !schedule.Enabled {
		t.Fatal("expected initially enabled")
	}

	// Toggle off.
	rec := doRequest(srv, "PATCH", "/api/schedules/"+schedule.ID+"/toggle", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	var toggled models.Schedule
	parseJSON(t, rec, &toggled)
	if toggled.Enabled {
		t.Error("expected enabled to be false after toggle")
	}

	// Toggle on.
	rec2 := doRequest(srv, "PATCH", "/api/schedules/"+schedule.ID+"/toggle", nil)
	var toggledBack models.Schedule
	parseJSON(t, rec2, &toggledBack)
	if !toggledBack.Enabled {
		t.Error("expected enabled to be true after second toggle")
	}
}

func TestToggleSchedule_NotFound(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "PATCH", "/api/schedules/nonexistent/toggle", nil)
	if rec.Code != 404 {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
}

// --- Schedule Runs ---

func TestListScheduleRuns(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "runs-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	createRec := doRequest(srv, "POST", "/api/schedules", CreateScheduleRequest{
		Name: "runs-sched", TeamID: team.ID, Prompt: "test", CronExpression: "0 * * * *",
	})
	var schedule models.Schedule
	parseJSON(t, createRec, &schedule)

	// Create some runs directly.
	srv.db.Create(&models.ScheduleRun{ID: "run-1", ScheduleID: schedule.ID, Status: "success"})
	srv.db.Create(&models.ScheduleRun{ID: "run-2", ScheduleID: schedule.ID, Status: "failed"})
	srv.db.Create(&models.ScheduleRun{ID: "run-3", ScheduleID: schedule.ID, Status: "running"})

	rec := doRequest(srv, "GET", "/api/schedules/"+schedule.ID+"/runs", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	var result struct {
		Data    []models.ScheduleRun `json:"data"`
		Total   int64                `json:"total"`
		Page    int                  `json:"page"`
		PerPage int                  `json:"per_page"`
	}
	parseJSON(t, rec, &result)

	if result.Total != 3 {
		t.Errorf("total: got %d, want 3", result.Total)
	}
	if len(result.Data) != 3 {
		t.Errorf("data length: got %d, want 3", len(result.Data))
	}
	if result.Page != 1 {
		t.Errorf("page: got %d, want 1", result.Page)
	}
	if result.PerPage != 20 {
		t.Errorf("per_page: got %d, want 20", result.PerPage)
	}
}

func TestListScheduleRuns_Pagination(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "runs-page-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	createRec := doRequest(srv, "POST", "/api/schedules", CreateScheduleRequest{
		Name: "page-sched", TeamID: team.ID, Prompt: "test", CronExpression: "0 * * * *",
	})
	var schedule models.Schedule
	parseJSON(t, createRec, &schedule)

	// Create 5 runs.
	for i := 0; i < 5; i++ {
		srv.db.Create(&models.ScheduleRun{
			ID:         "run-p" + string(rune('a'+i)),
			ScheduleID: schedule.ID,
			Status:     "success",
		})
	}

	// Request page 1 with per_page=2.
	rec := doRequest(srv, "GET", "/api/schedules/"+schedule.ID+"/runs?page=1&per_page=2", nil)
	var result struct {
		Data    []models.ScheduleRun `json:"data"`
		Total   int64                `json:"total"`
		Page    int                  `json:"page"`
		PerPage int                  `json:"per_page"`
	}
	parseJSON(t, rec, &result)

	if result.Total != 5 {
		t.Errorf("total: got %d, want 5", result.Total)
	}
	if len(result.Data) != 2 {
		t.Errorf("data length: got %d, want 2", len(result.Data))
	}
	if result.Page != 1 {
		t.Errorf("page: got %d, want 1", result.Page)
	}
	if result.PerPage != 2 {
		t.Errorf("per_page: got %d, want 2", result.PerPage)
	}
}

func TestListScheduleRuns_ScheduleNotFound(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "GET", "/api/schedules/nonexistent/runs", nil)
	if rec.Code != 404 {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
}

func TestGetScheduleRun(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "get-run-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	createRec := doRequest(srv, "POST", "/api/schedules", CreateScheduleRequest{
		Name: "get-run-sched", TeamID: team.ID, Prompt: "test", CronExpression: "0 * * * *",
	})
	var schedule models.Schedule
	parseJSON(t, createRec, &schedule)

	srv.db.Create(&models.ScheduleRun{
		ID:         "run-detail",
		ScheduleID: schedule.ID,
		Status:     "success",
		Error:      "",
	})

	rec := doRequest(srv, "GET", "/api/schedules/"+schedule.ID+"/runs/run-detail", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	var run models.ScheduleRun
	parseJSON(t, rec, &run)
	if run.ID != "run-detail" {
		t.Errorf("id: got %q, want 'run-detail'", run.ID)
	}
	if run.Status != "success" {
		t.Errorf("status: got %q, want 'success'", run.Status)
	}
}

func TestGetScheduleRun_NotFound(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "run-nf-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	createRec := doRequest(srv, "POST", "/api/schedules", CreateScheduleRequest{
		Name: "run-nf-sched", TeamID: team.ID, Prompt: "test", CronExpression: "0 * * * *",
	})
	var schedule models.Schedule
	parseJSON(t, createRec, &schedule)

	rec := doRequest(srv, "GET", "/api/schedules/"+schedule.ID+"/runs/nonexistent", nil)
	if rec.Code != 404 {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
}

func TestGetScheduleRun_ScheduleNotFound(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "GET", "/api/schedules/nonexistent/runs/some-run", nil)
	if rec.Code != 404 {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
}

// --- Cron validation helpers ---

func TestValidateCronExpression(t *testing.T) {
	valid := []string{
		"0 * * * *",
		"*/5 * * * *",
		"0 9 * * 1-5",
		"30 8 1,15 * *",
		"0 0 * * 0",
	}
	for _, expr := range valid {
		if err := validateCronExpression(expr); err != nil {
			t.Errorf("expected valid cron %q, got error: %v", expr, err)
		}
	}

	invalid := []string{
		"",
		"* * *",
		"* * * * * *",
		"not a cron",
	}
	for _, expr := range invalid {
		if err := validateCronExpression(expr); err == nil {
			t.Errorf("expected error for invalid cron %q", expr)
		}
	}
}

func TestValidateTimezone(t *testing.T) {
	valid := []string{"UTC", "America/New_York", "Europe/London", "Asia/Tokyo"}
	for _, tz := range valid {
		if err := validateTimezone(tz); err != nil {
			t.Errorf("expected valid timezone %q, got error: %v", tz, err)
		}
	}

	invalid := []string{"Not/A/Zone", "Invalid", "Foo/Bar/Baz"}
	for _, tz := range invalid {
		if err := validateTimezone(tz); err == nil {
			t.Errorf("expected error for invalid timezone %q", tz)
		}
	}
}

func TestCronFieldMatches(t *testing.T) {
	tests := []struct {
		field string
		value int
		min   int
		max   int
		want  bool
	}{
		{"*", 5, 0, 59, true},
		{"5", 5, 0, 59, true},
		{"5", 6, 0, 59, false},
		{"1,5,10", 5, 0, 59, true},
		{"1,5,10", 3, 0, 59, false},
		{"1-5", 3, 0, 59, true},
		{"1-5", 6, 0, 59, false},
		{"*/15", 0, 0, 59, true},
		{"*/15", 15, 0, 59, true},
		{"*/15", 30, 0, 59, true},
		{"*/15", 7, 0, 59, false},
		{"1-10/3", 1, 0, 59, true},
		{"1-10/3", 4, 0, 59, true},
		{"1-10/3", 7, 0, 59, true},
		{"1-10/3", 2, 0, 59, false},
	}

	for _, tc := range tests {
		got := cronFieldMatches(tc.field, tc.value, tc.min, tc.max)
		if got != tc.want {
			t.Errorf("cronFieldMatches(%q, %d, %d, %d) = %v, want %v",
				tc.field, tc.value, tc.min, tc.max, got, tc.want)
		}
	}
}
