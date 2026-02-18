package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/helmcode/agent-crew/internal/models"
	"github.com/helmcode/agent-crew/internal/runtime"
)

// mockRuntime implements runtime.AgentRuntime for testing.
type mockRuntime struct {
	deployInfraErr  error
	deployAgentErr  error
	stopAgentErr    error
	removeAgentErr  error
	teardownErr     error
	deployedAgents  []string
	teardownCalled  bool
}

func (m *mockRuntime) DeployInfra(_ context.Context, _ runtime.InfraConfig) error {
	return m.deployInfraErr
}

func (m *mockRuntime) DeployAgent(_ context.Context, cfg runtime.AgentConfig) (*runtime.AgentInstance, error) {
	if m.deployAgentErr != nil {
		return nil, m.deployAgentErr
	}
	m.deployedAgents = append(m.deployedAgents, cfg.Name)
	return &runtime.AgentInstance{
		ID:     "container-" + cfg.Name,
		Name:   cfg.Name,
		Status: "running",
	}, nil
}

func (m *mockRuntime) StopAgent(_ context.Context, _ string) error {
	return m.stopAgentErr
}

func (m *mockRuntime) RemoveAgent(_ context.Context, _ string) error {
	return m.removeAgentErr
}

func (m *mockRuntime) GetStatus(_ context.Context, id string) (*runtime.AgentStatus, error) {
	return &runtime.AgentStatus{ID: id, Name: "test", Status: "running"}, nil
}

func (m *mockRuntime) StreamLogs(_ context.Context, _ string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("log line")), nil
}

func (m *mockRuntime) TeardownInfra(_ context.Context, _ string) error {
	m.teardownCalled = true
	return m.teardownErr
}

func (m *mockRuntime) GetNATSURL(teamName string) string {
	return "nats://team-" + teamName + "-nats:4222"
}

// setupTestServer creates a Server with in-memory SQLite and mock runtime.
func setupTestServer(t *testing.T) (*Server, *mockRuntime) {
	t.Helper()
	db, err := models.InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	mock := &mockRuntime{}
	srv := NewServer(db, mock)
	return srv, mock
}

// doRequest performs an HTTP request against the Fiber app and returns the response.
func doRequest(srv *Server, method, path string, body interface{}) *httptest.ResponseRecorder {
	var bodyReader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	req.Header.Set("Content-Type", "application/json")

	resp, _ := srv.App.Test(req, -1)

	// Convert fiber response to httptest.ResponseRecorder for easier assertions.
	rec := httptest.NewRecorder()
	rec.Code = resp.StatusCode
	respBody, _ := io.ReadAll(resp.Body)
	rec.Body = bytes.NewBuffer(respBody)
	resp.Body.Close()
	return rec
}

// parseJSON unmarshals the response body into the target.
func parseJSON(t *testing.T, rec *httptest.ResponseRecorder, target interface{}) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), target); err != nil {
		t.Fatalf("failed to parse response JSON: %v\nbody: %s", err, rec.Body.String())
	}
}

// --- Team CRUD ---

func TestCreateTeam(t *testing.T) {
	srv, _ := setupTestServer(t)

	body := CreateTeamRequest{
		Name:        "test-team",
		Description: "A test team",
	}
	rec := doRequest(srv, "POST", "/api/teams", body)

	if rec.Code != 201 {
		t.Fatalf("status: got %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}

	var team models.Team
	parseJSON(t, rec, &team)

	if team.Name != "test-team" {
		t.Errorf("name: got %q, want 'test-team'", team.Name)
	}
	if team.Status != "stopped" {
		t.Errorf("status: got %q, want 'stopped'", team.Status)
	}
	if team.Runtime != "docker" {
		t.Errorf("runtime: got %q, want 'docker'", team.Runtime)
	}
	if team.ID == "" {
		t.Error("expected non-empty ID")
	}
}

func TestCreateTeam_MissingName(t *testing.T) {
	srv, _ := setupTestServer(t)

	body := CreateTeamRequest{Description: "no name"}
	rec := doRequest(srv, "POST", "/api/teams", body)

	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400", rec.Code)
	}
}

func TestCreateTeam_WithAgents(t *testing.T) {
	srv, _ := setupTestServer(t)

	body := CreateTeamRequest{
		Name: "team-with-agents",
		Agents: []CreateAgentInput{
			{Name: "leader", Role: "leader", SystemPrompt: "You are the leader"},
			{Name: "worker-1", Specialty: "devops"},
		},
	}
	rec := doRequest(srv, "POST", "/api/teams", body)

	if rec.Code != 201 {
		t.Fatalf("status: got %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}

	var team models.Team
	parseJSON(t, rec, &team)

	if len(team.Agents) != 2 {
		t.Fatalf("agents: got %d, want 2", len(team.Agents))
	}
	if team.Agents[0].Name != "leader" {
		t.Errorf("agent 0 name: got %q, want 'leader'", team.Agents[0].Name)
	}
	if team.Agents[0].Role != "leader" {
		t.Errorf("agent 0 role: got %q, want 'leader'", team.Agents[0].Role)
	}
	if team.Agents[1].Role != "worker" {
		t.Errorf("agent 1 role: got %q, want 'worker' (default)", team.Agents[1].Role)
	}
}

func TestCreateTeam_DuplicateName(t *testing.T) {
	srv, _ := setupTestServer(t)

	body := CreateTeamRequest{Name: "dup-team"}
	doRequest(srv, "POST", "/api/teams", body)

	rec := doRequest(srv, "POST", "/api/teams", body)
	if rec.Code != 409 {
		t.Fatalf("status: got %d, want 409 for duplicate name", rec.Code)
	}
}

func TestListTeams(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Create two teams.
	doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "team-a"})
	doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "team-b"})

	rec := doRequest(srv, "GET", "/api/teams", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var teams []models.Team
	parseJSON(t, rec, &teams)
	if len(teams) != 2 {
		t.Fatalf("teams: got %d, want 2", len(teams))
	}
}

