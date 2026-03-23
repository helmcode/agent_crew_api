package api

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/helmcode/agent-crew/internal/models"
	"github.com/helmcode/agent-crew/internal/runtime"
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
	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "ANTHROPIC_API_KEY", Value: "sk-test-123"})

	env := srv.LoadSettingsEnv("00000000-0000-0000-0000-000000000000")

	if env["ANTHROPIC_API_KEY"] != "sk-test-123" {
		t.Errorf("ANTHROPIC_API_KEY: got %q, want 'sk-test-123'", env["ANTHROPIC_API_KEY"])
	}
}

func TestLoadSettingsEnv_OAuthToken(t *testing.T) {
	srv, _ := setupTestServer(t)

	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "CLAUDE_CODE_OAUTH_TOKEN", Value: "oauth-abc"})

	env := srv.LoadSettingsEnv("00000000-0000-0000-0000-000000000000")

	if env["CLAUDE_CODE_OAUTH_TOKEN"] != "oauth-abc" {
		t.Errorf("CLAUDE_CODE_OAUTH_TOKEN: got %q, want 'oauth-abc'", env["CLAUDE_CODE_OAUTH_TOKEN"])
	}
}

func TestLoadSettingsEnv_AliasMapping(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Set the alias key (ANTHROPIC_AUTH_TOKEN maps to CLAUDE_CODE_OAUTH_TOKEN).
	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "ANTHROPIC_AUTH_TOKEN", Value: "alias-token"})

	env := srv.LoadSettingsEnv("00000000-0000-0000-0000-000000000000")

	// Should be mapped to the target key.
	if env["CLAUDE_CODE_OAUTH_TOKEN"] != "alias-token" {
		t.Errorf("CLAUDE_CODE_OAUTH_TOKEN via alias: got %q, want 'alias-token'", env["CLAUDE_CODE_OAUTH_TOKEN"])
	}
}

func TestLoadSettingsEnv_PrimaryOverridesAlias(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Set both primary and alias keys.
	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "CLAUDE_CODE_OAUTH_TOKEN", Value: "primary-token"})
	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "ANTHROPIC_AUTH_TOKEN", Value: "alias-token"})

	env := srv.LoadSettingsEnv("00000000-0000-0000-0000-000000000000")

	// Primary key should take precedence.
	if env["CLAUDE_CODE_OAUTH_TOKEN"] != "primary-token" {
		t.Errorf("primary should override alias: got %q, want 'primary-token'", env["CLAUDE_CODE_OAUTH_TOKEN"])
	}
}

func TestLoadSettingsEnv_Empty(t *testing.T) {
	srv, _ := setupTestServer(t)

	env := srv.LoadSettingsEnv("00000000-0000-0000-0000-000000000000")

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
	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "OPENAI_API_KEY", Value: "sk-oai-test-123"})

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
	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "OPENAI_API_KEY", Value: "sk-oai-test"})
	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "OPENCODE_MODEL", Value: "gpt-4o"})

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
	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "ANTHROPIC_API_KEY", Value: "sk-ant-test"})
	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "OPENCODE_MODEL", Value: "gpt-4o"})

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

	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "ANTHROPIC_API_KEY", Value: "sk-ant-test"})

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

	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "OPENAI_API_KEY", Value: "sk-oai-test"})

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

	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "ANTHROPIC_API_KEY", Value: "sk-ant-test"})

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

	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "OPENAI_API_KEY", Value: "sk-oai-test"})

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

			srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: tt.settingsKey, Value: tt.settingsVal})

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

	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "ANTHROPIC_API_KEY", Value: "sk-ant-test"})

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

	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "OPENAI_API_KEY", Value: "sk-oai-test"})

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
	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "OPENAI_API_KEY", Value: "sk-oai-123"})
	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "GOOGLE_GENERATIVE_AI_API_KEY", Value: "goog-123"})
	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "OPENCODE_MODEL", Value: "gpt-4o"})

	env := srv.LoadSettingsEnv("00000000-0000-0000-0000-000000000000")

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

	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "ANTHROPIC_API_KEY", Value: "sk-ant-test"})

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

	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "ANTHROPIC_API_KEY", Value: "sk-ant-test"})

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

	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "ANTHROPIC_API_KEY", Value: "sk-ant-test"})

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

	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "ANTHROPIC_API_KEY", Value: "sk-ant-test"})

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

	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "OPENAI_API_KEY", Value: "sk-oai-test"})
	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "OPENCODE_MODEL", Value: "gpt-4o"})

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

	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "OPENAI_API_KEY", Value: "sk-oai-test"})
	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "OPENCODE_MODEL", Value: "gpt-4o"})

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
	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "OPENAI_API_KEY", Value: "sk-oai-test-ws"})

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
	srv.db.Create(&models.Settings{OrgID: "00000000-0000-0000-0000-000000000000", Key: "OPENAI_API_KEY", Value: "sk-oai-test-no-ws"})

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

