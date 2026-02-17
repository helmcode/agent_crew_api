package models

import (
	"encoding/json"
	"testing"
)

func TestInitDB_InMemory(t *testing.T) {
	db, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("getting sql.DB: %v", err)
	}
	defer sqlDB.Close()

	if err := sqlDB.Ping(); err != nil {
		t.Fatalf("database ping failed: %v", err)
	}
}

func TestTeam_CRUD(t *testing.T) {
	db, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	team := Team{
		ID:          "team-001",
		Name:        "test-team",
		Description: "A test team",
		Status:      TeamStatusStopped,
		Runtime:     "docker",
	}

	// Create.
	if err := db.Create(&team).Error; err != nil {
		t.Fatalf("creating team: %v", err)
	}

	// Read.
	var found Team
	if err := db.First(&found, "id = ?", "team-001").Error; err != nil {
		t.Fatalf("finding team: %v", err)
	}
	if found.Name != "test-team" {
		t.Errorf("expected name 'test-team', got %q", found.Name)
	}

	// Update.
	if err := db.Model(&found).Update("status", TeamStatusRunning).Error; err != nil {
		t.Fatalf("updating team: %v", err)
	}
	db.First(&found, "id = ?", "team-001")
	if found.Status != TeamStatusRunning {
		t.Errorf("expected status 'running', got %q", found.Status)
	}

	// Delete.
	if err := db.Delete(&found).Error; err != nil {
		t.Fatalf("deleting team: %v", err)
	}
	result := db.First(&Team{}, "id = ?", "team-001")
	if result.Error == nil {
		t.Error("expected team to be deleted")
	}
}

func TestAgent_CRUD(t *testing.T) {
	db, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	team := Team{ID: "team-002", Name: "agent-test-team", Status: TeamStatusStopped, Runtime: "docker"}
	db.Create(&team)

	skills, _ := json.Marshal([]string{"terraform", "kubectl"})
	agent := Agent{
		ID:       "agent-001",
		TeamID:   "team-002",
		Name:     "devops-agent",
		Role:     AgentRoleWorker,
		Skills:   JSON(skills),
	}

	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("creating agent: %v", err)
	}

	var found Agent
	if err := db.First(&found, "id = ?", "agent-001").Error; err != nil {
		t.Fatalf("finding agent: %v", err)
	}
	if found.Name != "devops-agent" {
		t.Errorf("expected name 'devops-agent', got %q", found.Name)
	}

	// Verify JSON skills round-trip.
	var parsedSkills []string
	if err := json.Unmarshal(found.Skills, &parsedSkills); err != nil {
		t.Fatalf("unmarshaling skills: %v", err)
	}
	if len(parsedSkills) != 2 || parsedSkills[0] != "terraform" {
		t.Errorf("unexpected skills: %v", parsedSkills)
	}
}

func TestTeam_CascadeDeleteAgents(t *testing.T) {
	db, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	team := Team{ID: "team-003", Name: "cascade-team", Status: TeamStatusStopped, Runtime: "docker"}
	db.Create(&team)

	agent := Agent{ID: "agent-002", TeamID: "team-003", Name: "worker", Role: AgentRoleWorker}
	db.Create(&agent)

	// Delete team should cascade to agents.
	db.Select("Agents").Delete(&team)

	var count int64
	db.Model(&Agent{}).Where("team_id = ?", "team-003").Count(&count)
	if count != 0 {
		t.Errorf("expected 0 agents after cascade delete, got %d", count)
	}
}

func TestTaskLog_Create(t *testing.T) {
	db, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	payload, _ := json.Marshal(map[string]string{"task": "deploy"})
	log := TaskLog{
		ID:          "log-001",
		TeamID:      "team-001",
		MessageID:   "msg-001",
		FromAgent:   "leader",
		ToAgent:     "worker",
		MessageType: "task_assignment",
		Payload:     JSON(payload),
	}

	if err := db.Create(&log).Error; err != nil {
		t.Fatalf("creating task log: %v", err)
	}

	var found TaskLog
	db.First(&found, "id = ?", "log-001")
	if found.FromAgent != "leader" {
		t.Errorf("expected from_agent 'leader', got %q", found.FromAgent)
	}
}

func TestSettings_UniqueKey(t *testing.T) {
	db, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	s1 := Settings{Key: "api_port", Value: "8080"}
	if err := db.Create(&s1).Error; err != nil {
		t.Fatalf("creating setting: %v", err)
	}

	// Duplicate key should fail.
	s2 := Settings{Key: "api_port", Value: "9090"}
	if err := db.Create(&s2).Error; err == nil {
		t.Error("expected unique constraint error for duplicate key")
	}
}

func TestJSON_NilHandling(t *testing.T) {
	var j JSON

	// Scan nil.
	if err := j.Scan(nil); err != nil {
		t.Fatalf("Scan(nil): %v", err)
	}
	if string(j) != "null" {
		t.Errorf("expected 'null', got %q", string(j))
	}

	// MarshalJSON on empty.
	var empty JSON
	data, err := empty.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if string(data) != "null" {
		t.Errorf("expected 'null', got %q", string(data))
	}
}

func TestJSON_RoundTrip(t *testing.T) {
	input := `{"key":"value","nested":{"n":42}}`
	var j JSON
	if err := j.UnmarshalJSON([]byte(input)); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}

	val, err := j.Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	if val != input {
		t.Errorf("expected %q, got %q", input, val)
	}
}