func TestListTeams_Empty(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "GET", "/api/teams", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var teams []models.Team
	parseJSON(t, rec, &teams)
	if len(teams) != 0 {
		t.Fatalf("teams: got %d, want 0", len(teams))
	}
}

func TestGetTeam(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Create a team.
	createRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "get-me"})
	var created models.Team
	parseJSON(t, createRec, &created)

	rec := doRequest(srv, "GET", "/api/teams/"+created.ID, nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var team models.Team
	parseJSON(t, rec, &team)
	if team.Name != "get-me" {
		t.Errorf("name: got %q, want 'get-me'", team.Name)
	}
}

func TestGetTeam_NotFound(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "GET", "/api/teams/nonexistent-id", nil)
	if rec.Code != 404 {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
}

func TestUpdateTeam(t *testing.T) {
	srv, _ := setupTestServer(t)

	createRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "update-me"})
	var created models.Team
	parseJSON(t, createRec, &created)

	newName := "updated-name"
	newDesc := "updated description"
	rec := doRequest(srv, "PUT", "/api/teams/"+created.ID, UpdateTeamRequest{
		Name:        &newName,
		Description: &newDesc,
	})

	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	var updated models.Team
	parseJSON(t, rec, &updated)
	if updated.Name != "updated-name" {
		t.Errorf("name: got %q, want 'updated-name'", updated.Name)
	}
	if updated.Description != "updated description" {
		t.Errorf("description: got %q", updated.Description)
	}
}

func TestUpdateTeam_NotFound(t *testing.T) {
	srv, _ := setupTestServer(t)

	name := "test"
	rec := doRequest(srv, "PUT", "/api/teams/nonexistent", UpdateTeamRequest{Name: &name})
	if rec.Code != 404 {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
}

func TestDeleteTeam(t *testing.T) {
	srv, _ := setupTestServer(t)

	createRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "delete-me"})
	var created models.Team
	parseJSON(t, createRec, &created)

	rec := doRequest(srv, "DELETE", "/api/teams/"+created.ID, nil)
	if rec.Code != 204 {
		t.Fatalf("status: got %d, want 204", rec.Code)
	}

	// Verify team is deleted.
	getRec := doRequest(srv, "GET", "/api/teams/"+created.ID, nil)
	if getRec.Code != 404 {
		t.Fatalf("expected 404 after delete, got %d", getRec.Code)
	}
}

func TestDeleteTeam_NotFound(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "DELETE", "/api/teams/nonexistent", nil)
	if rec.Code != 404 {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
}

func TestDeleteTeam_CascadeDeletesAgents(t *testing.T) {
	srv, _ := setupTestServer(t)

	createRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name: "cascade-team",
		Agents: []CreateAgentInput{
			{Name: "agent-1"},
			{Name: "agent-2"},
		},
	})
	var created models.Team
	parseJSON(t, createRec, &created)

	doRequest(srv, "DELETE", "/api/teams/"+created.ID, nil)

	// Verify agents are also deleted.
	agentsRec := doRequest(srv, "GET", "/api/teams/"+created.ID+"/agents", nil)
	if agentsRec.Code != 404 {
		t.Fatalf("expected 404 for agents of deleted team, got %d", agentsRec.Code)
	}
}

// --- Agent CRUD ---

