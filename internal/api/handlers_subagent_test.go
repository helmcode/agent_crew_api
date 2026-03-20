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

// --- SubAgentInstructions field tests ---

func TestCreateAgent_SubAgentInstructions(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "instr-create-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	rec := doRequest(srv, "POST", "/api/teams/"+team.ID+"/agents", CreateAgentRequest{
		Name:                 "instr-agent",
		Role:                 "worker",
		SubAgentDescription:  "Short description",
		SubAgentInstructions: "Detailed instructions for this agent.\nMultiple lines.",
	})

	if rec.Code != 201 {
		t.Fatalf("status: got %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}

	var agent models.Agent
	parseJSON(t, rec, &agent)

	if agent.SubAgentInstructions != "Detailed instructions for this agent.\nMultiple lines." {
		t.Errorf("sub_agent_instructions: got %q", agent.SubAgentInstructions)
	}
	if agent.SubAgentDescription != "Short description" {
		t.Errorf("sub_agent_description: got %q", agent.SubAgentDescription)
	}
}

func TestUpdateAgent_SubAgentInstructions(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "instr-update-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	createRec := doRequest(srv, "POST", "/api/teams/"+team.ID+"/agents", CreateAgentRequest{
		Name:                 "upd-instr-agent",
		SubAgentInstructions: "Original instructions",
	})
	var created models.Agent
	parseJSON(t, createRec, &created)

	newInstr := "Updated instructions content"
	rec := doRequest(srv, "PUT", "/api/teams/"+team.ID+"/agents/"+created.ID, UpdateAgentRequest{
		SubAgentInstructions: &newInstr,
	})

	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	var agent models.Agent
	parseJSON(t, rec, &agent)

	if agent.SubAgentInstructions != "Updated instructions content" {
		t.Errorf("sub_agent_instructions: got %q", agent.SubAgentInstructions)
	}
}

func TestCreateTeam_WithSubAgentInstructions(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name: "team-instr-inline",
		Agents: []CreateAgentInput{
			{
				Name:                 "leader",
				Role:                 "leader",
				SystemPrompt:         "You lead the team",
			},
			{
				Name:                 "worker",
				Role:                 "worker",
				SubAgentDescription:  "Handles tasks",
				SubAgentInstructions: "Worker-specific instructions here.",
			},
		},
	})

	if rec.Code != 201 {
		t.Fatalf("status: got %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}

	var team models.Team
	parseJSON(t, rec, &team)

	var worker models.Agent
	for _, a := range team.Agents {
		if a.Role == "worker" {
			worker = a
			break
		}
	}

	if worker.SubAgentInstructions != "Worker-specific instructions here." {
		t.Errorf("sub_agent_instructions: got %q", worker.SubAgentInstructions)
	}
}

// --- Size validation tests ---

func TestCreateAgent_RejectsOversizedDescription(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "desc-size-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	// 2KB + 1 byte should be rejected.
	bigDesc := string(make([]byte, 2*1024+1))
	for i := range []byte(bigDesc) {
		_ = i
	}
	bigDescBytes := make([]byte, 2*1024+1)
	for i := range bigDescBytes {
		bigDescBytes[i] = 'a'
	}

	rec := doRequest(srv, "POST", "/api/teams/"+team.ID+"/agents", CreateAgentRequest{
		Name:                "oversized-desc-agent",
		SubAgentDescription: string(bigDescBytes),
	})

	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400 for oversized description\nbody: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateAgent_RejectsOversizedInstructions(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "instr-size-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	// 100KB + 1 byte should be rejected.
	bigInstr := make([]byte, 100*1024+1)
	for i := range bigInstr {
		bigInstr[i] = 'a'
	}

	rec := doRequest(srv, "POST", "/api/teams/"+team.ID+"/agents", CreateAgentRequest{
		Name:                 "oversized-instr-agent",
		SubAgentInstructions: string(bigInstr),
	})

	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400 for oversized instructions\nbody: %s", rec.Code, rec.Body.String())
	}
}

func TestUpdateAgent_RejectsOversizedDescription(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "upd-desc-size-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	createRec := doRequest(srv, "POST", "/api/teams/"+team.ID+"/agents", CreateAgentRequest{
		Name: "updsize-agent",
	})
	var agent models.Agent
	parseJSON(t, createRec, &agent)

	bigDesc := make([]byte, 2*1024+1)
	for i := range bigDesc {
		bigDesc[i] = 'a'
	}
	descStr := string(bigDesc)

	rec := doRequest(srv, "PUT", "/api/teams/"+team.ID+"/agents/"+agent.ID, UpdateAgentRequest{
		SubAgentDescription: &descStr,
	})

	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400 for oversized description update\nbody: %s", rec.Code, rec.Body.String())
	}
}

func TestUpdateAgent_RejectsOversizedInstructions(t *testing.T) {
	srv, _ := setupTestServer(t)

	teamRec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{Name: "upd-instr-size-team"})
	var team models.Team
	parseJSON(t, teamRec, &team)

	createRec := doRequest(srv, "POST", "/api/teams/"+team.ID+"/agents", CreateAgentRequest{
		Name: "updsize-instr-agent",
	})
	var agent models.Agent
	parseJSON(t, createRec, &agent)

	bigInstr := make([]byte, 100*1024+1)
	for i := range bigInstr {
		bigInstr[i] = 'a'
	}
	instrStr := string(bigInstr)

	rec := doRequest(srv, "PUT", "/api/teams/"+team.ID+"/agents/"+agent.ID, UpdateAgentRequest{
		SubAgentInstructions: &instrStr,
	})

	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400 for oversized instructions update\nbody: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateTeam_RejectsOversizedAgentDescription(t *testing.T) {
	srv, _ := setupTestServer(t)

	bigDesc := make([]byte, 2*1024+1)
	for i := range bigDesc {
		bigDesc[i] = 'a'
	}

	rec := doRequest(srv, "POST", "/api/teams", CreateTeamRequest{
		Name: "team-desc-size",
		Agents: []CreateAgentInput{
			{
				Name:                "big-desc-agent",
				SubAgentDescription: string(bigDesc),
			},
		},
	})

	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400 for oversized description in team create\nbody: %s", rec.Code, rec.Body.String())
	}
}