// --- StatusMessage tests ---

func TestStatusMessage_ReturnedInTeamJSON(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:   "status-msg-team",
		Agents: []CreateAgentInput{{Name: "a1", Role: "leader"}},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	// Verify status_message is present in JSON response (empty by default).
	if team.StatusMessage != "" {
		t.Errorf("status_message should be empty on creation, got %q", team.StatusMessage)
	}

	// Manually set a status message to verify it's returned via GET.
	srv.db.Model(&team).Updates(map[string]interface{}{
		"status":         models.TeamStatusError,
		"status_message": "test error message",
	})

	getRec := doRequest(srv, "GET", "/api/teams/"+team.ID, nil)
	if getRec.Code != 200 {
		t.Fatalf("status: got %d, want 200", getRec.Code)
	}

	var fetched models.Team
	parseJSON(t, getRec, &fetched)
	if fetched.StatusMessage != "test error message" {
		t.Errorf("status_message: got %q, want 'test error message'", fetched.StatusMessage)
	}
}

func TestStatusMessage_ClearedOnDeploy(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:   "clear-msg-team",
		Agents: []CreateAgentInput{{Name: "a1", Role: "leader"}},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	// Set an error status with a message.
	srv.db.Model(&team).Updates(map[string]interface{}{
		"status":         models.TeamStatusError,
		"status_message": "previous error",
	})

	// Deploy again — status_message should be cleared to "".
	// First reset to stopped so deploy is allowed.
	srv.db.Model(&team).Update("status", models.TeamStatusStopped)

	rec := doRequest(srv, "POST", "/api/teams/"+team.ID+"/deploy", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	var deployed models.Team
	parseJSON(t, rec, &deployed)
	if deployed.StatusMessage != "" {
		t.Errorf("status_message should be cleared on deploy, got %q", deployed.StatusMessage)
	}

	// Also verify in DB.
	var dbTeam models.Team
	srv.db.First(&dbTeam, "id = ?", team.ID)
	if dbTeam.StatusMessage != "" {
		t.Errorf("status_message in DB should be cleared, got %q", dbTeam.StatusMessage)
	}
}

func TestStatusMessage_PopulatedOnNoLeader(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name: "no-leader-msg-team",
		Agents: []CreateAgentInput{
			{Name: "worker-1", Role: "worker"},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	// Call deployTeamAsync synchronously (no leader → error).
	srv.deployTeamAsync(team)

	var updated models.Team
	srv.db.First(&updated, "id = ?", team.ID)
	if updated.Status != models.TeamStatusError {
		t.Errorf("status: got %q, want %q", updated.Status, models.TeamStatusError)
	}
	if updated.StatusMessage != "No leader agent found in team configuration" {
		t.Errorf("status_message: got %q, want 'No leader agent found in team configuration'", updated.StatusMessage)
	}
}

func TestStatusMessage_PopulatedOnInfraDeployFailure(t *testing.T) {
	srv, mock := setupTestServer(t)

	mock.deployInfraErr = errors.New("Docker daemon not reachable")

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:   "infra-fail-msg-team",
		Agents: []CreateAgentInput{{Name: "a1", Role: "leader"}},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.deployTeamAsync(team)

	var updated models.Team
	srv.db.First(&updated, "id = ?", team.ID)
	if updated.Status != models.TeamStatusError {
		t.Errorf("status: got %q, want %q", updated.Status, models.TeamStatusError)
	}
	want := "Failed to deploy infrastructure: Docker daemon not reachable"
	if updated.StatusMessage != want {
		t.Errorf("status_message: got %q, want %q", updated.StatusMessage, want)
	}
}

func TestStatusMessage_PopulatedOnAgentDeployFailure(t *testing.T) {
	srv, mock := setupTestServer(t)

	mock.deployAgentErr = errors.New("no auth configured: set ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN in the Settings page")

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:   "agent-fail-msg-team",
		Agents: []CreateAgentInput{{Name: "the-leader", Role: "leader"}},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.deployTeamAsync(team)

	var updated models.Team
	srv.db.First(&updated, "id = ?", team.ID)
	if updated.Status != models.TeamStatusError {
		t.Errorf("status: got %q, want %q", updated.Status, models.TeamStatusError)
	}
	want := "no auth configured: set ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN in the Settings page"
	if updated.StatusMessage != want {
		t.Errorf("status_message: got %q, want %q", updated.StatusMessage, want)
	}
}

func TestStatusMessage_ClearedOnStop(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:   "stop-clear-msg-team",
		Agents: []CreateAgentInput{{Name: "a1", Role: "leader"}},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	// Set team to error with a message.
	srv.db.Model(&team).Updates(map[string]interface{}{
		"status":         models.TeamStatusError,
		"status_message": "some deploy error",
	})

	// Stop the team — status_message should be cleared.
	rec := doRequest(srv, "POST", "/api/teams/"+team.ID+"/stop", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	var stopped models.Team
	parseJSON(t, rec, &stopped)
	if stopped.StatusMessage != "" {
		t.Errorf("status_message should be cleared on stop, got %q", stopped.StatusMessage)
	}

	// Verify in DB.
	var dbTeam models.Team
	srv.db.First(&dbTeam, "id = ?", team.ID)
	if dbTeam.StatusMessage != "" {
		t.Errorf("status_message in DB should be cleared on stop, got %q", dbTeam.StatusMessage)
	}
}

func TestStatusMessage_ReturnedInListTeams(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:   "list-msg-team",
		Agents: []CreateAgentInput{{Name: "a1", Role: "leader"}},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.db.Model(&team).Updates(map[string]interface{}{
		"status":         models.TeamStatusError,
		"status_message": "deploy failed: missing key",
	})

	rec := doRequest(srv, "GET", "/api/teams", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var teams []models.Team
	parseJSON(t, rec, &teams)
	if len(teams) != 1 {
		t.Fatalf("teams: got %d, want 1", len(teams))
	}
	if teams[0].StatusMessage != "deploy failed: missing key" {
		t.Errorf("status_message in list: got %q, want 'deploy failed: missing key'", teams[0].StatusMessage)
	}
}

// --- model_provider validation tests ---

func TestCreateTeam_ModelProviderValid(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:          "mp-valid-team",
		Provider:      "opencode",
		ModelProvider: "anthropic",
		Agents:        []CreateAgentInput{{Name: "leader", Role: "leader"}},
	})
	if rec.Code != 201 {
		t.Fatalf("status: got %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}

	var team models.Team
	parseJSON(t, rec, &team)
	if team.ModelProvider != "anthropic" {
		t.Errorf("model_provider: got %q, want %q", team.ModelProvider, "anthropic")
	}
}

func TestCreateTeam_ModelProviderInvalid(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:          "mp-invalid-team",
		Provider:      "opencode",
		ModelProvider: "aws-bedrock",
		Agents:        []CreateAgentInput{{Name: "leader", Role: "leader"}},
	})
	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400\nbody: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid model_provider") {
		t.Errorf("body should contain 'invalid model_provider': %s", rec.Body.String())
	}
}

func TestCreateTeam_ModelProviderIgnoredForClaude(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Claude provider should accept any model_provider (it's ignored).
	rec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:          "mp-claude-team",
		Provider:      "claude",
		ModelProvider: "anything",
		Agents:        []CreateAgentInput{{Name: "leader", Role: "leader"}},
	})
	if rec.Code != 201 {
		t.Fatalf("status: got %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateTeam_AgentModelConsistency(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Agent model doesn't match team's model_provider.
	rec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:          "mp-mismatch-team",
		Provider:      "opencode",
		ModelProvider: "anthropic",
		Agents: []CreateAgentInput{
			{Name: "leader", Role: "leader"},
			{Name: "worker", Role: "worker", SubAgentModel: "openai/gpt-4"},
		},
	})
	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400\nbody: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "doesn't match team model_provider") {
		t.Errorf("body should contain mismatch error: %s", rec.Body.String())
	}
}

