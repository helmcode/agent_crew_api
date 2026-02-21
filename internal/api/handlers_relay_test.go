package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/helmcode/agent-crew/internal/models"
	"github.com/helmcode/agent-crew/internal/protocol"
)

// buildRelayPayload marshals a protocol.Message into raw bytes suitable for
// processRelayMessage, mirroring what a NATS subscriber would receive.
func buildRelayPayload(t *testing.T, msgType protocol.MessageType, from, to string, payload interface{}) []byte {
	t.Helper()
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal payload: %v", err)
	}
	msg := protocol.Message{
		MessageID: "msg-" + string(msgType),
		From:      from,
		To:        to,
		Type:      msgType,
		Payload:   json.RawMessage(rawPayload),
		Timestamp: time.Now().UTC(),
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("failed to marshal protocol message: %v", err)
	}
	return data
}

// countRelayLogs returns the number of TaskLog rows saved for the given team.
func countRelayLogs(t *testing.T, srv *Server, teamID string) int64 {
	t.Helper()
	var count int64
	srv.db.Model(&models.TaskLog{}).Where("team_id = ?", teamID).Count(&count)
	return count
}

// --- processRelayMessage tests ---

func TestProcessRelayMessage_LeaderResponse(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "relay-resp-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	data := buildRelayPayload(t, protocol.TypeLeaderResponse, "leader", "user",
		protocol.LeaderResponsePayload{Status: "completed", Result: "task done successfully"})

	if err := srv.processRelayMessage(team.ID, team.Name, data); err != nil {
		t.Fatalf("processRelayMessage returned error: %v", err)
	}

	count := countRelayLogs(t, srv, team.ID)
	if count != 1 {
		t.Fatalf("task logs: got %d, want 1", count)
	}

	var log models.TaskLog
	srv.db.Where("team_id = ?", team.ID).First(&log)

	if log.FromAgent != "leader" {
		t.Errorf("from_agent: got %q, want 'leader'", log.FromAgent)
	}
	if log.ToAgent != "user" {
		t.Errorf("to_agent: got %q, want 'user'", log.ToAgent)
	}
	if log.MessageType != "task_result" {
		t.Errorf("message_type: got %q, want 'task_result'", log.MessageType)
	}
	if log.TeamID != team.ID {
		t.Errorf("team_id: got %q, want %q", log.TeamID, team.ID)
	}
	if log.ID == "" {
		t.Error("expected non-empty ID")
	}
}

func TestProcessRelayMessage_SkipsUserMessage(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "relay-skip-user"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	// user_message should be skipped — the chat handler already saves it.
	data := buildRelayPayload(t, protocol.TypeUserMessage, "user", "leader",
		protocol.UserMessagePayload{Content: "hello agents"})

	if err := srv.processRelayMessage(team.ID, team.Name, data); err != nil {
		t.Fatalf("processRelayMessage returned error: %v", err)
	}

	count := countRelayLogs(t, srv, team.ID)
	if count != 0 {
		t.Fatalf("task logs: got %d, want 0 (user_message must be skipped)", count)
	}
}

func TestProcessRelayMessage_SkipsSystemCommand(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "relay-skip-sys"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	// system_command should be skipped — internal control messages.
	data := buildRelayPayload(t, protocol.TypeSystemCommand, "system", "leader",
		protocol.SystemCommandPayload{Command: "shutdown"})

	if err := srv.processRelayMessage(team.ID, team.Name, data); err != nil {
		t.Fatalf("processRelayMessage returned error: %v", err)
	}

	count := countRelayLogs(t, srv, team.ID)
	if count != 0 {
		t.Fatalf("task logs: got %d, want 0 (system_command must be skipped)", count)
	}
}