func TestCreateAgent(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "agent-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	rec := doRequest(srv, "POST", "/api/teams/"+team.ID+"/agents", CreateAgentRequest{
		Name:         "new-agent",
		Role:         "leader",
		Specialty:    "devops",
		SystemPrompt: "You are a devops agent",
	})

	if rec.Code != 201 {
		t.Fatalf("status: got %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}

	var agent models.Agent
	parseJSON(t, rec, &agent)

	if agent.Name != "new-agent" {
		t.Errorf("name: got %q, want 'new-agent'", agent.Name)
	}
	if agent.Role != "leader" {
		t.Errorf("role: got %q, want 'leader'", agent.Role)
	}
	if agent.TeamID != team.ID {
		t.Errorf("team_id: got %q, want %q", agent.TeamID, team.ID)
	}
}

func TestCreateAgent_MissingName(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "agent-team-2"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	rec := doRequest(srv, "POST", "/api/teams/"+team.ID+"/agents", CreateAgentRequest{
		Role: "worker",
	})
	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400", rec.Code)
	}
}

func TestCreateAgent_TeamNotFound(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "POST", "/api/teams/nonexistent/agents", CreateAgentRequest{
		Name: "orphan",
	})
	if rec.Code != 404 {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
}

func TestListAgents(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name: "list-agents-team",
		Agents: []CreateAgentInput{
			{Name: "a1"}, {Name: "a2"}, {Name: "a3"},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	rec := doRequest(srv, "GET", "/api/teams/"+team.ID+"/agents", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var agents []models.Agent
	parseJSON(t, rec, &agents)
	if len(agents) != 3 {
		t.Fatalf("agents: got %d, want 3", len(agents))
	}
}

func TestGetAgent(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:   "get-agent-team",
		Agents: []CreateAgentInput{{Name: "target-agent", Role: "leader"}},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	agentID := team.Agents[0].ID
	rec := doRequest(srv, "GET", "/api/teams/"+team.ID+"/agents/"+agentID, nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var agent models.Agent
	parseJSON(t, rec, &agent)
	if agent.Name != "target-agent" {
		t.Errorf("name: got %q, want 'target-agent'", agent.Name)
	}
}

func TestGetAgent_NotFound(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "agent-nf-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	rec := doRequest(srv, "GET", "/api/teams/"+team.ID+"/agents/nonexistent", nil)
	if rec.Code != 404 {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
}

func TestUpdateAgent(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:   "upd-agent-team",
		Agents: []CreateAgentInput{{Name: "upd-agent"}},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	agentID := team.Agents[0].ID
	newName := "renamed-agent"
	newRole := "leader"
	rec := doRequest(srv, "PUT", "/api/teams/"+team.ID+"/agents/"+agentID, UpdateAgentRequest{
		Name: &newName,
		Role: &newRole,
	})

	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	var agent models.Agent
	parseJSON(t, rec, &agent)
	if agent.Name != "renamed-agent" {
		t.Errorf("name: got %q, want 'renamed-agent'", agent.Name)
	}
	if agent.Role != "leader" {
		t.Errorf("role: got %q, want 'leader'", agent.Role)
	}
}

func TestDeleteAgent(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:   "del-agent-team",
		Agents: []CreateAgentInput{{Name: "del-agent"}},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	agentID := team.Agents[0].ID
	rec := doRequest(srv, "DELETE", "/api/teams/"+team.ID+"/agents/"+agentID, nil)
	if rec.Code != 204 {
		t.Fatalf("status: got %d, want 204", rec.Code)
	}

	// Verify deletion.
	getRec := doRequest(srv, "GET", "/api/teams/"+team.ID+"/agents/"+agentID, nil)
	if getRec.Code != 404 {
		t.Fatalf("expected 404 after delete, got %d", getRec.Code)
	}
}

// --- Settings ---

func TestGetSettings_Empty(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "GET", "/api/settings", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var settings []models.Settings
	parseJSON(t, rec, &settings)
	if len(settings) != 0 {
		t.Fatalf("settings: got %d, want 0", len(settings))
	}
}

func TestUpdateSettings_CreateNew(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "PUT", "/api/settings", UpdateSettingsRequest{
		Key:   "api_port",
		Value: "8080",
	})

	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	var setting models.Settings
	parseJSON(t, rec, &setting)
	if setting.Key != "api_port" {
		t.Errorf("key: got %q", setting.Key)
	}
	if setting.Value != "8080" {
		t.Errorf("value: got %q", setting.Value)
	}
}

func TestUpdateSettings_UpdateExisting(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Create.
	doRequest(srv, "PUT", "/api/settings", UpdateSettingsRequest{Key: "port", Value: "8080"})

	// Update.
	rec := doRequest(srv, "PUT", "/api/settings", UpdateSettingsRequest{Key: "port", Value: "9090"})
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var setting models.Settings
	parseJSON(t, rec, &setting)
	if setting.Value != "9090" {
		t.Errorf("value: got %q, want '9090'", setting.Value)
	}
}

func TestUpdateSettings_MissingKey(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "PUT", "/api/settings", UpdateSettingsRequest{Value: "val"})
	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400", rec.Code)
	}
}

func TestGetSettings_AfterCreation(t *testing.T) {
	srv, _ := setupTestServer(t)

	doRequest(srv, "PUT", "/api/settings", UpdateSettingsRequest{Key: "k1", Value: "v1"})
	doRequest(srv, "PUT", "/api/settings", UpdateSettingsRequest{Key: "k2", Value: "v2"})

	rec := doRequest(srv, "GET", "/api/settings", nil)
	var settings []models.Settings
	parseJSON(t, rec, &settings)

	if len(settings) != 2 {
		t.Fatalf("settings: got %d, want 2", len(settings))
	}
}

// --- Chat & Messages ---

func TestSendChat_TeamNotRunning(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "chat-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	rec := doRequest(srv, "POST", "/api/teams/"+team.ID+"/chat", ChatRequest{Message: "hello"})
	if rec.Code != 409 {
		t.Fatalf("status: got %d, want 409 (team not running)", rec.Code)
	}
}

func TestSendChat_MissingMessage(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "chat-team-2"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	// Manually set team to running.
	srv.db.Model(&team).Update("status", models.TeamStatusRunning)

	rec := doRequest(srv, "POST", "/api/teams/"+team.ID+"/chat", ChatRequest{})
	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400", rec.Code)
	}
}

