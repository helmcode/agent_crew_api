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

func TestGetActivity_AllInterAgentTypes(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamID := createTeamForActivity(t, srv, "activity-all-types")

	// Insert one of each inter-agent message type.
	insertTaskLog(t, srv, "ta-1", teamID, "leader", "worker-1", string(protocol.TypeTaskAssignment),
		protocol.TaskAssignmentPayload{Instruction: "implement feature X"})

	insertTaskLog(t, srv, "tr-1", teamID, "worker-1", "leader", string(protocol.TypeTaskResult),
		protocol.TaskResultPayload{Status: "completed", Result: "feature X done"})

	insertTaskLog(t, srv, "su-1", teamID, "worker-1", "", string(protocol.TypeStatusUpdate),
		protocol.StatusUpdatePayload{Agent: "worker-1", Status: "working", CurrentTask: "feature X"})

	insertTaskLog(t, srv, "q-1", teamID, "leader", "worker-1", string(protocol.TypeQuestion),
		protocol.QuestionPayload{Question: "which database?", Options: []string{"postgres", "mysql"}})

	insertTaskLog(t, srv, "cs-1", teamID, "worker-1", "leader", string(protocol.TypeContextShare),
		map[string]string{"context": "shared data"})

	insertTaskLog(t, srv, "um-1", teamID, "user", "leader", string(protocol.TypeUserMessage),
		protocol.UserMessagePayload{Content: "hello team"})

	// GetActivity should return ALL 6 entries.
	rec := doRequest(srv, "GET", "/api/teams/"+teamID+"/activity", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var logs []models.TaskLog
	parseJSON(t, rec, &logs)
	if len(logs) != 6 {
		t.Fatalf("activity entries: got %d, want 6", len(logs))
	}

	// Verify all expected types are present.
	typeSet := make(map[string]bool)
	for _, log := range logs {
		typeSet[log.MessageType] = true
	}
	expectedTypes := []string{
		"task_assignment", "task_result", "status_update",
		"question", "context_share", "user_message",
	}
	for _, et := range expectedTypes {
		if !typeSet[et] {
			t.Errorf("missing message type %q in activity results", et)
		}
	}
}

func TestGetActivity_TaskAssignmentPayload(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamID := createTeamForActivity(t, srv, "activity-ta-payload")

	insertTaskLog(t, srv, "ta-p1", teamID, "leader", "worker-1", string(protocol.TypeTaskAssignment),
		protocol.TaskAssignmentPayload{
			Instruction:     "run integration tests",
			ExpectedOutput:  "all tests pass",
			DeadlineSeconds: 300,
		})

	rec := doRequest(srv, "GET", "/api/teams/"+teamID+"/activity", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var logs []models.TaskLog
	parseJSON(t, rec, &logs)
	if len(logs) != 1 {
		t.Fatalf("entries: got %d, want 1", len(logs))
	}

	// Verify from/to fields.
	if logs[0].FromAgent != "leader" {
		t.Errorf("from_agent: got %q, want 'leader'", logs[0].FromAgent)
	}
	if logs[0].ToAgent != "worker-1" {
		t.Errorf("to_agent: got %q, want 'worker-1'", logs[0].ToAgent)
	}

	// Verify payload content.
	var payload protocol.TaskAssignmentPayload
	if err := json.Unmarshal(logs[0].Payload, &payload); err != nil {
		t.Fatalf("parsing payload: %v", err)
	}
	if payload.Instruction != "run integration tests" {
		t.Errorf("instruction: got %q", payload.Instruction)
	}
	if payload.ExpectedOutput != "all tests pass" {
		t.Errorf("expected_output: got %q", payload.ExpectedOutput)
	}
	if payload.DeadlineSeconds != 300 {
		t.Errorf("deadline_seconds: got %d, want 300", payload.DeadlineSeconds)
	}
}

func TestGetActivity_TaskResultPayload(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamID := createTeamForActivity(t, srv, "activity-tr-payload")

	insertTaskLog(t, srv, "tr-p1", teamID, "worker-1", "leader", string(protocol.TypeTaskResult),
		protocol.TaskResultPayload{
			Status:    "completed",
			Result:    "deployed to staging",
			Artifacts: []string{"build.log", "deploy.log"},
		})

	insertTaskLog(t, srv, "tr-p2", teamID, "worker-2", "leader", string(protocol.TypeTaskResult),
		protocol.TaskResultPayload{
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

	// Verify the completed result (ordered DESC, so most recent first).
	for _, log := range logs {
		var payload protocol.TaskResultPayload
		if err := json.Unmarshal(log.Payload, &payload); err != nil {
			t.Fatalf("parsing payload: %v", err)
		}
		switch log.FromAgent {
		case "worker-1":
			if payload.Status != "completed" {
				t.Errorf("worker-1 status: got %q, want 'completed'", payload.Status)
			}
			if payload.Result != "deployed to staging" {
				t.Errorf("worker-1 result: got %q", payload.Result)
			}
		case "worker-2":
			if payload.Status != "failed" {
				t.Errorf("worker-2 status: got %q, want 'failed'", payload.Status)
			}
			if payload.Error != "connection timeout" {
				t.Errorf("worker-2 error: got %q", payload.Error)
			}
		}
	}
}

func TestGetActivity_StatusUpdatePayload(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamID := createTeamForActivity(t, srv, "activity-su-payload")

	insertTaskLog(t, srv, "su-p1", teamID, "worker-1", "", string(protocol.TypeStatusUpdate),
		protocol.StatusUpdatePayload{
			Agent:           "worker-1",
			Status:          "working",
			CurrentTask:     "building Docker image",
			TasksCompleted:  3,
			TasksFailed:     0,
			ContextUsagePct: 42,
		})

	rec := doRequest(srv, "GET", "/api/teams/"+teamID+"/activity", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var logs []models.TaskLog
	parseJSON(t, rec, &logs)
	if len(logs) != 1 {
		t.Fatalf("entries: got %d, want 1", len(logs))
	}

	var payload protocol.StatusUpdatePayload
	if err := json.Unmarshal(logs[0].Payload, &payload); err != nil {
		t.Fatalf("parsing payload: %v", err)
	}
	if payload.Agent != "worker-1" {
		t.Errorf("agent: got %q", payload.Agent)
	}
	if payload.Status != "working" {
		t.Errorf("status: got %q", payload.Status)
	}
	if payload.CurrentTask != "building Docker image" {
		t.Errorf("current_task: got %q", payload.CurrentTask)
	}
	if payload.TasksCompleted != 3 {
		t.Errorf("tasks_completed: got %d, want 3", payload.TasksCompleted)
	}
	if payload.ContextUsagePct != 42 {
		t.Errorf("context_usage_pct: got %d, want 42", payload.ContextUsagePct)
	}
}

func TestGetActivity_QuestionPayload(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamID := createTeamForActivity(t, srv, "activity-q-payload")

	insertTaskLog(t, srv, "q-p1", teamID, "worker-2", "leader", string(protocol.TypeQuestion),
		protocol.QuestionPayload{
			Question: "Should I use Redis or Memcached for caching?",
			Options:  []string{"redis", "memcached", "neither"},
		})

	rec := doRequest(srv, "GET", "/api/teams/"+teamID+"/activity", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var logs []models.TaskLog
	parseJSON(t, rec, &logs)
	if len(logs) != 1 {
		t.Fatalf("entries: got %d, want 1", len(logs))
	}

	var payload protocol.QuestionPayload
	if err := json.Unmarshal(logs[0].Payload, &payload); err != nil {
		t.Fatalf("parsing payload: %v", err)
	}
	if payload.Question != "Should I use Redis or Memcached for caching?" {
		t.Errorf("question: got %q", payload.Question)
	}
	if len(payload.Options) != 3 {
		t.Fatalf("options: got %d, want 3", len(payload.Options))
	}
	if payload.Options[0] != "redis" {
		t.Errorf("options[0]: got %q, want 'redis'", payload.Options[0])
	}
}

func TestGetActivity_PreservesAgentNames(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamID := createTeamForActivity(t, srv, "activity-names")

	// Insert messages between various agents to verify from/to are agent
	// names (not NATS subjects). This was the bug fixed in bridge.go.
	testCases := []struct {
		id, from, to, msgType string
	}{
		{"n-1", "leader", "worker-1", "task_assignment"},
		{"n-2", "worker-1", "leader", "task_result"},
		{"n-3", "leader", "worker-2", "task_assignment"},
		{"n-4", "worker-2", "leader", "task_result"},
		{"n-5", "worker-1", "", "status_update"},
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
	if len(logs) != 5 {
		t.Fatalf("entries: got %d, want 5", len(logs))
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

	// Insert 5 inter-agent messages.
	for i := 0; i < 5; i++ {
		insertTaskLog(t, srv, "ac-"+string(rune('a'+i)), teamID, "leader", "worker-1",
			string(protocol.TypeTaskAssignment),
			protocol.TaskAssignmentPayload{Instruction: "task"})
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
		insertTaskLog(t, srv, "al-"+string(rune('a'+i)), teamID, "worker-1", "", "status_update",
			protocol.StatusUpdatePayload{Agent: "worker-1", Status: "working"})
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

	// Also insert an inter-agent message.
	insertTaskLog(t, srv, "ao-1", teamID, "worker-1", "leader", "task_result",
		protocol.TaskResultPayload{Status: "completed", Result: "done"})

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

func TestGetMessages_ExcludesInterAgentOperational(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamID := createTeamForActivity(t, srv, "msg-exclude")

	// Insert a full set of message types.
	insertTaskLog(t, srv, "me-1", teamID, "user", "leader", "user_message",
		protocol.UserMessagePayload{Content: "hello"})
	insertTaskLog(t, srv, "me-2", teamID, "leader", "worker-1", "task_assignment",
		protocol.TaskAssignmentPayload{Instruction: "do work"})
	insertTaskLog(t, srv, "me-3", teamID, "worker-1", "", "status_update",
		protocol.StatusUpdatePayload{Agent: "worker-1", Status: "working"})
	insertTaskLog(t, srv, "me-4", teamID, "worker-1", "leader", "task_result",
		protocol.TaskResultPayload{Status: "completed", Result: "done"})
	insertTaskLog(t, srv, "me-5", teamID, "leader", "worker-2", "question",
		protocol.QuestionPayload{Question: "which tool?"})
	insertTaskLog(t, srv, "me-6", teamID, "worker-2", "leader", "context_share",
		map[string]string{"key": "value"})

	// GetMessages (default filter) should return only user_message + task_result.
	rec := doRequest(srv, "GET", "/api/teams/"+teamID+"/messages", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var logs []models.TaskLog
	parseJSON(t, rec, &logs)

	if len(logs) != 2 {
		t.Fatalf("filtered messages: got %d, want 2 (user_message + task_result)", len(logs))
	}

	for _, log := range logs {
		if log.MessageType != "user_message" && log.MessageType != "task_result" {
			t.Errorf("unexpected type in chat messages: %q", log.MessageType)
		}
	}
}

func TestGetMessages_CustomFilterIncludesOperational(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamID := createTeamForActivity(t, srv, "msg-custom-filter")

	insertTaskLog(t, srv, "cf-1", teamID, "leader", "worker-1", "task_assignment",
		protocol.TaskAssignmentPayload{Instruction: "deploy"})
	insertTaskLog(t, srv, "cf-2", teamID, "worker-1", "", "status_update",
		protocol.StatusUpdatePayload{Agent: "worker-1", Status: "working"})
	insertTaskLog(t, srv, "cf-3", teamID, "user", "leader", "user_message",
		protocol.UserMessagePayload{Content: "hi"})

	// Request only task_assignment.
	rec := doRequest(srv, "GET", "/api/teams/"+teamID+"/messages?types=task_assignment", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var logs []models.TaskLog
	parseJSON(t, rec, &logs)
	if len(logs) != 1 {
		t.Fatalf("custom type filter: got %d, want 1", len(logs))
	}
	if logs[0].MessageType != "task_assignment" {
		t.Errorf("message_type: got %q, want 'task_assignment'", logs[0].MessageType)
	}
}

func TestGetMessages_MultipleCustomTypes(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamID := createTeamForActivity(t, srv, "msg-multi-types")

	insertTaskLog(t, srv, "mt-1", teamID, "leader", "worker-1", "task_assignment",
		protocol.TaskAssignmentPayload{Instruction: "build"})
	insertTaskLog(t, srv, "mt-2", teamID, "worker-1", "", "status_update",
		protocol.StatusUpdatePayload{Agent: "worker-1", Status: "idle"})
	insertTaskLog(t, srv, "mt-3", teamID, "worker-1", "leader", "question",
		protocol.QuestionPayload{Question: "which env?"})
	insertTaskLog(t, srv, "mt-4", teamID, "user", "leader", "user_message",
		protocol.UserMessagePayload{Content: "deploy it"})

	// Request task_assignment and question.
	rec := doRequest(srv, "GET", "/api/teams/"+teamID+"/messages?types=task_assignment,question", nil)
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
	if !typeSet["task_assignment"] {
		t.Error("expected task_assignment in results")
	}
	if !typeSet["question"] {
		t.Error("expected question in results")
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
	insertTaskLog(t, srv, "iso-1", team1ID, "leader", "worker-1", "task_assignment",
		protocol.TaskAssignmentPayload{Instruction: "team 1 task"})
	insertTaskLog(t, srv, "iso-2", team1ID, "worker-1", "leader", "task_result",
		protocol.TaskResultPayload{Status: "completed", Result: "team 1 result"})

	// Insert messages into team 2.
	insertTaskLog(t, srv, "iso-3", team2ID, "leader", "worker-a", "task_assignment",
		protocol.TaskAssignmentPayload{Instruction: "team 2 task"})

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

func TestGetActivity_ContextSharePayload(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamID := createTeamForActivity(t, srv, "activity-cs-payload")

	contextData := map[string]interface{}{
		"file":    "main.go",
		"line":    42,
		"snippet": "func main() {}",
	}
	insertTaskLog(t, srv, "csp-1", teamID, "worker-1", "leader", string(protocol.TypeContextShare),
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

	// 2. Leader assigns task to worker.
	insertTaskLog(t, srv, "wf-1", teamID, "leader", "worker-1", "task_assignment",
		protocol.TaskAssignmentPayload{Instruction: "deploy to staging"})

	// 3. Worker reports status.
	insertTaskLog(t, srv, "wf-2", teamID, "worker-1", "", "status_update",
		protocol.StatusUpdatePayload{Agent: "worker-1", Status: "working", CurrentTask: "deploying"})

	// 4. Worker asks a question.
	insertTaskLog(t, srv, "wf-3", teamID, "worker-1", "leader", "question",
		protocol.QuestionPayload{Question: "which cluster?"})

	// 5. Worker reports result.
	insertTaskLog(t, srv, "wf-4", teamID, "worker-1", "leader", "task_result",
		protocol.TaskResultPayload{Status: "completed", Result: "deployed to staging cluster"})

	// GetActivity should return all 5 entries (1 user_message + 4 inter-agent).
	rec := doRequest(srv, "GET", "/api/teams/"+teamID+"/activity", nil)
	var activityLogs []models.TaskLog
	parseJSON(t, rec, &activityLogs)
	if len(activityLogs) != 5 {
		t.Fatalf("activity: got %d, want 5", len(activityLogs))
	}

	// GetMessages should return only user_message + task_result = 2.
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
	if !chatTypes["task_result"] {
		t.Error("expected task_result in chat results")
	}
}