func TestProcessRelayMessage_InvalidJSON(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "relay-json-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	err := srv.processRelayMessage(team.ID, team.Name, []byte("not valid json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestProcessRelayMessage_PreservesMessageID(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "relay-msgid-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	rawPayload, _ := json.Marshal(protocol.LeaderResponsePayload{Status: "completed", Result: "done"})
	msg := protocol.Message{
		MessageID: "unique-message-id-42",
		From:      "leader",
		To:        "user",
		Type:      protocol.TypeLeaderResponse,
		Payload:   json.RawMessage(rawPayload),
		Timestamp: time.Now().UTC(),
	}
	data, _ := json.Marshal(msg)

	if err := srv.processRelayMessage(team.ID, team.Name, data); err != nil {
		t.Fatalf("processRelayMessage returned error: %v", err)
	}

	var log models.TaskLog
	srv.db.Where("team_id = ?", team.ID).First(&log)
	if log.MessageID != "unique-message-id-42" {
		t.Errorf("message_id: got %q, want 'unique-message-id-42'", log.MessageID)
	}
}

func TestProcessRelayMessage_TeamIsolation(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Create two teams.
	rec1 := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "relay-iso-team-a"})
	rec2 := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "relay-iso-team-b"})
	var teamA, teamB models.Team
	parseJSON(t, rec1, &teamA)
	parseJSON(t, rec2, &teamB)

	// Send a message to team A only.
	data := buildRelayPayload(t, protocol.TypeLeaderResponse, "leader", "user",
		protocol.LeaderResponsePayload{Status: "completed", Result: "done"})

	if err := srv.processRelayMessage(teamA.ID, teamA.Name, data); err != nil {
		t.Fatalf("processRelayMessage returned error: %v", err)
	}

	// Team A should have 1 log, team B should have 0.
	countA := countRelayLogs(t, srv, teamA.ID)
	countB := countRelayLogs(t, srv, teamB.ID)

	if countA != 1 {
		t.Errorf("team A logs: got %d, want 1", countA)
	}
	if countB != 0 {
		t.Errorf("team B logs: got %d, want 0 (team isolation broken)", countB)
	}
}

func TestProcessRelayMessage_ContainerValidation(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "relay-val-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	data := buildRelayPayload(t, protocol.TypeContainerValidation, "leader", "system",
		protocol.ContainerValidationPayload{
			AgentName: "leader",
			Checks: []protocol.ValidationCheck{
				{Name: "claude_md", Status: protocol.ValidationOK, Message: "CLAUDE.md exists"},
				{Name: "skills_symlink", Status: protocol.ValidationWarning, Message: "symlink broken"},
			},
			Summary: "1 ok, 1 warning(s), 0 error(s)",
		})

	if err := srv.processRelayMessage(team.ID, team.Name, data); err != nil {
		t.Fatalf("processRelayMessage returned error: %v", err)
	}

	count := countRelayLogs(t, srv, team.ID)
	if count != 1 {
		t.Fatalf("task logs: got %d, want 1", count)
	}

	var log models.TaskLog
	srv.db.Where("team_id = ?", team.ID).First(&log)

	if log.MessageType != "container_validation" {
		t.Errorf("message_type: got %q, want 'container_validation'", log.MessageType)
	}
	if log.FromAgent != "leader" {
		t.Errorf("from_agent: got %q, want 'leader'", log.FromAgent)
	}

	// Verify payload is preserved.
	var payload protocol.ContainerValidationPayload
	if err := json.Unmarshal(log.Payload, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if len(payload.Checks) != 2 {
		t.Errorf("checks: got %d, want 2", len(payload.Checks))
	}
	if payload.Summary != "1 ok, 1 warning(s), 0 error(s)" {
		t.Errorf("summary: got %q", payload.Summary)
	}
}