func TestUpdateTeam_ModelProviderResetsAgentModels(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Create team with model_provider=anthropic and an agent with a matching model.
	createRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:          "mp-reset-team",
		Provider:      "opencode",
		ModelProvider: "anthropic",
		Agents: []CreateAgentInput{
			{Name: "leader", Role: "leader", SubAgentModel: "anthropic/claude-sonnet-4-20250514"},
			{Name: "worker", Role: "worker", SubAgentModel: "anthropic/claude-haiku-4-5-20251001"},
		},
	})
	var team models.Team
	parseJSON(t, createRec, &team)

	// Update to a different model_provider.
	newMP := "openai"
	rec := doRequest(srv, "PUT", "/api/teams/"+team.ID, UpdateTeamRequest{
		ModelProvider: &newMP,
	})
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	var updated models.Team
	parseJSON(t, rec, &updated)
	if updated.ModelProvider != "openai" {
		t.Errorf("model_provider: got %q, want %q", updated.ModelProvider, "openai")
	}

	// All agents should have been reset to "inherit".
	var agents []models.Agent
	srv.db.Where("team_id = ?", team.ID).Find(&agents)
	for _, a := range agents {
		if a.SubAgentModel != "inherit" {
			t.Errorf("agent %q sub_agent_model: got %q, want %q", a.Name, a.SubAgentModel, "inherit")
		}
	}
}

