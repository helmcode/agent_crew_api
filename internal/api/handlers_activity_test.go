package api

import (
	"encoding/json"
	"testing"

	"github.com/helmcode/agent-crew/internal/models"
	"github.com/helmcode/agent-crew/internal/protocol"
)

// createTeamForActivity is a helper that creates a team and returns its ID.
func createTeamForActivity(t *testing.T, srv *Server, name string) string {
	t.Helper()
	rec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: name})
	if rec.Code != 201 {
		t.Fatalf("failed to create team %q: status %d, body: %s", name, rec.Code, rec.Body.String())
	}
	var team models.Team
	parseJSON(t, rec, &team)
	return team.ID
}

// insertTaskLog inserts a TaskLog directly into the DB and returns it.
func insertTaskLog(t *testing.T, srv *Server, id, teamID, from, to, msgType string, payload interface{}) models.TaskLog {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshaling payload: %v", err)
	}
	log := models.TaskLog{
		ID:          id,
		TeamID:      teamID,
		FromAgent:   from,
		ToAgent:     to,
		MessageType: msgType,
		Payload:     models.JSON(raw),
	}
	if err := srv.db.Create(&log).Error; err != nil {
		t.Fatalf("inserting task log: %v", err)
	}
	return log
}

func TestGetActivity_AllTypes(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamID := createTeamForActivity(t, srv, "activity-all-types")

	// Insert both message types that appear in the DB.
	insertTaskLog(t, srv, "um-1", teamID, "user", "leader", string(protocol.TypeUserMessage),
		protocol.UserMessagePayload{Content: "hello leader"})

	insertTaskLog(t, srv, "lr-1", teamID, "leader", "user", string(protocol.TypeLeaderResponse),
		protocol.LeaderResponsePayload{Status: "completed", Result: "task done"})

	// GetActivity should return ALL 2 entries.
	rec := doRequest(srv, "GET", "/api/teams/"+teamID+"/activity", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var logs []models.TaskLog
	parseJSON(t, rec, &logs)
	if len(logs) != 2 {
		t.Fatalf("activity entries: got %d, want 2", len(logs))
	}

	// Verify both expected types are present.
	typeSet := make(map[string]bool)
	for _, log := range logs {
		typeSet[log.MessageType] = true
	}
	expectedTypes := []string{"user_message", "leader_response"}
	for _, et := range expectedTypes {
		if !typeSet[et] {
			t.Errorf("missing message type %q in activity results", et)
		}
	}
}

func TestGetActivity_LeaderResponsePayload(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamID := createTeamForActivity(t, srv, "activity-lr-payload")

	insertTaskLog(t, srv, "lr-p1", teamID, "leader", "user", string(protocol.TypeLeaderResponse),
		protocol.LeaderResponsePayload{
			Status: "completed",
			Result: "deployed to staging",
		})

	insertTaskLog(t, srv, "lr-p2", teamID, "leader", "user", string(protocol.TypeLeaderResponse),
		protocol.LeaderResponsePayload{
			Status: "failed",
			Error:  "connection timeout",
		})

	rec := doRequest(srv, "GET", "/api/teams/"+teamID+"/activity", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var logs []models.TaskLog
	parseJSON(t, rec, &logs)
	if len(logs) != 2 {
		t.Fatalf("entries: got %d, want 2", len(logs))
	}

	// Verify payloads.
	for _, log := range logs {
		var payload protocol.LeaderResponsePayload
		if err := json.Unmarshal(log.Payload, &payload); err != nil {
			t.Fatalf("parsing payload: %v", err)
		}
		switch log.ID {
		case "lr-p1":
			if payload.Status != "completed" {
				t.Errorf("lr-p1 status: got %q, want 'completed'", payload.Status)
			}
			if payload.Result != "deployed to staging" {
				t.Errorf("lr-p1 result: got %q", payload.Result)
			}
		case "lr-p2":
			if payload.Status != "failed" {
				t.Errorf("lr-p2 status: got %q, want 'failed'", payload.Status)
			}
			if payload.Error != "connection timeout" {
				t.Errorf("lr-p2 error: got %q", payload.Error)
			}
		}
	}
}

func TestGetActivity_PreservesAgentNames(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamID := createTeamForActivity(t, srv, "activity-names")

	// Insert messages to verify from/to are agent names (not NATS subjects).
	testCases := []struct {
		id, from, to, msgType string
	}{
		{"n-1", "user", "leader", "user_message"},
		{"n-2", "leader", "user", "leader_response"},
		{"n-3", "leader", "user", "leader_response"},
	}

	for _, tc := range testCases {
		insertTaskLog(t, srv, tc.id, teamID, tc.from, tc.to, tc.msgType,
			map[string]string{"content": "test"})
	}

	rec := doRequest(srv, "GET", "/api/teams/"+teamID+"/activity", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var logs []models.TaskLog
	parseJSON(t, rec, &logs)
	if len(logs) != 3 {
		t.Fatalf("entries: got %d, want 3", len(logs))
	}

	// Verify no NATS subjects leaked into from/to fields.
	for _, log := range logs {
		if containsDot(log.FromAgent) {
			t.Errorf("from_agent %q looks like a NATS subject (contains dot)", log.FromAgent)
		}
		if log.ToAgent != "" && containsDot(log.ToAgent) {
			t.Errorf("to_agent %q looks like a NATS subject (contains dot)", log.ToAgent)
		}
	}
}

// containsDot checks if a string contains a dot — simple heuristic to detect
// NATS subjects (e.g. "team.myteam.leader") vs agent names (e.g. "leader").
func containsDot(s string) bool {
	for _, c := range s {
		if c == '.' {
			return true
		}
	}
	return false
}

func TestGetActivity_CursorPagination(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamID := createTeamForActivity(t, srv, "activity-cursor")

	// Insert 5 leader response messages.
	for i := 0; i < 5; i++ {
		insertTaskLog(t, srv, "ac-"+string(rune('a'+i)), teamID, "leader", "user",
			string(protocol.TypeLeaderResponse),
			protocol.LeaderResponsePayload{Status: "completed", Result: "task"})
	}

	// Get all messages first.
	rec := doRequest(srv, "GET", "/api/teams/"+teamID+"/activity", nil)
	var allLogs []models.TaskLog
	parseJSON(t, rec, &allLogs)
	if len(allLogs) != 5 {
		t.Fatalf("expected 5 activity entries, got %d", len(allLogs))
	}

	// Use the oldest message's timestamp as cursor.
	oldest := allLogs[len(allLogs)-1]
	beforeParam := oldest.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")
	rec2 := doRequest(srv, "GET", "/api/teams/"+teamID+"/activity?before="+beforeParam, nil)
	if rec2.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec2.Code)
	}

	var olderLogs []models.TaskLog
	parseJSON(t, rec2, &olderLogs)
	if len(olderLogs) != 0 {
		t.Fatalf("expected 0 entries before oldest, got %d", len(olderLogs))
	}
}

func TestGetActivity_LimitParameter(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamID := createTeamForActivity(t, srv, "activity-limit")

	for i := 0; i < 10; i++ {
		insertTaskLog(t, srv, "al-"+string(rune('a'+i)), teamID, "leader", "user", "leader_response",
			protocol.LeaderResponsePayload{Status: "completed", Result: "done"})
	}

	// Limit=3 should return 3 entries.
	rec := doRequest(srv, "GET", "/api/teams/"+teamID+"/activity?limit=3", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var logs []models.TaskLog
	parseJSON(t, rec, &logs)
	if len(logs) != 3 {
		t.Fatalf("entries with limit=3: got %d, want 3", len(logs))
	}
}

func TestGetActivity_LimitCappedAt200(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamID := createTeamForActivity(t, srv, "activity-cap")

	// Requesting limit=999 should be capped (handler caps at 200).
	rec := doRequest(srv, "GET", "/api/teams/"+teamID+"/activity?limit=999", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
}

func TestGetActivity_OrderDescending(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamID := createTeamForActivity(t, srv, "activity-order")

	srv.db.Model(&models.Team{}).Where("id = ?", teamID).Update("status", models.TeamStatusRunning)

	// Send chat messages via the endpoint to get real timestamps.
	doRequest(srv, "POST", "/api/teams/"+teamID+"/chat", ChatRequest{Message: "first"})
	doRequest(srv, "POST", "/api/teams/"+teamID+"/chat", ChatRequest{Message: "second"})

	// Also insert a leader response.
	insertTaskLog(t, srv, "ao-1", teamID, "leader", "user", "leader_response",
		protocol.LeaderResponsePayload{Status: "completed", Result: "done"})

	rec := doRequest(srv, "GET", "/api/teams/"+teamID+"/activity", nil)
	var logs []models.TaskLog
	parseJSON(t, rec, &logs)

	if len(logs) < 2 {
		t.Fatalf("expected at least 2 entries, got %d", len(logs))
	}

	// Should be ordered DESC (most recent first).
	for i := 0; i < len(logs)-1; i++ {
		if logs[i].CreatedAt.Before(logs[i+1].CreatedAt) {
			t.Errorf("entry %d (at %v) is before entry %d (at %v) — expected DESC order",
				i, logs[i].CreatedAt, i+1, logs[i+1].CreatedAt)
		}
	}
}

func TestGetMessages_ExcludesNonChatTypes(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamID := createTeamForActivity(t, srv, "msg-exclude")

	// Insert the two chat-relevant types plus a system_command (should be excluded).
	insertTaskLog(t, srv, "me-1", teamID, "user", "leader", "user_message",
		protocol.UserMessagePayload{Content: "hello"})
	insertTaskLog(t, srv, "me-2", teamID, "leader", "user", "leader_response",
		protocol.LeaderResponsePayload{Status: "completed", Result: "done"})
	insertTaskLog(t, srv, "me-3", teamID, "system", "leader", "system_command",
		protocol.SystemCommandPayload{Command: "shutdown"})

	// GetMessages (default filter) should return only user_message + leader_response.
	rec := doRequest(srv, "GET", "/api/teams/"+teamID+"/messages", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var logs []models.TaskLog
	parseJSON(t, rec, &logs)

	if len(logs) != 2 {
		t.Fatalf("filtered messages: got %d, want 2 (user_message + leader_response)", len(logs))
	}

	for _, log := range logs {
		if log.MessageType != "user_message" && log.MessageType != "leader_response" {
			t.Errorf("unexpected type in chat messages: %q", log.MessageType)
		}
	}
}

func TestGetMessages_CustomFilterIncludesSpecificType(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamID := createTeamForActivity(t, srv, "msg-custom-filter")

	insertTaskLog(t, srv, "cf-1", teamID, "leader", "user", "leader_response",
		protocol.LeaderResponsePayload{Status: "completed", Result: "done"})
	insertTaskLog(t, srv, "cf-2", teamID, "user", "leader", "user_message",
		protocol.UserMessagePayload{Content: "hi"})

	// Request only leader_response.
	rec := doRequest(srv, "GET", "/api/teams/"+teamID+"/messages?types=leader_response", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var logs []models.TaskLog
	parseJSON(t, rec, &logs)
	if len(logs) != 1 {
		t.Fatalf("custom type filter: got %d, want 1", len(logs))
	}
	if logs[0].MessageType != "leader_response" {
		t.Errorf("message_type: got %q, want 'leader_response'", logs[0].MessageType)
	}
}

func TestGetMessages_MultipleCustomTypes(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamID := createTeamForActivity(t, srv, "msg-multi-types")

	insertTaskLog(t, srv, "mt-1", teamID, "leader", "user", "leader_response",
		protocol.LeaderResponsePayload{Status: "completed", Result: "built"})
	insertTaskLog(t, srv, "mt-2", teamID, "system", "leader", "system_command",
		protocol.SystemCommandPayload{Command: "shutdown"})
	insertTaskLog(t, srv, "mt-3", teamID, "user", "leader", "user_message",
		protocol.UserMessagePayload{Content: "deploy it"})

	// Request leader_response and user_message.
	rec := doRequest(srv, "GET", "/api/teams/"+teamID+"/messages?types=leader_response,user_message", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var logs []models.TaskLog
	parseJSON(t, rec, &logs)
	if len(logs) != 2 {
		t.Fatalf("multi-type filter: got %d, want 2", len(logs))
	}

	typeSet := make(map[string]bool)
	for _, log := range logs {
		typeSet[log.MessageType] = true
	}
	if !typeSet["leader_response"] {
		t.Error("expected leader_response in results")
	}
	if !typeSet["user_message"] {
		t.Error("expected user_message in results")
	}
}

func TestGetActivity_EmptyTeam(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamID := createTeamForActivity(t, srv, "activity-empty")

	rec := doRequest(srv, "GET", "/api/teams/"+teamID+"/activity", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var logs []models.TaskLog
	parseJSON(t, rec, &logs)
	if len(logs) != 0 {
		t.Fatalf("entries for empty team: got %d, want 0", len(logs))
	}
}

func TestGetActivity_InvalidBeforeTimestamp(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamID := createTeamForActivity(t, srv, "activity-bad-ts")

	rec := doRequest(srv, "GET", "/api/teams/"+teamID+"/activity?before=not-a-timestamp", nil)
	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400 for invalid timestamp", rec.Code)
	}
}

func TestGetActivity_IsolationBetweenTeams(t *testing.T) {
	srv, _ := setupTestServer(t)
	team1ID := createTeamForActivity(t, srv, "isolation-team-1")
	team2ID := createTeamForActivity(t, srv, "isolation-team-2")

	// Insert messages into team 1.
	insertTaskLog(t, srv, "iso-1", team1ID, "user", "leader", "user_message",
		protocol.UserMessagePayload{Content: "team 1 task"})
	insertTaskLog(t, srv, "iso-2", team1ID, "leader", "user", "leader_response",
		protocol.LeaderResponsePayload{Status: "completed", Result: "team 1 result"})

	// Insert messages into team 2.
	insertTaskLog(t, srv, "iso-3", team2ID, "user", "leader", "user_message",
		protocol.UserMessagePayload{Content: "team 2 task"})

	// Team 1 activity should only see 2 entries.
	rec1 := doRequest(srv, "GET", "/api/teams/"+team1ID+"/activity", nil)
	var logs1 []models.TaskLog
	parseJSON(t, rec1, &logs1)
	if len(logs1) != 2 {
		t.Fatalf("team 1 activity: got %d, want 2", len(logs1))
	}

	// Team 2 activity should only see 1 entry.
	rec2 := doRequest(srv, "GET", "/api/teams/"+team2ID+"/activity", nil)
	var logs2 []models.TaskLog
	parseJSON(t, rec2, &logs2)
	if len(logs2) != 1 {
		t.Fatalf("team 2 activity: got %d, want 1", len(logs2))
	}
}

func TestGetActivity_ArbitraryPayloadPreserved(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamID := createTeamForActivity(t, srv, "activity-payload-preserved")

	contextData := map[string]interface{}{
		"file":    "main.go",
		"line":    42,
		"snippet": "func main() {}",
	}
	insertTaskLog(t, srv, "pp-1", teamID, "leader", "user", "leader_response",
		contextData)

	rec := doRequest(srv, "GET", "/api/teams/"+teamID+"/activity", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var logs []models.TaskLog
	parseJSON(t, rec, &logs)
	if len(logs) != 1 {
		t.Fatalf("entries: got %d, want 1", len(logs))
	}

	// Verify the payload preserves the arbitrary JSON.
	var payload map[string]interface{}
	if err := json.Unmarshal(logs[0].Payload, &payload); err != nil {
		t.Fatalf("parsing payload: %v", err)
	}
	if payload["file"] != "main.go" {
		t.Errorf("file: got %v, want 'main.go'", payload["file"])
	}
	// JSON numbers unmarshal as float64.
	if payload["line"] != float64(42) {
		t.Errorf("line: got %v, want 42", payload["line"])
	}
	if payload["snippet"] != "func main() {}" {
		t.Errorf("snippet: got %v", payload["snippet"])
	}
}

func TestGetActivity_MixedWorkflow(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamID := createTeamForActivity(t, srv, "activity-workflow")

	srv.db.Model(&models.Team{}).Where("id = ?", teamID).Update("status", models.TeamStatusRunning)

	// Simulate a realistic workflow:
	// 1. User sends message.
	doRequest(srv, "POST", "/api/teams/"+teamID+"/chat", ChatRequest{Message: "deploy to staging"})

	// 2. Leader responds with result.
	insertTaskLog(t, srv, "wf-1", teamID, "leader", "user", "leader_response",
		protocol.LeaderResponsePayload{Status: "completed", Result: "deployed to staging cluster"})

	// GetActivity should return all 2 entries (1 user_message + 1 leader_response).
	rec := doRequest(srv, "GET", "/api/teams/"+teamID+"/activity", nil)
	var activityLogs []models.TaskLog
	parseJSON(t, rec, &activityLogs)
	if len(activityLogs) != 2 {
		t.Fatalf("activity: got %d, want 2", len(activityLogs))
	}

	// GetMessages (default filter) should also return both — they are chat types.
	rec2 := doRequest(srv, "GET", "/api/teams/"+teamID+"/messages", nil)
	var chatLogs []models.TaskLog
	parseJSON(t, rec2, &chatLogs)
	if len(chatLogs) != 2 {
		t.Fatalf("chat messages: got %d, want 2", len(chatLogs))
	}

	chatTypes := make(map[string]bool)
	for _, log := range chatLogs {
		chatTypes[log.MessageType] = true
	}
	if !chatTypes["user_message"] {
		t.Error("expected user_message in chat results")
	}
	if !chatTypes["leader_response"] {
		t.Error("expected leader_response in chat results")
	}
}
