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

func TestStopTeam_ClearsAgentContainerIDs(t *testing.T) {
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

	// Simulate running state with container IDs.
	srv.db.Model(&team).Update("status", models.TeamStatusRunning)
	for i := range team.Agents {
		srv.db.Model(&team.Agents[i]).Updates(map[string]interface{}{
			"container_id":     "container-" + team.Agents[i].Name,
			"container_status": models.ContainerStatusRunning,
		})
	}

	// Stop the team.
	doRequest(srv, "POST", "/api/teams/"+team.ID+"/stop", nil)

	// Verify agent container IDs and statuses were cleared.
	var agents []models.Agent
	srv.db.Where("team_id = ?", team.ID).Find(&agents)

	for _, a := range agents {
		if a.ContainerID != "" {
			t.Errorf("agent %s container_id should be empty, got %q", a.Name, a.ContainerID)
		}
		if a.ContainerStatus != models.ContainerStatusStopped {
			t.Errorf("agent %s container_status: got %q, want %q", a.Name, a.ContainerStatus, models.ContainerStatusStopped)
		}
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