func TestUpdateTeam_ProviderSwitchToClaude_ClearsModelProvider(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Create an OpenCode team with model_provider=openai.
	createRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:          "mp-switch-claude-team",
		Provider:      "opencode",
		ModelProvider: "openai",
		Agents:        []CreateAgentInput{{Name: "leader", Role: "leader"}},
	})
	var team models.Team
	parseJSON(t, createRec, &team)
	if team.ModelProvider != "openai" {
		t.Fatalf("model_provider: got %q, want %q", team.ModelProvider, "openai")
	}

	// Switch provider to Claude.
	claude := "claude"
	rec := doRequest(srv, "PUT", "/api/teams/"+team.ID, UpdateTeamRequest{
		Provider: &claude,
	})
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	var updated models.Team
	parseJSON(t, rec, &updated)
	if updated.Provider != "claude" {
		t.Errorf("provider: got %q, want %q", updated.Provider, "claude")
	}
	if updated.ModelProvider != "" {
		t.Errorf("model_provider should be cleared, got %q", updated.ModelProvider)
	}
}

func TestCreateAgent_ModelConsistencyValidation(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Create team with model_provider=openai.
	createRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:          "agent-mp-team",
		Provider:      "opencode",
		ModelProvider: "openai",
		Agents:        []CreateAgentInput{{Name: "leader", Role: "leader"}},
	})
	var team models.Team
	parseJSON(t, createRec, &team)

	// Try to add agent with mismatched model.
	rec := doRequest(srv, "POST", "/api/teams/"+team.ID+"/agents", CreateAgentRequest{
		Name:          "bad-worker",
		Role:          "worker",
		SubAgentModel: "anthropic/claude-sonnet-4-20250514",
	})
	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400\nbody: %s", rec.Code, rec.Body.String())
	}

	// Add agent with matching model — should succeed.
	rec = doRequest(srv, "POST", "/api/teams/"+team.ID+"/agents", CreateAgentRequest{
		Name:          "good-worker",
		Role:          "worker",
		SubAgentModel: "openai/gpt-4o",
	})
	if rec.Code != 201 {
		t.Fatalf("status: got %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}
}

func TestUpdateAgent_ModelConsistencyValidation(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Create team with model_provider=google.
	createRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:          "update-agent-mp-team",
		Provider:      "opencode",
		ModelProvider: "google",
		Agents: []CreateAgentInput{
			{Name: "leader", Role: "leader"},
			{Name: "worker", Role: "worker"},
		},
	})
	var team models.Team
	parseJSON(t, createRec, &team)

	// Find the worker agent.
	var worker models.Agent
	for _, a := range team.Agents {
		if a.Role == "worker" {
			worker = a
			break
		}
	}

	// Try to update with mismatched model.
	badModel := "anthropic/claude-sonnet-4-20250514"
	rec := doRequest(srv, "PUT", "/api/teams/"+team.ID+"/agents/"+worker.ID, UpdateAgentRequest{
		SubAgentModel: &badModel,
	})
	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400\nbody: %s", rec.Code, rec.Body.String())
	}

	// Update with matching model — should succeed.
	goodModel := "google/gemini-pro"
	rec = doRequest(srv, "PUT", "/api/teams/"+team.ID+"/agents/"+worker.ID, UpdateAgentRequest{
		SubAgentModel: &goodModel,
	})
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
}

