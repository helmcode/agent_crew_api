package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

	env := srv.LoadSettingsEnv()

	if env["ANTHROPIC_API_KEY"] != "sk-test-123" {
		t.Errorf("ANTHROPIC_API_KEY: got %q, want 'sk-test-123'", env["ANTHROPIC_API_KEY"])
	}
}

func TestLoadSettingsEnv_OAuthToken(t *testing.T) {
	srv, _ := setupTestServer(t)

	srv.db.Create(&models.Settings{Key: "CLAUDE_CODE_OAUTH_TOKEN", Value: "oauth-abc"})

	env := srv.LoadSettingsEnv()

	if env["CLAUDE_CODE_OAUTH_TOKEN"] != "oauth-abc" {
		t.Errorf("CLAUDE_CODE_OAUTH_TOKEN: got %q, want 'oauth-abc'", env["CLAUDE_CODE_OAUTH_TOKEN"])
	}
}

func TestLoadSettingsEnv_AliasMapping(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Set the alias key (ANTHROPIC_AUTH_TOKEN maps to CLAUDE_CODE_OAUTH_TOKEN).
	srv.db.Create(&models.Settings{Key: "ANTHROPIC_AUTH_TOKEN", Value: "alias-token"})

	env := srv.LoadSettingsEnv()

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

	env := srv.LoadSettingsEnv()

	// Primary key should take precedence.
	if env["CLAUDE_CODE_OAUTH_TOKEN"] != "primary-token" {
		t.Errorf("primary should override alias: got %q, want 'primary-token'", env["CLAUDE_CODE_OAUTH_TOKEN"])
	}
}

