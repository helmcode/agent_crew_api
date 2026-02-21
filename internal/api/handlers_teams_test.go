package api

import (
	"testing"

	"github.com/helmcode/agent-crew/internal/models"
)

func TestDeployTeam_SetsStatusDeploying(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:   "deploying-status-team",
		Agents: []CreateAgentInput{{Name: "a1", Role: "leader"}},
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
		t.Errorf("status: got %q, want %q", deployed.Status, models.TeamStatusDeploying)
	}

	// Verify agents are included in response.
	if len(deployed.Agents) != 1 {
		t.Errorf("agents count: got %d, want 1", len(deployed.Agents))
	}
}

func TestStopTeam_WorksFromErrorStatus(t *testing.T) {
	srv, mock := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:   "error-stop-team",
		Agents: []CreateAgentInput{{Name: "a1"}},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	// Set team to error status (should still be stoppable).
	srv.db.Model(&team).Update("status", models.TeamStatusError)

	rec := doRequest(srv, "POST", "/api/teams/"+team.ID+"/stop", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	var stopped models.Team
	parseJSON(t, rec, &stopped)
	if stopped.Status != models.TeamStatusStopped {
		t.Errorf("status: got %q, want %q", stopped.Status, models.TeamStatusStopped)
	}

	if !mock.teardownCalled {
		t.Error("expected TeardownInfra to be called")
	}
}

func TestStopTeam_ClearsLeaderContainerOnly(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name: "clear-container-team",
		Agents: []CreateAgentInput{
			{Name: "a1", Role: "leader"},
			{Name: "a2", Role: "worker"},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	// Simulate running state — only the leader has a container in the new model.
	srv.db.Model(&team).Update("status", models.TeamStatusRunning)
	for i := range team.Agents {
		if team.Agents[i].Role == models.AgentRoleLeader {
			srv.db.Model(&team.Agents[i]).Updates(map[string]interface{}{
				"container_id":     "container-" + team.Agents[i].Name,
				"container_status": models.ContainerStatusRunning,
			})
		}
	}

	// Stop the team.
	doRequest(srv, "POST", "/api/teams/"+team.ID+"/stop", nil)

	// Verify leader container state was cleared.
	var agents []models.Agent
	srv.db.Where("team_id = ?", team.ID).Find(&agents)

	for _, a := range agents {
		if a.Role == models.AgentRoleLeader {
			if a.ContainerID != "" {
				t.Errorf("leader container_id should be empty, got %q", a.ContainerID)
			}
			if a.ContainerStatus != models.ContainerStatusStopped {
				t.Errorf("leader container_status: got %q, want %q", a.ContainerStatus, models.ContainerStatusStopped)
			}
		} else {
			// Workers should never have had container state in the single-container model.
			if a.ContainerID != "" {
				t.Errorf("worker %s should have no container_id, got %q", a.Name, a.ContainerID)
			}
		}
	}
}

func TestDeployTeamAsync_OnlyDeploysLeader(t *testing.T) {
	srv, mock := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name: "single-container-team",
		Agents: []CreateAgentInput{
			{Name: "the-leader", Role: "leader", SystemPrompt: "You lead"},
			{Name: "sub-agent-1", Role: "worker", Specialty: "frontend"},
			{Name: "sub-agent-2", Role: "worker", Specialty: "backend"},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	// Call deployTeamAsync synchronously.
	srv.deployTeamAsync(team)

	// Only the leader should have been deployed as a container.
	if len(mock.deployedAgents) != 1 {
		t.Fatalf("deployed agents: got %d, want 1", len(mock.deployedAgents))
	}
	if mock.deployedAgents[0] != "the-leader" {
		t.Errorf("deployed agent name: got %q, want 'the-leader'", mock.deployedAgents[0])
	}

	// Verify team status is running.
	var updated models.Team
	srv.db.First(&updated, "id = ?", team.ID)
	if updated.Status != models.TeamStatusRunning {
		t.Errorf("team status: got %q, want %q", updated.Status, models.TeamStatusRunning)
	}

	// Verify leader has container state.
	var leader models.Agent
	srv.db.Where("team_id = ? AND role = ?", team.ID, models.AgentRoleLeader).First(&leader)
	if leader.ContainerID != "container-the-leader" {
		t.Errorf("leader container_id: got %q, want 'container-the-leader'", leader.ContainerID)
	}
	if leader.ContainerStatus != models.ContainerStatusRunning {
		t.Errorf("leader container_status: got %q, want %q", leader.ContainerStatus, models.ContainerStatusRunning)
	}

	// Verify workers have no container state.
	var workers []models.Agent
	srv.db.Where("team_id = ? AND role = ?", team.ID, models.AgentRoleWorker).Find(&workers)
	for _, w := range workers {
		if w.ContainerID != "" {
			t.Errorf("worker %s should have no container_id, got %q", w.Name, w.ContainerID)
		}
	}
}

func TestDeployTeamAsync_NoLeader_SetsError(t *testing.T) {
	srv, mock := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name: "no-leader-team",
		Agents: []CreateAgentInput{
			{Name: "worker-1", Role: "worker"},
			{Name: "worker-2", Role: "worker"},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	// Call deployTeamAsync synchronously.
	srv.deployTeamAsync(team)

	// No containers should have been deployed.
	if len(mock.deployedAgents) != 0 {
		t.Fatalf("deployed agents: got %d, want 0", len(mock.deployedAgents))
	}

	// Verify team status is error (no leader found).
	var updated models.Team
	srv.db.First(&updated, "id = ?", team.ID)
	if updated.Status != models.TeamStatusError {
		t.Errorf("team status: got %q, want %q", updated.Status, models.TeamStatusError)
	}
}

func TestLoadSettingsEnv_PrimaryKeys(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Set API key in settings.
	srv.db.Create(&models.Settings{Key: "ANTHROPIC_API_KEY", Value: "sk-test-123"})

	env := srv.loadSettingsEnv()

	if env["ANTHROPIC_API_KEY"] != "sk-test-123" {
		t.Errorf("ANTHROPIC_API_KEY: got %q, want 'sk-test-123'", env["ANTHROPIC_API_KEY"])
	}
}

func TestLoadSettingsEnv_OAuthToken(t *testing.T) {
	srv, _ := setupTestServer(t)

	srv.db.Create(&models.Settings{Key: "CLAUDE_CODE_OAUTH_TOKEN", Value: "oauth-abc"})

	env := srv.loadSettingsEnv()

	if env["CLAUDE_CODE_OAUTH_TOKEN"] != "oauth-abc" {
		t.Errorf("CLAUDE_CODE_OAUTH_TOKEN: got %q, want 'oauth-abc'", env["CLAUDE_CODE_OAUTH_TOKEN"])
	}
}

func TestLoadSettingsEnv_AliasMapping(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Set the alias key (ANTHROPIC_AUTH_TOKEN maps to CLAUDE_CODE_OAUTH_TOKEN).
	srv.db.Create(&models.Settings{Key: "ANTHROPIC_AUTH_TOKEN", Value: "alias-token"})

	env := srv.loadSettingsEnv()

	// Should be mapped to the target key.
	if env["CLAUDE_CODE_OAUTH_TOKEN"] != "alias-token" {
		t.Errorf("CLAUDE_CODE_OAUTH_TOKEN via alias: got %q, want 'alias-token'", env["CLAUDE_CODE_OAUTH_TOKEN"])
	}
}

func TestLoadSettingsEnv_PrimaryOverridesAlias(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Set both primary and alias keys.
	srv.db.Create(&models.Settings{Key: "CLAUDE_CODE_OAUTH_TOKEN", Value: "primary-token"})
	srv.db.Create(&models.Settings{Key: "ANTHROPIC_AUTH_TOKEN", Value: "alias-token"})

	env := srv.loadSettingsEnv()

	// Primary key should take precedence.
	if env["CLAUDE_CODE_OAUTH_TOKEN"] != "primary-token" {
		t.Errorf("primary should override alias: got %q, want 'primary-token'", env["CLAUDE_CODE_OAUTH_TOKEN"])
	}
}

func TestLoadSettingsEnv_Empty(t *testing.T) {
	srv, _ := setupTestServer(t)

	env := srv.loadSettingsEnv()

	if len(env) != 0 {
		t.Errorf("expected empty map, got %v", env)
	}
}

func TestCreateTeam_WithWorkspacePath(t *testing.T) {
	srv, _ := setupTestServer(t)

	body := CreateTeamRequest{
		Name:          "workspace-team",
		WorkspacePath: "/tmp/test-workspace",
	}
	rec := doRequest(srv, "POST", "/api/teams", body)

	if rec.Code != 201 {
		t.Fatalf("status: got %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}

	var team models.Team
	parseJSON(t, rec, &team)

	if team.WorkspacePath != "/tmp/test-workspace" {
		t.Errorf("workspace_path: got %q, want '/tmp/test-workspace'", team.WorkspacePath)
	}
}

func TestUpdateTeam_WorkspacePath(t *testing.T) {
	srv, _ := setupTestServer(t)

	createRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "upd-ws-team"})
	var team models.Team
	parseJSON(t, createRec, &team)

	wsPath := "/new/workspace"
	rec := doRequest(srv, "PUT", "/api/teams/"+team.ID, UpdateTeamRequest{
		WorkspacePath: &wsPath,
	})

	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var updated models.Team
	parseJSON(t, rec, &updated)
	if updated.WorkspacePath != "/new/workspace" {
		t.Errorf("workspace_path: got %q, want '/new/workspace'", updated.WorkspacePath)
	}
}

func TestCreateTeam_DefaultRuntime(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "default-rt"})
	var team models.Team
	parseJSON(t, rec, &team)

	if team.Runtime != "docker" {
		t.Errorf("default runtime: got %q, want 'docker'", team.Runtime)
	}
}

func TestCreateTeam_AgentWithClaudeMD(t *testing.T) {
	srv, _ := setupTestServer(t)

	claudeContent := "# My Custom Agent\n\nCustom instructions here.\n"
	rec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name: "claude-md-team",
		Agents: []CreateAgentInput{
			{Name: "a1", Role: "worker", ClaudeMD: claudeContent},
		},
	})

	if rec.Code != 201 {
		t.Fatalf("status: got %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}

	var team models.Team
	parseJSON(t, rec, &team)

	if len(team.Agents) != 1 {
		t.Fatalf("agents: got %d, want 1", len(team.Agents))
	}
	if team.Agents[0].ClaudeMD != claudeContent {
		t.Errorf("claude_md: got %q, want %q", team.Agents[0].ClaudeMD, claudeContent)
	}
}

func TestUpdateAgent_ClaudeMD(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:   "upd-claude-md-team",
		Agents: []CreateAgentInput{{Name: "a1"}},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	newMD := "# Updated Config\n"
	rec := doRequest(srv, "PUT", "/api/teams/"+team.ID+"/agents/"+team.Agents[0].ID, UpdateAgentRequest{
		ClaudeMD: &newMD,
	})

	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var agent models.Agent
	parseJSON(t, rec, &agent)
	if agent.ClaudeMD != newMD {
		t.Errorf("claude_md: got %q, want %q", agent.ClaudeMD, newMD)
	}
}

func TestCreateTeam_KubernetesRuntime(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:    "k8s-team",
		Runtime: "kubernetes",
	})
	var team models.Team
	parseJSON(t, rec, &team)

	if team.Runtime != "kubernetes" {
		t.Errorf("runtime: got %q, want 'kubernetes'", team.Runtime)
	}
}