func TestFilterAPIKeysByModelProvider(t *testing.T) {
	tests := []struct {
		name          string
		modelProvider string
		inputKeys     map[string]string
		wantKeys      map[string]bool // keys that should remain
		wantRemoved   []string        // keys that should be removed
	}{
		{
			name:          "anthropic keeps only anthropic keys",
			modelProvider: "anthropic",
			inputKeys: map[string]string{
				"ANTHROPIC_API_KEY":            "sk-ant-123",
				"OPENAI_API_KEY":               "sk-openai-123",
				"GOOGLE_API_KEY":               "google-123",
				"GOOGLE_GENERATIVE_AI_API_KEY": "google-gen-123",
				"OTHER_VAR":                    "keep-me",
			},
			wantKeys:    map[string]bool{"ANTHROPIC_API_KEY": true, "OTHER_VAR": true},
			wantRemoved: []string{"OPENAI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY"},
		},
		{
			name:          "openai keeps only openai keys",
			modelProvider: "openai",
			inputKeys: map[string]string{
				"ANTHROPIC_API_KEY": "sk-ant-123",
				"OPENAI_API_KEY":   "sk-openai-123",
				"SOME_CONFIG":      "value",
			},
			wantKeys:    map[string]bool{"OPENAI_API_KEY": true, "SOME_CONFIG": true},
			wantRemoved: []string{"ANTHROPIC_API_KEY"},
		},
		{
			name:          "google keeps all google key variants",
			modelProvider: "google",
			inputKeys: map[string]string{
				"ANTHROPIC_API_KEY":            "sk-ant-123",
				"OPENAI_API_KEY":               "sk-openai-123",
				"GOOGLE_API_KEY":               "google-123",
				"GEMINI_API_KEY":               "gemini-123",
				"GOOGLE_GENERATIVE_AI_API_KEY": "google-gen-123",
				"SOME_CONFIG":                  "value",
			},
			wantKeys:    map[string]bool{"GOOGLE_API_KEY": true, "GEMINI_API_KEY": true, "GOOGLE_GENERATIVE_AI_API_KEY": true, "SOME_CONFIG": true},
			wantRemoved: []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
		},
		{
			name:          "ollama removes all provider keys",
			modelProvider: "ollama",
			inputKeys: map[string]string{
				"ANTHROPIC_API_KEY": "sk-ant-123",
				"OPENAI_API_KEY":   "sk-openai-123",
				"OLLAMA_HOST":      "http://localhost:11434",
			},
			wantKeys:    map[string]bool{"OLLAMA_HOST": true},
			wantRemoved: []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := make(map[string]string)
			for k, v := range tt.inputKeys {
				env[k] = v
			}
			filterAPIKeysByModelProvider(env, tt.modelProvider)

			for key := range tt.wantKeys {
				if _, ok := env[key]; !ok {
					t.Errorf("key %q should be present but was removed", key)
				}
			}
			for _, key := range tt.wantRemoved {
				if _, ok := env[key]; ok {
					t.Errorf("key %q should be removed but is still present", key)
				}
			}
		})
	}
}