func TestProcessRelayMessage_MultipleMessages(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "relay-multi-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	messages := []struct {
		msgType    protocol.MessageType
		from       string
		to         string
		payload    interface{}
		shouldSave bool
	}{
		{protocol.TypeLeaderResponse, "leader", "user", protocol.LeaderResponsePayload{Status: "completed", Result: "done"}, true},
		{protocol.TypeContainerValidation, "leader", "system", protocol.ContainerValidationPayload{AgentName: "leader", Summary: "ok"}, true},
		// These must be skipped.
		{protocol.TypeUserMessage, "user", "leader", protocol.UserMessagePayload{Content: "ignored"}, false},
		{protocol.TypeSystemCommand, "system", "leader", protocol.SystemCommandPayload{Command: "shutdown"}, false},
	}

	for _, m := range messages {
		data := buildRelayPayload(t, m.msgType, m.from, m.to, m.payload)
		if err := srv.processRelayMessage(team.ID, team.Name, data); err != nil {
			t.Fatalf("processRelayMessage(%s) returned error: %v", m.msgType, err)
		}
	}

	// leader_response + container_validation should be saved (2 out of 4).
	count := countRelayLogs(t, srv, team.ID)
	if count != 2 {
		t.Fatalf("task logs: got %d, want 2 (leader_response + container_validation saved)", count)
	}
}

func TestProcessRelayMessage_ActivityEvent(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "relay-activity-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	data := buildRelayPayload(t, protocol.TypeActivityEvent, "leader", "system",
		protocol.ActivityEventPayload{
			EventType: "tool_use",
			AgentName: "leader",
			ToolName:  "Bash",
			Action:    "Bash: ls -la /workspace",
			Payload:   json.RawMessage(`{"type":"tool_use","name":"Bash"}`),
		})

	if err := srv.processRelayMessage(team.ID, team.Name, data); err != nil {
		t.Fatalf("processRelayMessage returned error: %v", err)
	}

	count := countRelayLogs(t, srv, team.ID)
	if count != 1 {
		t.Fatalf("task logs: got %d, want 1", count)
	}

	var log models.TaskLog
	srv.db.Where("team_id = ?", team.ID).First(&log)

	if log.MessageType != "activity_event" {
		t.Errorf("message_type: got %q, want 'activity_event'", log.MessageType)
	}
	if log.FromAgent != "leader" {
		t.Errorf("from_agent: got %q, want 'leader'", log.FromAgent)
	}
	if log.ToAgent != "system" {
		t.Errorf("to_agent: got %q, want 'system'", log.ToAgent)
	}

	// Verify payload is preserved and can be deserialized.
	var payload protocol.ActivityEventPayload
	if err := json.Unmarshal(log.Payload, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if payload.EventType != "tool_use" {
		t.Errorf("event_type: got %q, want 'tool_use'", payload.EventType)
	}
	if payload.AgentName != "leader" {
		t.Errorf("agent_name: got %q, want 'leader'", payload.AgentName)
	}
	if payload.ToolName != "Bash" {
		t.Errorf("tool_name: got %q, want 'Bash'", payload.ToolName)
	}
	if payload.Action != "Bash: ls -la /workspace" {
		t.Errorf("action: got %q, want 'Bash: ls -la /workspace'", payload.Action)
	}
}

func TestProcessRelayMessage_PayloadPreserved(t *testing.T) {
	srv, _ := setupTestServer(t)
	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "relay-payload-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	data := buildRelayPayload(t, protocol.TypeLeaderResponse, "leader", "user",
		protocol.LeaderResponsePayload{Status: "completed", Result: "the final answer", Error: ""})

	if err := srv.processRelayMessage(team.ID, team.Name, data); err != nil {
		t.Fatalf("processRelayMessage returned error: %v", err)
	}

	var log models.TaskLog
	srv.db.Where("team_id = ?", team.ID).First(&log)

	// Verify the payload was stored and can be unmarshaled back.
	var respPayload protocol.LeaderResponsePayload
	if err := json.Unmarshal(log.Payload, &respPayload); err != nil {
		t.Fatalf("failed to unmarshal stored payload: %v", err)
	}
	if respPayload.Status != "completed" {
		t.Errorf("payload status: got %q, want 'completed'", respPayload.Status)
	}
	if respPayload.Result != "the final answer" {
		t.Errorf("payload result: got %q, want 'the final answer'", respPayload.Result)
	}
}
