package api

import (
	"encoding/json"
	"testing"

	"github.com/helmcode/agent-crew/internal/models"
)

// parseSkills unmarshals a models.JSON skills field into a string slice.
func parseSkills(t *testing.T, raw models.JSON) []string {
	t.Helper()
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var skills []string
	if err := json.Unmarshal(raw, &skills); err != nil {
		t.Fatalf("failed to unmarshal sub_agent_skills: %v", err)
	}
	return skills
}

// hasSkill reports whether skill is present in the slice.
func hasSkill(skills []string, skill string) bool {
	for _, s := range skills {
		if s == skill {
			return true
		}
	}
	return false
}

// --- Sub-agent configuration fields ---

func TestCreateAgent_SubAgentFields(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "subagent-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	rec := doRequest(srv, "POST", "/api/teams/"+team.ID+"/agents", CreateAgentRequest{
		Name:                "researcher",
		Role:                "worker",
		SubAgentDescription: "Delegate research tasks to this agent",
		SubAgentSkills:      []string{"Read", "Grep", "Glob"},
		SubAgentModel:       "sonnet",
	})

	if rec.Code != 201 {
		t.Fatalf("status: got %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}

	var agent models.Agent
	parseJSON(t, rec, &agent)

	if agent.SubAgentDescription != "Delegate research tasks to this agent" {
		t.Errorf("sub_agent_description: got %q", agent.SubAgentDescription)
	}
	skills := parseSkills(t, agent.SubAgentSkills)
	if !hasSkill(skills, "Read") {
		t.Errorf("sub_agent_skills missing 'Read': got %v", skills)
	}
	if !hasSkill(skills, "Grep") {
		t.Errorf("sub_agent_skills missing 'Grep': got %v", skills)
	}
	if !hasSkill(skills, "Glob") {
		t.Errorf("sub_agent_skills missing 'Glob': got %v", skills)
	}
	if agent.SubAgentModel != "sonnet" {
		t.Errorf("sub_agent_model: got %q, want 'sonnet'", agent.SubAgentModel)
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
	model := "opus"

	rec := doRequest(srv, "PUT", "/api/teams/"+team.ID+"/agents/"+agentID, UpdateAgentRequest{
		SubAgentDescription: &desc,
		SubAgentSkills:      []string{"Read", "Bash"},
		SubAgentModel:       &model,
	})

	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	var agent models.Agent
	parseJSON(t, rec, &agent)

	if agent.SubAgentDescription != desc {
		t.Errorf("sub_agent_description: got %q, want %q", agent.SubAgentDescription, desc)
	}
	skills := parseSkills(t, agent.SubAgentSkills)
	if !hasSkill(skills, "Read") {
		t.Errorf("sub_agent_skills missing 'Read': got %v", skills)
	}
	if !hasSkill(skills, "Bash") {
		t.Errorf("sub_agent_skills missing 'Bash': got %v", skills)
	}
	if agent.SubAgentModel != model {
		t.Errorf("sub_agent_model: got %q, want %q", agent.SubAgentModel, model)
	}
}

func TestUpdateAgent_SubAgentPartialUpdate(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "partial-upd-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	// Create agent with sub-agent fields.
	createRec := doRequest(srv, "POST", "/api/teams/"+team.ID+"/agents", CreateAgentRequest{
		Name:                "partial-agent",
		SubAgentDescription: "Original description",
		SubAgentSkills:      []string{"Read", "Grep"},
		SubAgentModel:       "sonnet",
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
	skills := parseSkills(t, agent.SubAgentSkills)
	if !hasSkill(skills, "Read") {
		t.Errorf("sub_agent_skills 'Read' should be unchanged: got %v", skills)
	}
	if !hasSkill(skills, "Grep") {
		t.Errorf("sub_agent_skills 'Grep' should be unchanged: got %v", skills)
	}
	if agent.SubAgentModel != "sonnet" {
		t.Errorf("sub_agent_model should be unchanged: got %q", agent.SubAgentModel)
	}
}

func TestCreateTeam_WithSubAgentFields(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name: "team-subagent-inline",
		Agents: []CreateAgentInput{
			{
				Name:         "leader",
				Role:         "leader",
				SystemPrompt: "You are the team leader",
			},
			{
				Name:                "coder",
				Role:                "worker",
				SubAgentDescription: "Handles coding tasks",
				SubAgentSkills:      []string{"Read", "Edit", "Write", "Bash"},
				SubAgentModel:       "opus",
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
	skills := parseSkills(t, worker.SubAgentSkills)
	if !hasSkill(skills, "Read") {
		t.Errorf("sub_agent_skills missing 'Read': got %v", skills)
	}
	if !hasSkill(skills, "Edit") {
		t.Errorf("sub_agent_skills missing 'Edit': got %v", skills)
	}
	if worker.SubAgentModel != "opus" {
		t.Errorf("sub_agent_model: got %q, want 'opus'", worker.SubAgentModel)
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
				SubAgentSkills:      []string{"Read"},
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
	skills := parseSkills(t, agent.SubAgentSkills)
	if !hasSkill(skills, "Read") {
		t.Errorf("sub_agent_skills missing 'Read': got %v", skills)
	}
	if agent.SubAgentModel != "haiku" {
		t.Errorf("sub_agent_model: got %q, want 'haiku'", agent.SubAgentModel)
	}
}