func TestDeployTeam_OllamaProvider_SetsOllamaURL(t *testing.T) {
	srv, mock := setupTestServer(t)

	// Create an OpenCode team with ollama model_provider.
	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:          "ollama-team",
		Provider:      "opencode",
		ModelProvider: "ollama",
		Agents: []CreateAgentInput{
			{Name: "leader", Role: "leader", SubAgentModel: "ollama/llama3.2"},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	// Set an OLLAMA_BASE_URL in settings so OpenCode auth is satisfied.
	srv.db.Create(&models.Settings{OrgID: team.OrgID, Key: "OLLAMA_BASE_URL", Value: "http://placeholder:11434"})

	rec := doRequest(srv, "POST", "/api/teams/"+team.ID+"/deploy", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	// Wait for async deploy to execute.
	time.Sleep(1 * time.Second)

	// Verify Ollama was connected to the team network.
	mock.mu.Lock()
	connectedCount := len(mock.ollamaConnected)
	var pulledModels []string
	pulledModels = append(pulledModels, mock.ollamaPulledModels...)
	var lastCfg *runtime.AgentConfig
	if mock.lastAgentConfig != nil {
		cfgCopy := *mock.lastAgentConfig
		lastCfg = &cfgCopy
	}
	mock.mu.Unlock()

	if connectedCount == 0 {
		t.Error("expected ollama to be connected to team network")
	}

	// Verify model was pulled (stripped of "ollama/" prefix).
	if len(pulledModels) == 0 {
		t.Error("expected ollama model to be pulled")
	} else if pulledModels[0] != "llama3.2" {
		t.Errorf("pulled model: got %q, want %q", pulledModels[0], "llama3.2")
	}

	// Verify OLLAMA_BASE_URL was set in agent config.
	if lastCfg != nil {
		if url, ok := lastCfg.Env["OLLAMA_BASE_URL"]; !ok || url != "http://agentcrew-ollama:11434" {
			t.Errorf("OLLAMA_BASE_URL: got %q, want %q", url, "http://agentcrew-ollama:11434")
		}
	}
}

func TestStopTeam_OllamaProvider_DecrementsRefCount(t *testing.T) {
	srv, mock := setupTestServer(t)

	// Create and manually set up an Ollama team as running.
	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:          "ollama-stop-team",
		Provider:      "opencode",
		ModelProvider: "ollama",
		Agents: []CreateAgentInput{
			{Name: "leader", Role: "leader"},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	// Simulate running state with Ollama SharedInfra ref count of 2.
	srv.db.Model(&team).Update("status", models.TeamStatusRunning)
	srv.db.Create(&models.SharedInfra{
		ID:           "infra-1",
		ResourceType: "ollama",
		ContainerID:  "ollama-cid",
		Status:       "running",
		RefCount:     2,
	})

	rec := doRequest(srv, "POST", "/api/teams/"+team.ID+"/stop", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	// Verify ollama was disconnected from the team network.
	if len(mock.ollamaDisconnected) == 0 {
		t.Error("expected ollama to be disconnected from team network")
	}

	// Verify ref count was decremented to 1 (not stopped since ref_count > 0).
	var infra models.SharedInfra
	srv.db.Where("resource_type = ?", "ollama").First(&infra)
	if infra.RefCount != 1 {
		t.Errorf("ref_count: got %d, want 1", infra.RefCount)
	}
	if mock.ollamaStopCalled {
		t.Error("ollama should NOT be stopped when ref_count > 0")
	}
}

func TestStopTeam_OllamaProvider_StopsWhenLastRef(t *testing.T) {
	srv, mock := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:          "ollama-last-ref",
		Provider:      "opencode",
		ModelProvider: "ollama",
		Agents: []CreateAgentInput{
			{Name: "leader", Role: "leader"},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.db.Model(&team).Update("status", models.TeamStatusRunning)
	srv.db.Create(&models.SharedInfra{
		ID:           "infra-2",
		ResourceType: "ollama",
		ContainerID:  "ollama-cid",
		Status:       "running",
		RefCount:     1,
	})

	rec := doRequest(srv, "POST", "/api/teams/"+team.ID+"/stop", nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	// With ref_count going to 0, ollama should be stopped.
	var infra models.SharedInfra
	srv.db.Where("resource_type = ?", "ollama").First(&infra)
	if infra.RefCount != 0 {
		t.Errorf("ref_count: got %d, want 0", infra.RefCount)
	}
	if !mock.ollamaStopCalled {
		t.Error("ollama should be stopped when ref_count reaches 0")
	}
}

func TestStopTeam_NonOllama_SkipsOllamaCleanup(t *testing.T) {
	srv, mock := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name: "non-ollama-team",
		Agents: []CreateAgentInput{
			{Name: "leader", Role: "leader"},
		},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	srv.db.Model(&team).Update("status", models.TeamStatusRunning)

	doRequest(srv, "POST", "/api/teams/"+team.ID+"/stop", nil)

	if len(mock.ollamaDisconnected) > 0 {
		t.Error("non-ollama team should not disconnect ollama")
	}
	if mock.ollamaStopCalled {
		t.Error("non-ollama team should not stop ollama")
	}
}