func TestLoadSettingsEnv_Empty(t *testing.T) {
	srv, _ := setupTestServer(t)

	env := srv.LoadSettingsEnv()

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

func TestCreateTeam_AgentWithInstructionsMD(t *testing.T) {
	srv, _ := setupTestServer(t)

	content := "# My Custom Agent\n\nCustom instructions here.\n"
	rec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name: "instructions-md-team",
		Agents: []CreateAgentInput{
			{Name: "a1", Role: "worker", InstructionsMD: content},
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
	if team.Agents[0].InstructionsMD != content {
		t.Errorf("instructions_md: got %q, want %q", team.Agents[0].InstructionsMD, content)
	}
}

func TestCreateTeam_AgentWithClaudeMDBackwardCompat(t *testing.T) {
	srv, _ := setupTestServer(t)

	content := "# Backward Compat\n"
	rec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name: "claude-md-compat-team",
		Agents: []CreateAgentInput{
			{Name: "a1", Role: "worker", ClaudeMD: content},
		},
	})

	if rec.Code != 201 {
		t.Fatalf("status: got %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}

	var team models.Team
	parseJSON(t, rec, &team)

	if team.Agents[0].InstructionsMD != content {
		t.Errorf("instructions_md via claude_md compat: got %q, want %q", team.Agents[0].InstructionsMD, content)
	}
}

func TestUpdateAgent_InstructionsMD(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:   "upd-instructions-md-team",
		Agents: []CreateAgentInput{{Name: "a1"}},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	newMD := "# Updated Config\n"
	rec := doRequest(srv, "PUT", "/api/teams/"+team.ID+"/agents/"+team.Agents[0].ID, UpdateAgentRequest{
		InstructionsMD: &newMD,
	})

	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var agent models.Agent
	parseJSON(t, rec, &agent)
	if agent.InstructionsMD != newMD {
		t.Errorf("instructions_md: got %q, want %q", agent.InstructionsMD, newMD)
	}
}

func TestUpdateAgent_ClaudeMDBackwardCompat(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:   "upd-claude-md-compat-team",
		Agents: []CreateAgentInput{{Name: "a1"}},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	newMD := "# Updated via claude_md\n"
	rec := doRequest(srv, "PUT", "/api/teams/"+team.ID+"/agents/"+team.Agents[0].ID, UpdateAgentRequest{
		ClaudeMD: &newMD,
	})

	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var agent models.Agent
	parseJSON(t, rec, &agent)
	if agent.InstructionsMD != newMD {
		t.Errorf("instructions_md via claude_md compat: got %q, want %q", agent.InstructionsMD, newMD)
	}
}

func TestDeployTeamAsync_LegacySkillsWithFullURL(t *testing.T) {
	srv, mock := setupTestServer(t)

	// Create team with worker agent using legacy string-format skills that include full URLs.
	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name: "legacy-skills-team",
		Agents: []CreateAgentInput{
			{Name: "the-leader", Role: "leader"},
			{
				Name: "worker-1", Role: "worker",
				SubAgentSkills: []string{
					"https://github.com/jezweb/claude-skills:fastapi",
					"vercel-labs/agent-skills:vercel-react-best-practices",
				},
			},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.deployTeamAsync(team)

	// Verify skills were correctly parsed and passed to the agent config.
	if mock.lastAgentConfig == nil {
		t.Fatal("expected lastAgentConfig to be set")
	}
	skillsJSON := mock.lastAgentConfig.Env["AGENT_SKILLS_INSTALL"]
	if skillsJSON == "" {
		t.Fatal("expected AGENT_SKILLS_INSTALL to be set in agent env")
	}

	var skills []struct {
		RepoURL   string `json:"repo_url"`
		SkillName string `json:"skill_name"`
	}
	if err := json.Unmarshal([]byte(skillsJSON), &skills); err != nil {
		t.Fatalf("failed to parse skills JSON: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d: %s", len(skills), skillsJSON)
	}

	// First skill: full URL should be preserved as-is.
	if skills[0].RepoURL != "https://github.com/jezweb/claude-skills" {
		t.Errorf("skill[0] repo_url: got %q, want 'https://github.com/jezweb/claude-skills'", skills[0].RepoURL)
	}
	if skills[0].SkillName != "fastapi" {
		t.Errorf("skill[0] skill_name: got %q, want 'fastapi'", skills[0].SkillName)
	}

	// Second skill: short format should get https://github.com/ prefix.
	if skills[1].RepoURL != "https://github.com/vercel-labs/agent-skills" {
		t.Errorf("skill[1] repo_url: got %q, want 'https://github.com/vercel-labs/agent-skills'", skills[1].RepoURL)
	}
	if skills[1].SkillName != "vercel-react-best-practices" {
		t.Errorf("skill[1] skill_name: got %q, want 'vercel-react-best-practices'", skills[1].SkillName)
	}
}

func TestDeployTeamAsync_SkillConfigFormat(t *testing.T) {
	srv, mock := setupTestServer(t)

	// Create team with worker agent using SkillConfig object format.
	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name: "skillconfig-team",
		Agents: []CreateAgentInput{
			{Name: "the-leader", Role: "leader"},
			{
				Name: "worker-1", Role: "worker",
				SubAgentSkills: []map[string]string{
					{"repo_url": "https://github.com/jezweb/claude-skills", "skill_name": "fastapi"},
					{"repo_url": "https://github.com/vercel-labs/agent-skills", "skill_name": "vercel-react-best-practices"},
				},
			},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.deployTeamAsync(team)

	if mock.lastAgentConfig == nil {
		t.Fatal("expected lastAgentConfig to be set")
	}
	skillsJSON := mock.lastAgentConfig.Env["AGENT_SKILLS_INSTALL"]
	if skillsJSON == "" {
		t.Fatal("expected AGENT_SKILLS_INSTALL to be set")
	}

	var skills []struct {
		RepoURL   string `json:"repo_url"`
		SkillName string `json:"skill_name"`
	}
	if err := json.Unmarshal([]byte(skillsJSON), &skills); err != nil {
		t.Fatalf("failed to parse skills JSON: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(skills))
	}
	if skills[0].RepoURL != "https://github.com/jezweb/claude-skills" {
		t.Errorf("skill[0] repo_url: got %q", skills[0].RepoURL)
	}
	if skills[0].SkillName != "fastapi" {
		t.Errorf("skill[0] skill_name: got %q", skills[0].SkillName)
	}
}

func TestDeployTeamAsync_DeduplicatesSkills(t *testing.T) {
	srv, mock := setupTestServer(t)

	// Two workers with the same skill should be deduplicated.
	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name: "dedup-skills-team",
		Agents: []CreateAgentInput{
			{Name: "the-leader", Role: "leader"},
			{
				Name: "worker-1", Role: "worker",
				SubAgentSkills: []map[string]string{
					{"repo_url": "https://github.com/jezweb/claude-skills", "skill_name": "fastapi"},
				},
			},
			{
				Name: "worker-2", Role: "worker",
				SubAgentSkills: []map[string]string{
					{"repo_url": "https://github.com/jezweb/claude-skills", "skill_name": "fastapi"},
				},
			},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.deployTeamAsync(team)

	if mock.lastAgentConfig == nil {
		t.Fatal("expected lastAgentConfig to be set")
	}
	skillsJSON := mock.lastAgentConfig.Env["AGENT_SKILLS_INSTALL"]

	var skills []struct {
		RepoURL   string `json:"repo_url"`
		SkillName string `json:"skill_name"`
	}
	if err := json.Unmarshal([]byte(skillsJSON), &skills); err != nil {
		t.Fatalf("failed to parse skills JSON: %v", err)
	}
	if len(skills) != 1 {
		t.Errorf("expected 1 deduplicated skill, got %d: %s", len(skills), skillsJSON)
	}
}

func TestCreateTeam_DuplicateAgentNames(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name: "dup-agent-team",
		Agents: []CreateAgentInput{
			{Name: "agent-one", Role: "leader"},
			{Name: "Agent-One", Role: "worker"},
		},
	})

	if rec.Code != 409 {
		t.Fatalf("status: got %d, want 409 for duplicate agent names\nbody: %s", rec.Code, rec.Body.String())
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

func TestCreateTeam_DefaultProvider(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "default-prov"})
	if rec.Code != 201 {
		t.Fatalf("status: got %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}

	var team models.Team
	parseJSON(t, rec, &team)

	if team.Provider != "claude" {
		t.Errorf("default provider: got %q, want 'claude'", team.Provider)
	}
}

func TestCreateTeam_ProviderClaude(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:     "claude-prov",
		Provider: "claude",
	})
	if rec.Code != 201 {
		t.Fatalf("status: got %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}

	var team models.Team
	parseJSON(t, rec, &team)

	if team.Provider != "claude" {
		t.Errorf("provider: got %q, want 'claude'", team.Provider)
	}
}

func TestCreateTeam_ProviderOpenCode(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:     "opencode-prov",
		Provider: "opencode",
	})
	if rec.Code != 201 {
		t.Fatalf("status: got %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}

	var team models.Team
	parseJSON(t, rec, &team)

	if team.Provider != "opencode" {
		t.Errorf("provider: got %q, want 'opencode'", team.Provider)
	}
}

func TestCreateTeam_InvalidProvider(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:     "bad-prov",
		Provider: "gemini",
	})

	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400 for invalid provider\nbody: %s", rec.Code, rec.Body.String())
	}
}

func TestUpdateTeam_Provider(t *testing.T) {
	srv, _ := setupTestServer(t)

	createRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "upd-prov-team"})
	var team models.Team
	parseJSON(t, createRec, &team)

	prov := "opencode"
	rec := doRequest(srv, "PUT", "/api/teams/"+team.ID, UpdateTeamRequest{
		Provider: &prov,
	})

	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	var updated models.Team
	parseJSON(t, rec, &updated)
	if updated.Provider != "opencode" {
		t.Errorf("provider: got %q, want 'opencode'", updated.Provider)
	}
}

func TestUpdateTeam_InvalidProvider(t *testing.T) {
	srv, _ := setupTestServer(t)

	createRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "upd-bad-prov"})
	var team models.Team
	parseJSON(t, createRec, &team)

	prov := "invalid-provider"
	rec := doRequest(srv, "PUT", "/api/teams/"+team.ID, UpdateTeamRequest{
		Provider: &prov,
	})

	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400 for invalid provider\nbody: %s", rec.Code, rec.Body.String())
	}
}