func TestSendChat_TeamNotFound(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "POST", "/api/teams/nonexistent/chat", ChatRequest{Message: "hello"})
	if rec.Code != 404 {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
}

func TestGetMessages(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "msg-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	// Manually set team to running and send a chat message.
	srv.db.Model(&team).Update("status", models.TeamStatusRunning)
	doRequest(srv, "POST", "/api/teams/"+team.ID+"/chat", ChatRequest{Message: "test msg"})

	rec := doRequest(srv, "GET", "/api/teams/"+team.ID+"/messages", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var logs []models.TaskLog
	parseJSON(t, rec, &logs)
	if len(logs) != 1 {
		t.Fatalf("messages: got %d, want 1", len(logs))
	}
	if logs[0].FromAgent != "user" {
		t.Errorf("from_agent: got %q, want 'user'", logs[0].FromAgent)
	}
}

func TestGetMessages_TeamNotFound(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "GET", "/api/teams/nonexistent/messages", nil)
	if rec.Code != 404 {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
}

// --- Deploy / Stop ---

func TestDeployTeam(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:   "deploy-team",
		Agents: []CreateAgentInput{{Name: "agent-1"}},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	rec := doRequest(srv, "POST", "/api/teams/"+team.ID+"/deploy", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	var deployed models.Team
	parseJSON(t, rec, &deployed)
	if deployed.Status != models.TeamStatusDeploying {
		t.Errorf("status: got %q, want 'deploying'", deployed.Status)
	}
}

func TestDeployTeam_NotFound(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "POST", "/api/teams/nonexistent/deploy", nil)
	if rec.Code != 404 {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
}

func TestDeployTeam_AlreadyRunning(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "running-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.db.Model(&team).Update("status", models.TeamStatusRunning)

	rec := doRequest(srv, "POST", "/api/teams/"+team.ID+"/deploy", nil)
	if rec.Code != 409 {
		t.Fatalf("status: got %d, want 409", rec.Code)
	}
}

func TestStopTeam(t *testing.T) {
	srv, mock := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:   "stop-team",
		Agents: []CreateAgentInput{{Name: "agent-1"}},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	// Set team to running.
	srv.db.Model(&team).Update("status", models.TeamStatusRunning)

	rec := doRequest(srv, "POST", "/api/teams/"+team.ID+"/stop", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	var stopped models.Team
	parseJSON(t, rec, &stopped)
	if stopped.Status != models.TeamStatusStopped {
		t.Errorf("status: got %q, want 'stopped'", stopped.Status)
	}

	if !mock.teardownCalled {
		t.Error("expected TeardownInfra to be called")
	}
}

func TestStopTeam_NotRunning(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "stop-stopped"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	rec := doRequest(srv, "POST", "/api/teams/"+team.ID+"/stop", nil)
	if rec.Code != 409 {
		t.Fatalf("status: got %d, want 409 (team not running)", rec.Code)
	}
}

func TestDeleteTeam_WhileRunning(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "del-running"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.db.Model(&team).Update("status", models.TeamStatusRunning)

	rec := doRequest(srv, "DELETE", "/api/teams/"+team.ID, nil)
	if rec.Code != 409 {
		t.Fatalf("status: got %d, want 409 (must stop before delete)", rec.Code)
	}
}
