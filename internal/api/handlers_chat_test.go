package api

import (
	"encoding/json"
	"testing"

	"github.com/helmcode/agent-crew/internal/models"
	"github.com/helmcode/agent-crew/internal/protocol"
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

func TestGetMessages_LimitCappedAt500(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "limit-cap-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	// Requesting limit=999 should be capped at 500 (handler caps it).
	// We just verify the endpoint doesn't error with large limits.
	rec := doRequest(srv, "GET", "/api/teams/"+team.ID+"/messages?limit=999", nil)
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

func TestGetMessages_FiltersOutStatusUpdates(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "filter-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	// Insert a mix of message types directly into DB.
	types := []struct {
		msgType string
		from    string
	}{
		{"user_message", "user"},
		{"system_command", "system"},
		{"leader_response", "leader"},
		{"task_result", "leader"}, // backward compat: old records
		{"system_command", "system"},
		{"user_message", "user"},
	}

	for i, tt := range types {
		content, _ := json.Marshal(map[string]string{"content": "msg"})
		srv.db.Create(&models.TaskLog{
			ID:          "filter-" + string(rune('a'+i)),
			TeamID:      team.ID,
			FromAgent:   tt.from,
			ToAgent:     "leader",
			MessageType: tt.msgType,
			Payload:     models.JSON(content),
		})
	}

	// Default GetMessages should return user_message, leader_response, and task_result (backward compat).
	rec := doRequest(srv, "GET", "/api/teams/"+team.ID+"/messages", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var logs []models.TaskLog
	parseJSON(t, rec, &logs)

	// Should get: 2 user_message + 1 leader_response + 1 task_result = 4
	if len(logs) != 4 {
		t.Fatalf("filtered messages: got %d, want 4", len(logs))
	}

	allowed := map[string]bool{"user_message": true, "leader_response": true, "task_result": true}
	for _, log := range logs {
		if !allowed[log.MessageType] {
			t.Errorf("unexpected message type in filtered results: %q", log.MessageType)
		}
	}
}

func TestGetMessages_CustomTypesFilter(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "custom-types-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	// Insert different message types.
	for i, msgType := range []string{"user_message", "leader_response", "system_command"} {
		content, _ := json.Marshal(map[string]string{"content": "msg"})
		srv.db.Create(&models.TaskLog{
			ID:          "ct-" + string(rune('a'+i)),
			TeamID:      team.ID,
			FromAgent:   "user",
			ToAgent:     "leader",
			MessageType: msgType,
			Payload:     models.JSON(content),
		})
	}

	// Request only system_command types.
	rec := doRequest(srv, "GET", "/api/teams/"+team.ID+"/messages?types=system_command", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var logs []models.TaskLog
	parseJSON(t, rec, &logs)
	if len(logs) != 1 {
		t.Fatalf("custom type filter: got %d, want 1", len(logs))
	}
	if logs[0].MessageType != "system_command" {
		t.Errorf("message_type: got %q, want 'system_command'", logs[0].MessageType)
	}
}

func TestGetMessages_CursorPagination(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "cursor-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.db.Model(&team).Update("status", models.TeamStatusRunning)

	// Send messages via the chat endpoint to get real timestamps.
	doRequest(srv, "POST", "/api/teams/"+team.ID+"/chat", ChatRequest{Message: "msg-1"})
	doRequest(srv, "POST", "/api/teams/"+team.ID+"/chat", ChatRequest{Message: "msg-2"})
	doRequest(srv, "POST", "/api/teams/"+team.ID+"/chat", ChatRequest{Message: "msg-3"})

	// Get all messages to find a cursor.
	rec := doRequest(srv, "GET", "/api/teams/"+team.ID+"/messages", nil)
	var allLogs []models.TaskLog
	parseJSON(t, rec, &allLogs)
	if len(allLogs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(allLogs))
	}

	// Use the oldest message's timestamp as cursor to get nothing.
	oldest := allLogs[len(allLogs)-1]
	beforeParam := oldest.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")
	rec2 := doRequest(srv, "GET", "/api/teams/"+team.ID+"/messages?before="+beforeParam, nil)
	if rec2.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec2.Code)
	}

	var olderLogs []models.TaskLog
	parseJSON(t, rec2, &olderLogs)
	if len(olderLogs) != 0 {
		t.Fatalf("expected 0 messages before oldest, got %d", len(olderLogs))
	}
}

func TestGetMessages_InvalidBeforeTimestamp(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "bad-cursor-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	rec := doRequest(srv, "GET", "/api/teams/"+team.ID+"/messages?before=not-a-timestamp", nil)
	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400 for invalid timestamp", rec.Code)
	}
}

func TestGetActivity(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "activity-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	// Insert a mix of message types.
	for i, msgType := range []string{"user_message", "leader_response", "system_command"} {
		content, _ := json.Marshal(map[string]string{"content": "msg"})
		srv.db.Create(&models.TaskLog{
			ID:          "act-" + string(rune('a'+i)),
			TeamID:      team.ID,
			FromAgent:   "user",
			ToAgent:     "leader",
			MessageType: msgType,
			Payload:     models.JSON(content),
		})
	}

	// GetActivity should return ALL types (unfiltered).
	rec := doRequest(srv, "GET", "/api/teams/"+team.ID+"/activity", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var logs []models.TaskLog
	parseJSON(t, rec, &logs)
	if len(logs) != 3 {
		t.Fatalf("activity entries: got %d, want 3", len(logs))
	}
}

func TestGetActivity_TeamNotFound(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "GET", "/api/teams/nonexistent/activity", nil)
	if rec.Code != 404 {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
}

func TestGetMessages_IncludesRelayedLeaderResponses(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "relay-chat-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)
	srv.db.Model(&team).Update("status", models.TeamStatusRunning)

	// Simulate user sending a message via chat endpoint.
	doRequest(srv, "POST", "/api/teams/"+team.ID+"/chat", ChatRequest{Message: "hello agent"})

	// Simulate leader response arriving via the relay (as it does in production).
	data := buildRelayPayload(t, protocol.TypeLeaderResponse, "leader", "user",
		protocol.LeaderResponsePayload{Status: "completed", Result: "hello user"})
	if err := srv.processRelayMessage(team.ID, team.Name, data); err != nil {
		t.Fatalf("processRelayMessage returned error: %v", err)
	}

	// GetMessages should return both the user message and the leader response.
	rec := doRequest(srv, "GET", "/api/teams/"+team.ID+"/messages", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var logs []models.TaskLog
	parseJSON(t, rec, &logs)
	if len(logs) != 2 {
		t.Fatalf("messages: got %d, want 2 (user_message + leader_response)", len(logs))
	}

	types := map[string]bool{}
	for _, log := range logs {
		types[log.MessageType] = true
	}
	if !types["user_message"] {
		t.Error("expected user_message in chat messages")
	}
	if !types["leader_response"] {
		t.Error("expected leader_response in chat messages")
	}
}

func TestSplitCSV(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"a,b,c", []string{"a", "b", "c"}},
		{" a , b , c ", []string{"a", "b", "c"}},
		{"single", []string{"single"}},
		{",,,", nil},
		{"a,,b", []string{"a", "b"}},
	}

	for _, tt := range tests {
		got := splitCSV(tt.input)
		if len(got) != len(tt.expected) {
			t.Errorf("splitCSV(%q): got %v, want %v", tt.input, got, tt.expected)
			continue
		}
		for i := range got {
			if got[i] != tt.expected[i] {
				t.Errorf("splitCSV(%q)[%d]: got %q, want %q", tt.input, i, got[i], tt.expected[i])
			}
		}
	}
}
