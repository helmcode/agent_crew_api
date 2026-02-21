package api

import (
	"testing"

	"github.com/helmcode/agent-crew/internal/models"
)

// --- Sub-agent configuration fields ---

func TestCreateAgent_SubAgentFields(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "subagent-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	rec := doRequest(srv, "POST", "/api/teams/"+team.ID+"/agents", CreateAgentRequest{
		Name:                   "researcher",
		Role:                   "worker",
		SubAgentDescription:    "Delegate research tasks to this agent",
		SubAgentTools:          "Read, Grep, Glob",
		SubAgentModel:          "sonnet",
		SubAgentPermissionMode: "acceptEdits",
	})

	if rec.Code != 201 {
		t.Fatalf("status: got %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}

	var agent models.Agent
	parseJSON(t, rec, &agent)

	if agent.SubAgentDescription != "Delegate research tasks to this agent" {
		t.Errorf("sub_agent_description: got %q", agent.SubAgentDescription)
	}
	if agent.SubAgentTools != "Read, Grep, Glob" {
		t.Errorf("sub_agent_tools: got %q", agent.SubAgentTools)
	}
	if agent.SubAgentModel != "sonnet" {
		t.Errorf("sub_agent_model: got %q, want 'sonnet'", agent.SubAgentModel)
	}
	if agent.SubAgentPermissionMode != "acceptEdits" {
		t.Errorf("sub_agent_permission_mode: got %q, want 'acceptEdits'", agent.SubAgentPermissionMode)
	}
}

func TestCreateAgent_SubAgentModelDefault(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "default-model-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	rec := doRequest(srv, "POST", "/api/teams/"+team.ID+"/agents", CreateAgentRequest{
		Name: "no-model-agent",
	})

	if rec.Code != 201 {
		t.Fatalf("status: got %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}

	var agent models.Agent
	parseJSON(t, rec, &agent)

	if agent.SubAgentModel != "inherit" {
		t.Errorf("sub_agent_model default: got %q, want 'inherit'", agent.SubAgentModel)
	}
}

func TestUpdateAgent_SubAgentFields(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name:   "upd-subagent-team",
		Agents: []CreateAgentInput{{Name: "updatable-agent"}},
	})
	var team models.Team
	parseJSON(t, teamRec, &team)

	agentID := team.Agents[0].ID
	desc := "Updated delegation trigger"
	tools := "Read, Bash"
	model := "opus"
	perm := "acceptEdits"

	rec := doRequest(srv, "PUT", "/api/teams/"+team.ID+"/agents/"+agentID, UpdateAgentRequest{
		SubAgentDescription:    &desc,
		SubAgentTools:          &tools,
		SubAgentModel:          &model,
		SubAgentPermissionMode: &perm,
	})

	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	var agent models.Agent
	parseJSON(t, rec, &agent)

	if agent.SubAgentDescription != desc {
		t.Errorf("sub_agent_description: got %q, want %q", agent.SubAgentDescription, desc)
	}
	if agent.SubAgentTools != tools {
		t.Errorf("sub_agent_tools: got %q, want %q", agent.SubAgentTools, tools)
	}
	if agent.SubAgentModel != model {
		t.Errorf("sub_agent_model: got %q, want %q", agent.SubAgentModel, model)
	}
	if agent.SubAgentPermissionMode != perm {
		t.Errorf("sub_agent_permission_mode: got %q, want %q", agent.SubAgentPermissionMode, perm)
	}
}

