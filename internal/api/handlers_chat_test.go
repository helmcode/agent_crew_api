package api

import (
	"encoding/json"
	"testing"

	"github.com/helmcode/agent-crew/internal/models"
)

func TestSendChat_SavesMessageToDB(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "chat-save-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	// Set team to running.
	srv.db.Model(&team).Update("status", models.TeamStatusRunning)

	// Send a chat message (NATS will fail but the message should still be saved to DB).
	rec := doRequest(srv, "POST", "/api/teams/"+team.ID+"/chat", ChatRequest{Message: "hello world"})

	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	// Verify the message was saved to the DB.
	var logs []models.TaskLog
	if err := srv.db.Where("team_id = ?", team.ID).Find(&logs).Error; err != nil {
		t.Fatalf("querying task logs: %v", err)
	}

	if len(logs) != 1 {
		t.Fatalf("task logs: got %d, want 1", len(logs))
	}

	log := logs[0]
	if log.FromAgent != "user" {
		t.Errorf("from_agent: got %q, want 'user'", log.FromAgent)
	}
	if log.ToAgent != "leader" {
		t.Errorf("to_agent: got %q, want 'leader'", log.ToAgent)
	}
	if log.MessageType != "user_message" {
		t.Errorf("message_type: got %q, want 'user_message'", log.MessageType)
	}
	if log.ID == "" {
		t.Error("expected non-empty task log ID")
	}

	// Verify the payload contains the message content.
	var payload map[string]string
	if err := json.Unmarshal(log.Payload, &payload); err != nil {
		t.Fatalf("parsing payload: %v", err)
	}
	if payload["content"] != "hello world" {
		t.Errorf("payload content: got %q, want 'hello world'", payload["content"])
	}
}

func TestSendChat_MultipleMessages(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "chat-multi-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.db.Model(&team).Update("status", models.TeamStatusRunning)

	// Send multiple messages.
	messages := []string{"first", "second", "third"}
	for _, msg := range messages {
		doRequest(srv, "POST", "/api/teams/"+team.ID+"/chat", ChatRequest{Message: msg})
	}

	// Verify all messages were saved.
	var count int64
	srv.db.Model(&models.TaskLog{}).Where("team_id = ?", team.ID).Count(&count)
	if count != 3 {
		t.Fatalf("task log count: got %d, want 3", count)
	}
}

func TestGetMessages_Pagination(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "pagination-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.db.Model(&team).Update("status", models.TeamStatusRunning)

	// Insert 5 messages directly into DB for reliable ordering.
	for i := 0; i < 5; i++ {
		content, _ := json.Marshal(map[string]string{"content": "msg"})
		srv.db.Create(&models.TaskLog{
			ID:          "log-" + string(rune('a'+i)),
			TeamID:      team.ID,
			FromAgent:   "user",
			ToAgent:     "leader",
			MessageType: "user_message",
			Payload:     models.JSON(content),
		})
	}

	// Request with limit=2.
	rec := doRequest(srv, "GET", "/api/teams/"+team.ID+"/messages?limit=2", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var logs []models.TaskLog
	parseJSON(t, rec, &logs)
	if len(logs) != 2 {
		t.Fatalf("messages with limit=2: got %d, want 2", len(logs))
	}

	// Request without limit should return all (default 50).
	rec2 := doRequest(srv, "GET", "/api/teams/"+team.ID+"/messages", nil)
	var allLogs []models.TaskLog
	parseJSON(t, rec2, &allLogs)
	if len(allLogs) != 5 {
		t.Fatalf("messages without limit: got %d, want 5", len(allLogs))
	}
}

func TestGetMessages_LimitCappedAt200(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "limit-cap-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	// Requesting limit=500 should be capped at 200 (handler caps it).
	// We just verify the endpoint doesn't error with large limits.
	rec := doRequest(srv, "GET", "/api/teams/"+team.ID+"/messages?limit=500", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
}

func TestGetMessages_OrderDescending(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "order-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.db.Model(&team).Update("status", models.TeamStatusRunning)

	// Send messages sequentially.
	doRequest(srv, "POST", "/api/teams/"+team.ID+"/chat", ChatRequest{Message: "first"})
	doRequest(srv, "POST", "/api/teams/"+team.ID+"/chat", ChatRequest{Message: "second"})

	rec := doRequest(srv, "GET", "/api/teams/"+team.ID+"/messages", nil)
	var logs []models.TaskLog
	parseJSON(t, rec, &logs)

	if len(logs) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(logs))
	}

	// Messages should be ordered DESC (most recent first).
	if logs[0].CreatedAt.Before(logs[1].CreatedAt) {
		t.Error("messages should be ordered by created_at DESC")
	}
}

func TestSendChat_InvalidBody(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "bad-body-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.db.Model(&team).Update("status", models.TeamStatusRunning)

	// Send a request with empty message.
	rec := doRequest(srv, "POST", "/api/teams/"+team.ID+"/chat", ChatRequest{Message: ""})
	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400 for empty message", rec.Code)
	}
}