// --- OpenCode provider integration tests ---

func TestDeployTeamAsync_OpenCodeProvider_GeneratesOpenCodeFiles(t *testing.T) {
	srv, mock := setupTestServer(t)

	// Set OpenCode-compatible API key in settings.
	srv.db.Create(&models.Settings{Key: "OPENAI_API_KEY", Value: "sk-oai-test-123"})

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:     "opencode-deploy-team",
		Provider: "opencode",
		Agents: []CreateAgentInput{
			{Name: "the-leader", Role: "leader", SystemPrompt: "You lead"},
			{Name: "backend-dev", Role: "worker", SubAgentDescription: "Go backend developer"},
			{Name: "frontend-dev", Role: "worker", SubAgentDescription: "React frontend developer"},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	if team.Provider != "opencode" {
		t.Fatalf("provider: got %q, want 'opencode'", team.Provider)
	}

	srv.deployTeamAsync(team)

	// Only the leader should have been deployed.
	if len(mock.deployedAgents) != 1 {
		t.Fatalf("deployed agents: got %d, want 1", len(mock.deployedAgents))
	}
	if mock.deployedAgents[0] != "the-leader" {
		t.Errorf("deployed agent: got %q, want 'the-leader'", mock.deployedAgents[0])
	}

	// Verify sub-agent files use OpenCode format (not Claude).
	cfg := mock.lastAgentConfig
	if cfg == nil {
		t.Fatal("expected lastAgentConfig to be set")
	}

	// OpenCode sub-agent files should have tools: and permission: sections.
	backendFile, ok := cfg.SubAgentFiles["backend-dev.md"]
	if !ok {
		t.Fatal("missing sub-agent file for backend-dev")
	}
	if !containsStr(backendFile, "tools:") {
		t.Error("OpenCode sub-agent file should contain 'tools:' section")
	}
	if !containsStr(backendFile, "permission:") {
		t.Error("OpenCode sub-agent file should contain 'permission:' section")
	}
	if !containsStr(backendFile, "Go backend developer") {
		t.Error("OpenCode sub-agent file should contain description")
	}

	// Verify that Claude-specific frontmatter is NOT present.
	if containsStr(backendFile, "background: true") {
		t.Error("OpenCode sub-agent file should NOT contain 'background: true' (Claude-specific)")
	}
	if containsStr(backendFile, "isolation: worktree") {
		t.Error("OpenCode sub-agent file should NOT contain 'isolation: worktree' (Claude-specific)")
	}

	// Verify provider is passed in the agent config.
	if cfg.Provider != "opencode" {
		t.Errorf("agent config provider: got %q, want 'opencode'", cfg.Provider)
	}

	// Verify the ClaudeMD (AGENTS.MD content) contains team info.
	if cfg.ClaudeMD == "" {
		t.Error("expected ClaudeMD (AGENTS.MD) to be set for OpenCode leader")
	}
	if !containsStr(cfg.ClaudeMD, "# Team: opencode-deploy-team") {
		t.Error("AGENTS.MD should contain team name header")
	}
	if !containsStr(cfg.ClaudeMD, "backend-dev") {
		t.Error("AGENTS.MD should list worker backend-dev")
	}
}

func TestDeployTeamAsync_OpenCodeProvider_ForwardsOpenCodeEnvVars(t *testing.T) {
	srv, mock := setupTestServer(t)

	// Set OpenCode-specific settings.
	srv.db.Create(&models.Settings{Key: "OPENAI_API_KEY", Value: "sk-oai-test"})
	srv.db.Create(&models.Settings{Key: "OPENCODE_MODEL", Value: "gpt-4o"})

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:     "opencode-env-team",
		Provider: "opencode",
		Agents: []CreateAgentInput{
			{Name: "the-leader", Role: "leader"},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.deployTeamAsync(team)

	cfg := mock.lastAgentConfig
	if cfg == nil {
		t.Fatal("expected lastAgentConfig to be set")
	}

	// OPENCODE_MODEL should be forwarded for OpenCode teams.
	if cfg.Env["OPENCODE_MODEL"] != "gpt-4o" {
		t.Errorf("OPENCODE_MODEL: got %q, want 'gpt-4o'", cfg.Env["OPENCODE_MODEL"])
	}
}

func TestDeployTeamAsync_ClaudeProvider_DoesNotForwardOpenCodeModel(t *testing.T) {
	srv, mock := setupTestServer(t)

	// Set settings that include OpenCode-specific keys.
	srv.db.Create(&models.Settings{Key: "ANTHROPIC_API_KEY", Value: "sk-ant-test"})
	srv.db.Create(&models.Settings{Key: "OPENCODE_MODEL", Value: "gpt-4o"})

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:     "claude-no-opencode-env",
		Provider: "claude",
		Agents: []CreateAgentInput{
			{Name: "the-leader", Role: "leader"},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.deployTeamAsync(team)

	cfg := mock.lastAgentConfig
	if cfg == nil {
		t.Fatal("expected lastAgentConfig to be set")
	}

	// Claude teams should NOT have OPENCODE_MODEL explicitly set by the deploy logic.
	// Note: the key may still be in the raw settings env, but deployTeamAsync
	// only adds OPENCODE_MODEL for opencode providers.
	if cfg.Provider != "claude" {
		t.Errorf("agent config provider: got %q, want 'claude'", cfg.Provider)
	}
}

func TestCreateTeam_ProviderPersistedInDB(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:     "persist-prov",
		Provider: "opencode",
	})
	var created models.Team
	parseJSON(t, rec, &created)

	// Re-fetch from DB to verify persistence.
	getRec := doRequest(srv, "GET", "/api/teams/"+created.ID, nil)
	var fetched models.Team
	parseJSON(t, getRec, &fetched)

	if fetched.Provider != "opencode" {
		t.Errorf("persisted provider: got %q, want 'opencode'", fetched.Provider)
	}
}

func TestDeployTeamAsync_OpenCodeProvider_WithInstructionsMD(t *testing.T) {
	srv, mock := setupTestServer(t)

	srv.db.Create(&models.Settings{Key: "ANTHROPIC_API_KEY", Value: "sk-ant-test"})

	customInstructions := "# Custom Leader Instructions\n\nThese are custom OpenCode instructions.\n"
	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:     "opencode-instructions-team",
		Provider: "opencode",
		Agents: []CreateAgentInput{
			{Name: "the-leader", Role: "leader", InstructionsMD: customInstructions},
			{Name: "worker-1", Role: "worker", SubAgentDescription: "test worker"},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.deployTeamAsync(team)

	cfg := mock.lastAgentConfig
	if cfg == nil {
		t.Fatal("expected lastAgentConfig to be set")
	}

	// When leader has custom InstructionsMD, it should be used directly.
	if cfg.ClaudeMD != customInstructions {
		t.Errorf("ClaudeMD: got %q, want %q", cfg.ClaudeMD, customInstructions)
	}
}

func TestDeployTeamAsync_OpenCodeProvider_WithSkills(t *testing.T) {
	srv, mock := setupTestServer(t)

	srv.db.Create(&models.Settings{Key: "OPENAI_API_KEY", Value: "sk-oai-test"})

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:     "opencode-skills-team",
		Provider: "opencode",
		Agents: []CreateAgentInput{
			{
				Name: "the-leader", Role: "leader",
				SubAgentSkills: []map[string]string{
					{"repo_url": "https://github.com/org/skills", "skill_name": "global-skill"},
				},
			},
			{
				Name: "worker-1", Role: "worker",
				SubAgentDescription: "test worker",
				SubAgentSkills: []map[string]string{
					{"repo_url": "https://github.com/org/skills", "skill_name": "worker-skill"},
				},
			},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.deployTeamAsync(team)

	cfg := mock.lastAgentConfig
	if cfg == nil {
		t.Fatal("expected lastAgentConfig to be set")
	}

	// Skills should be collected and deduplicated in AGENT_SKILLS_INSTALL.
	skillsJSON := cfg.Env["AGENT_SKILLS_INSTALL"]
	if skillsJSON == "" {
		t.Fatal("expected AGENT_SKILLS_INSTALL to be set")
	}

	var skills []struct {
		RepoURL   string `json:"repo_url"`
		SkillName string `json:"skill_name"`
	}
	if err := json.Unmarshal([]byte(skillsJSON), &skills); err != nil {
		t.Fatalf("failed to parse skills JSON: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d: %s", len(skills), skillsJSON)
	}

	// Verify the worker sub-agent file includes merged global skills.
	workerFile, ok := cfg.SubAgentFiles["worker-1.md"]
	if !ok {
		t.Fatal("missing sub-agent file for worker-1")
	}
	if !containsStr(workerFile, "global-skill") {
		t.Error("worker sub-agent should contain merged global skill")
	}
	if !containsStr(workerFile, "worker-skill") {
		t.Error("worker sub-agent should contain its own skill")
	}
}

func TestDeployTeamAsync_ClaudeProvider_GeneratesClaudeFiles(t *testing.T) {
	srv, mock := setupTestServer(t)

	srv.db.Create(&models.Settings{Key: "ANTHROPIC_API_KEY", Value: "sk-ant-test"})

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:     "claude-files-team",
		Provider: "claude",
		Agents: []CreateAgentInput{
			{Name: "the-leader", Role: "leader", SystemPrompt: "You lead"},
			{Name: "backend-dev", Role: "worker", SubAgentDescription: "Go backend developer"},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.deployTeamAsync(team)

	cfg := mock.lastAgentConfig
	if cfg == nil {
		t.Fatal("expected lastAgentConfig to be set")
	}

	// Claude sub-agent files should have Claude-specific frontmatter.
	backendFile, ok := cfg.SubAgentFiles["backend-dev.md"]
	if !ok {
		t.Fatal("missing sub-agent file for backend-dev")
	}
	if !containsStr(backendFile, "background: true") {
		t.Error("Claude sub-agent file should contain 'background: true'")
	}
	if !containsStr(backendFile, "isolation: worktree") {
		t.Error("Claude sub-agent file should contain 'isolation: worktree'")
	}
	if !containsStr(backendFile, "permissionMode: bypassPermissions") {
		t.Error("Claude sub-agent file should contain 'permissionMode: bypassPermissions'")
	}

	// Claude sub-agent files should NOT have OpenCode-specific sections.
	if containsStr(backendFile, "tools:") {
		t.Error("Claude sub-agent file should NOT contain OpenCode 'tools:' section")
	}
	if containsStr(backendFile, "permission:") {
		t.Error("Claude sub-agent file should NOT contain OpenCode 'permission:' section")
	}

	// Verify provider is claude.
	if cfg.Provider != "claude" {
		t.Errorf("agent config provider: got %q, want 'claude'", cfg.Provider)
	}
}

func TestDeployTeamAsync_OpenCodeProvider_MultipleWorkers(t *testing.T) {
	srv, mock := setupTestServer(t)

	srv.db.Create(&models.Settings{Key: "OPENAI_API_KEY", Value: "sk-oai-test"})

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:     "opencode-multi-worker",
		Provider: "opencode",
		Agents: []CreateAgentInput{
			{Name: "the-leader", Role: "leader"},
			{Name: "worker-a", Role: "worker", SubAgentDescription: "First worker"},
			{Name: "worker-b", Role: "worker", SubAgentDescription: "Second worker"},
			{Name: "worker-c", Role: "worker", SubAgentDescription: "Third worker"},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.deployTeamAsync(team)

	cfg := mock.lastAgentConfig
	if cfg == nil {
		t.Fatal("expected lastAgentConfig to be set")
	}

	// All three workers should have sub-agent files.
	expectedWorkers := []string{"worker-a.md", "worker-b.md", "worker-c.md"}
	for _, name := range expectedWorkers {
		content, ok := cfg.SubAgentFiles[name]
		if !ok {
			t.Errorf("missing sub-agent file for %s", name)
			continue
		}
		// Each should have OpenCode format.
		if !containsStr(content, "tools:") {
			t.Errorf("%s missing 'tools:' section", name)
		}
		if !containsStr(content, "permission:") {
			t.Errorf("%s missing 'permission:' section", name)
		}
	}

	// AGENTS.MD should list all workers.
	if cfg.ClaudeMD == "" {
		t.Fatal("expected ClaudeMD (AGENTS.MD) to be set")
	}
	if !containsStr(cfg.ClaudeMD, "worker-a") {
		t.Error("AGENTS.MD should list worker-a")
	}
	if !containsStr(cfg.ClaudeMD, "worker-b") {
		t.Error("AGENTS.MD should list worker-b")
	}
	if !containsStr(cfg.ClaudeMD, "worker-c") {
		t.Error("AGENTS.MD should list worker-c")
	}
}

func TestDeployTeamAsync_ProviderForwardedInAgentConfig(t *testing.T) {
	// Verify that the provider field is always forwarded to the runtime.
	tests := []struct {
		name         string
		provider     string
		settingsKey  string
		settingsVal  string
		wantProvider string
	}{
		{"claude", "claude", "ANTHROPIC_API_KEY", "sk-ant-test", "claude"},
		{"opencode", "opencode", "OPENAI_API_KEY", "sk-oai-test", "opencode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, mock := setupTestServer(t)

			srv.db.Create(&models.Settings{Key: tt.settingsKey, Value: tt.settingsVal})

			teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
				Name:     "provider-fwd-" + tt.name,
				Provider: tt.provider,
				Agents: []CreateAgentInput{
					{Name: "the-leader", Role: "leader"},
				},
			})
			var team models.Team
			parseJSON(t, teamRec, &team)

			srv.deployTeamAsync(team)

			cfg := mock.lastAgentConfig
			if cfg == nil {
				t.Fatal("expected lastAgentConfig to be set")
			}
			if cfg.Provider != tt.wantProvider {
				t.Errorf("provider: got %q, want %q", cfg.Provider, tt.wantProvider)
			}
		})
	}
}

func TestDeployTeamAsync_ClaudeProvider_DoesNotGenerateOpenCodeFormat(t *testing.T) {
	srv, mock := setupTestServer(t)

	srv.db.Create(&models.Settings{Key: "ANTHROPIC_API_KEY", Value: "sk-ant-test"})

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:     "claude-no-opencode",
		Provider: "claude",
		Agents: []CreateAgentInput{
			{Name: "the-leader", Role: "leader"},
			{Name: "worker-1", Role: "worker", SubAgentDescription: "test worker"},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.deployTeamAsync(team)

	cfg := mock.lastAgentConfig
	if cfg == nil {
		t.Fatal("expected lastAgentConfig to be set")
	}

	// ClaudeMD should be Claude format, not OpenCode AGENTS.MD.
	if containsStr(cfg.ClaudeMD, "# Team:") {
		t.Error("Claude deploy should NOT generate OpenCode '# Team:' header in ClaudeMD")
	}

	// Sub-agent files should be Claude format.
	workerFile, ok := cfg.SubAgentFiles["worker-1.md"]
	if !ok {
		t.Fatal("missing worker-1.md sub-agent file")
	}
	// Claude format has 'name:' in frontmatter; OpenCode format has 'tools:'.
	if !containsStr(workerFile, "name: worker-1") {
		t.Error("Claude sub-agent should have 'name:' in frontmatter")
	}
}

func TestDeployTeamAsync_OpenCodeProvider_NoSubAgentNameField(t *testing.T) {
	srv, mock := setupTestServer(t)

	srv.db.Create(&models.Settings{Key: "OPENAI_API_KEY", Value: "sk-oai-test"})

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:     "opencode-no-name-field",
		Provider: "opencode",
		Agents: []CreateAgentInput{
			{Name: "the-leader", Role: "leader"},
			{Name: "worker-1", Role: "worker", SubAgentDescription: "Backend dev"},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.deployTeamAsync(team)

	cfg := mock.lastAgentConfig
	if cfg == nil {
		t.Fatal("expected lastAgentConfig to be set")
	}

	// OpenCode sub-agent files do NOT have a 'name:' field in frontmatter.
	workerFile := cfg.SubAgentFiles["worker-1.md"]
	if containsStr(workerFile, "name:") {
		t.Error("OpenCode sub-agent file should NOT have 'name:' in frontmatter (Claude-specific)")
	}
}

func TestLoadSettingsEnv_OpenCodeKeys(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Set OpenCode-relevant keys.
	srv.db.Create(&models.Settings{Key: "OPENAI_API_KEY", Value: "sk-oai-123"})
	srv.db.Create(&models.Settings{Key: "GOOGLE_GENERATIVE_AI_API_KEY", Value: "goog-123"})
	srv.db.Create(&models.Settings{Key: "OPENCODE_MODEL", Value: "gpt-4o"})

	env := srv.LoadSettingsEnv()

	if env["OPENAI_API_KEY"] != "sk-oai-123" {
		t.Errorf("OPENAI_API_KEY: got %q", env["OPENAI_API_KEY"])
	}
	if env["GOOGLE_GENERATIVE_AI_API_KEY"] != "goog-123" {
		t.Errorf("GOOGLE_GENERATIVE_AI_API_KEY: got %q", env["GOOGLE_GENERATIVE_AI_API_KEY"])
	}
	if env["OPENCODE_MODEL"] != "gpt-4o" {
		t.Errorf("OPENCODE_MODEL: got %q", env["OPENCODE_MODEL"])
	}
}

func TestDeployTeamAsync_ClaudeProvider_LeaderModelPassedAsEnvVar(t *testing.T) {
	srv, mock := setupTestServer(t)

	srv.db.Create(&models.Settings{Key: "ANTHROPIC_API_KEY", Value: "sk-ant-test"})

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:     "claude-leader-model",
		Provider: "claude",
		Agents: []CreateAgentInput{
			{Name: "the-leader", Role: "leader", SubAgentModel: "sonnet"},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.deployTeamAsync(team)

	cfg := mock.lastAgentConfig
	if cfg == nil {
		t.Fatal("expected lastAgentConfig to be set")
	}

	want := "claude-sonnet-4-20250514"
	if cfg.Env["CLAUDE_MODEL"] != want {
		t.Errorf("CLAUDE_MODEL: got %q, want %q", cfg.Env["CLAUDE_MODEL"], want)
	}
}

func TestDeployTeamAsync_ClaudeProvider_LeaderModelOpus(t *testing.T) {
	srv, mock := setupTestServer(t)

	srv.db.Create(&models.Settings{Key: "ANTHROPIC_API_KEY", Value: "sk-ant-test"})

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:     "claude-leader-opus",
		Provider: "claude",
		Agents: []CreateAgentInput{
			{Name: "the-leader", Role: "leader", SubAgentModel: "opus"},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.deployTeamAsync(team)

	cfg := mock.lastAgentConfig
	if cfg == nil {
		t.Fatal("expected lastAgentConfig to be set")
	}

	want := "claude-opus-4-20250514"
	if cfg.Env["CLAUDE_MODEL"] != want {
		t.Errorf("CLAUDE_MODEL: got %q, want %q", cfg.Env["CLAUDE_MODEL"], want)
	}
}

func TestDeployTeamAsync_ClaudeProvider_LeaderModelHaiku(t *testing.T) {
	srv, mock := setupTestServer(t)

	srv.db.Create(&models.Settings{Key: "ANTHROPIC_API_KEY", Value: "sk-ant-test"})

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:     "claude-leader-haiku",
		Provider: "claude",
		Agents: []CreateAgentInput{
			{Name: "the-leader", Role: "leader", SubAgentModel: "haiku"},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.deployTeamAsync(team)

	cfg := mock.lastAgentConfig
	if cfg == nil {
		t.Fatal("expected lastAgentConfig to be set")
	}

	want := "claude-haiku-4-5-20251001"
	if cfg.Env["CLAUDE_MODEL"] != want {
		t.Errorf("CLAUDE_MODEL: got %q, want %q", cfg.Env["CLAUDE_MODEL"], want)
	}
}

func TestDeployTeamAsync_ClaudeProvider_LeaderModelInherit_NoEnvVar(t *testing.T) {
	srv, mock := setupTestServer(t)

	srv.db.Create(&models.Settings{Key: "ANTHROPIC_API_KEY", Value: "sk-ant-test"})

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:     "claude-leader-inherit",
		Provider: "claude",
		Agents: []CreateAgentInput{
			{Name: "the-leader", Role: "leader", SubAgentModel: "inherit"},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.deployTeamAsync(team)

	cfg := mock.lastAgentConfig
	if cfg == nil {
		t.Fatal("expected lastAgentConfig to be set")
	}

	// When model is "inherit", CLAUDE_MODEL should NOT be set.
	if v, ok := cfg.Env["CLAUDE_MODEL"]; ok && v != "" {
		t.Errorf("CLAUDE_MODEL should not be set for 'inherit', got %q", v)
	}
}

func TestDeployTeamAsync_OpenCodeProvider_LeaderModelOverridesSettings(t *testing.T) {
	srv, mock := setupTestServer(t)

	srv.db.Create(&models.Settings{Key: "OPENAI_API_KEY", Value: "sk-oai-test"})
	srv.db.Create(&models.Settings{Key: "OPENCODE_MODEL", Value: "gpt-4o"})

	// CreateTeam does not validate SubAgentModel, so OpenCode format is accepted.
	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:     "opencode-leader-model",
		Provider: "opencode",
		Agents: []CreateAgentInput{
			{Name: "the-leader", Role: "leader", SubAgentModel: "anthropic/claude-sonnet-4-20250514"},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.deployTeamAsync(team)

	cfg := mock.lastAgentConfig
	if cfg == nil {
		t.Fatal("expected lastAgentConfig to be set")
	}

	// Leader's model should override the Settings OPENCODE_MODEL.
	want := "anthropic/claude-sonnet-4-20250514"
	if cfg.Env["OPENCODE_MODEL"] != want {
		t.Errorf("OPENCODE_MODEL: got %q, want %q", cfg.Env["OPENCODE_MODEL"], want)
	}
}

func TestDeployTeamAsync_OpenCodeProvider_LeaderModelInherit_FallsBackToSettings(t *testing.T) {
	srv, mock := setupTestServer(t)

	srv.db.Create(&models.Settings{Key: "OPENAI_API_KEY", Value: "sk-oai-test"})
	srv.db.Create(&models.Settings{Key: "OPENCODE_MODEL", Value: "gpt-4o"})

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:     "opencode-leader-inherit",
		Provider: "opencode",
		Agents: []CreateAgentInput{
			{Name: "the-leader", Role: "leader"},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.deployTeamAsync(team)

	cfg := mock.lastAgentConfig
	if cfg == nil {
		t.Fatal("expected lastAgentConfig to be set")
	}

	// When leader model is "inherit" (default), should fall back to Settings OPENCODE_MODEL.
	if cfg.Env["OPENCODE_MODEL"] != "gpt-4o" {
		t.Errorf("OPENCODE_MODEL: got %q, want 'gpt-4o'", cfg.Env["OPENCODE_MODEL"])
	}
}

func TestDeployTeamAsync_OpenCodeProvider_WritesWorkspaceToHost(t *testing.T) {
	srv, mock := setupTestServer(t)

	// Set OpenCode-compatible API key in settings.
	srv.db.Create(&models.Settings{Key: "OPENAI_API_KEY", Value: "sk-oai-test-ws"})

	// Use a temporary directory as the workspace path so the test can verify
	// that deployTeamAsync writes .opencode/ files to the host filesystem.
	tmpDir := t.TempDir()

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:          "opencode-workspace-host",
		Provider:      "opencode",
		WorkspacePath: tmpDir,
		Agents: []CreateAgentInput{
			{Name: "the-leader", Role: "leader", Specialty: "orchestration"},
			{Name: "backend-dev", Role: "worker", SubAgentDescription: "Go backend developer"},
			{Name: "frontend-dev", Role: "worker", SubAgentDescription: "React frontend developer"},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.deployTeamAsync(team)

	// Verify the leader was deployed.
	if len(mock.deployedAgents) != 1 {
		t.Fatalf("deployed agents: got %d, want 1", len(mock.deployedAgents))
	}

	// Verify .opencode/AGENTS.MD was written to the host workspace path.
	agentsMDPath := filepath.Join(tmpDir, ".opencode", "AGENTS.MD")
	agentsMD, err := os.ReadFile(agentsMDPath)
	if err != nil {
		t.Fatalf("AGENTS.MD not created on host: %v", err)
	}
	if !containsStr(string(agentsMD), "# Team: opencode-workspace-host") {
		t.Error("AGENTS.MD missing team name header")
	}
	if !containsStr(string(agentsMD), "the-leader") {
		t.Error("AGENTS.MD missing leader name")
	}
	if !containsStr(string(agentsMD), "backend-dev") {
		t.Error("AGENTS.MD missing worker backend-dev in team roster")
	}
	if !containsStr(string(agentsMD), "frontend-dev") {
		t.Error("AGENTS.MD missing worker frontend-dev in team roster")
	}

	// Verify worker agent files were written.
	backendPath := filepath.Join(tmpDir, ".opencode", "agents", "backend-dev.md")
	backendData, err := os.ReadFile(backendPath)
	if err != nil {
		t.Fatalf("backend-dev.md not created on host: %v", err)
	}
	if !containsStr(string(backendData), "Go backend developer") {
		t.Error("backend-dev.md missing description")
	}
	if !containsStr(string(backendData), "tools:") {
		t.Error("backend-dev.md missing tools section (OpenCode format)")
	}

	frontendPath := filepath.Join(tmpDir, ".opencode", "agents", "frontend-dev.md")
	frontendData, err := os.ReadFile(frontendPath)
	if err != nil {
		t.Fatalf("frontend-dev.md not created on host: %v", err)
	}
	if !containsStr(string(frontendData), "React frontend developer") {
		t.Error("frontend-dev.md missing description")
	}

	// Verify the workspace path was passed to the agent config for Docker bind mount.
	cfg := mock.lastAgentConfig
	if cfg == nil {
		t.Fatal("expected lastAgentConfig to be set")
	}
	if cfg.WorkspacePath != tmpDir {
		t.Errorf("agent config WorkspacePath: got %q, want %q", cfg.WorkspacePath, tmpDir)
	}
}

func TestDeployTeamAsync_OpenCodeProvider_NoWorkspacePath_SkipsHostWrite(t *testing.T) {
	srv, mock := setupTestServer(t)

	// Set OpenCode-compatible API key in settings.
	srv.db.Create(&models.Settings{Key: "OPENAI_API_KEY", Value: "sk-oai-test-no-ws"})

	// No WorkspacePath — should still deploy without errors.
	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:     "opencode-no-workspace",
		Provider: "opencode",
		Agents: []CreateAgentInput{
			{Name: "the-leader", Role: "leader"},
			{Name: "worker-1", Role: "worker", SubAgentDescription: "Backend dev"},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.deployTeamAsync(team)

	// Deploy should succeed.
	if len(mock.deployedAgents) != 1 {
		t.Fatalf("deployed agents: got %d, want 1", len(mock.deployedAgents))
	}

	// Verify team is running.
	var updated models.Team
	srv.db.First(&updated, "id = ?", team.ID)
	if updated.Status != models.TeamStatusRunning {
		t.Errorf("team status: got %q, want %q", updated.Status, models.TeamStatusRunning)
	}
}

func TestClaudeModelID(t *testing.T) {
	tests := []struct {
		short string
		want  string
	}{
		{"sonnet", "claude-sonnet-4-20250514"},
		{"opus", "claude-opus-4-20250514"},
		{"haiku", "claude-haiku-4-5-20251001"},
		{"inherit", ""},
		{"", ""},
		{"unknown", ""},
	}
	for _, tt := range tests {
		t.Run(tt.short, func(t *testing.T) {
			got := claudeModelID(tt.short)
			if got != tt.want {
				t.Errorf("claudeModelID(%q) = %q, want %q", tt.short, got, tt.want)
			}
		})
	}
}

func containsStr(s, substr string) bool {
	return strings.Contains(s, substr)
}