func TestUpdateAgent_SubAgentPartialUpdate(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "partial-upd-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	// Create agent with sub-agent fields.
	createRec := doRequest(srv, "POST", "/api/teams/"+team.ID+"/agents", CreateAgentRequest{
		Name:                   "partial-agent",
		SubAgentDescription:    "Original description",
		SubAgentTools:          "Read, Grep",
		SubAgentModel:          "sonnet",
		SubAgentPermissionMode: "default",
	})
	var created models.Agent
	parseJSON(t, createRec, &created)

	// Update only the description — other fields should remain unchanged.
	newDesc := "Updated description only"
	rec := doRequest(srv, "PUT", "/api/teams/"+team.ID+"/agents/"+created.ID, UpdateAgentRequest{
		SubAgentDescription: &newDesc,
	})

	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	var agent models.Agent
	parseJSON(t, rec, &agent)

	if agent.SubAgentDescription != "Updated description only" {
		t.Errorf("sub_agent_description: got %q", agent.SubAgentDescription)
	}
	if agent.SubAgentTools != "Read, Grep" {
		t.Errorf("sub_agent_tools should be unchanged: got %q", agent.SubAgentTools)
	}
	if agent.SubAgentModel != "sonnet" {
		t.Errorf("sub_agent_model should be unchanged: got %q", agent.SubAgentModel)
	}
	if agent.SubAgentPermissionMode != "default" {
		t.Errorf("sub_agent_permission_mode should be unchanged: got %q", agent.SubAgentPermissionMode)
	}
}

func TestCreateTeam_WithSubAgentFields(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name: "team-subagent-inline",
		Agents: []CreateAgentInput{
			{
				Name:                   "leader",
				Role:                   "leader",
				SystemPrompt:           "You are the team leader",
			},
			{
				Name:                   "coder",
				Role:                   "worker",
				SubAgentDescription:    "Handles coding tasks",
				SubAgentTools:          "Read, Edit, Write, Bash",
				SubAgentModel:          "opus",
				SubAgentPermissionMode: "acceptEdits",
			},
		},
	})

	if rec.Code != 201 {
		t.Fatalf("status: got %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}

	var team models.Team
	parseJSON(t, rec, &team)

	if len(team.Agents) != 2 {
		t.Fatalf("agents: got %d, want 2", len(team.Agents))
	}

	// Find the worker agent.
	var worker models.Agent
	for _, a := range team.Agents {
		if a.Role == "worker" {
			worker = a
			break
		}
	}

	if worker.SubAgentDescription != "Handles coding tasks" {
		t.Errorf("sub_agent_description: got %q", worker.SubAgentDescription)
	}
	if worker.SubAgentTools != "Read, Edit, Write, Bash" {
		t.Errorf("sub_agent_tools: got %q", worker.SubAgentTools)
	}
	if worker.SubAgentModel != "opus" {
		t.Errorf("sub_agent_model: got %q, want 'opus'", worker.SubAgentModel)
	}
	if worker.SubAgentPermissionMode != "acceptEdits" {
		t.Errorf("sub_agent_permission_mode: got %q, want 'acceptEdits'", worker.SubAgentPermissionMode)
	}

	// Leader should have default model.
	var leader models.Agent
	for _, a := range team.Agents {
		if a.Role == "leader" {
			leader = a
			break
		}
	}
	if leader.SubAgentModel != "inherit" {
		t.Errorf("leader sub_agent_model default: got %q, want 'inherit'", leader.SubAgentModel)
	}
}

func TestGetTeam_ReturnsSubAgentFields(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Create team with an agent that has sub-agent fields.
	createRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name: "get-subagent-team",
		Agents: []CreateAgentInput{
			{
				Name:                "sub-agent",
				SubAgentDescription: "Test delegation",
				SubAgentTools:       "Read",
				SubAgentModel:       "haiku",
			},
		},
	})
	var created models.Team
	parseJSON(t, createRec, &created)

	// GET the team and verify sub-agent fields are included.
	rec := doRequest(srv, "GET", "/api/teams/"+created.ID, nil)
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var team models.Team
	parseJSON(t, rec, &team)

	if len(team.Agents) != 1 {
		t.Fatalf("agents: got %d, want 1", len(team.Agents))
	}

	agent := team.Agents[0]
	if agent.SubAgentDescription != "Test delegation" {
		t.Errorf("sub_agent_description: got %q", agent.SubAgentDescription)
	}
	if agent.SubAgentTools != "Read" {
		t.Errorf("sub_agent_tools: got %q", agent.SubAgentTools)
	}
	if agent.SubAgentModel != "haiku" {
		t.Errorf("sub_agent_model: got %q, want 'haiku'", agent.SubAgentModel)
	}
}
