// Package models defines GORM models and SQLite database setup for AgentCrew.
package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// JSON is a custom type that stores JSON data as a string in SQLite.
type JSON json.RawMessage

func (j JSON) Value() (driver.Value, error) {
	if len(j) == 0 {
		return "null", nil
	}
	return string(j), nil
}

func (j *JSON) Scan(value interface{}) error {
	if value == nil {
		*j = JSON("null")
		return nil
	}
	switch v := value.(type) {
	case string:
		*j = JSON(v)
	case []byte:
		*j = JSON(v)
	default:
		return fmt.Errorf("unsupported type for JSON: %T", value)
	}
	return nil
}

func (j JSON) MarshalJSON() ([]byte, error) {
	if len(j) == 0 {
		return []byte("null"), nil
	}
	return []byte(j), nil
}

func (j *JSON) UnmarshalJSON(data []byte) error {
	*j = JSON(data)
	return nil
}

// Team represents an agent team managed by the orchestrator.
type Team struct {
	ID            string    `gorm:"primaryKey;size:36" json:"id"`
	Name          string    `gorm:"uniqueIndex;not null;size:255" json:"name"`
	Description   string    `gorm:"size:1024" json:"description"`
	Status        string    `gorm:"not null;size:50;default:stopped" json:"status"`
	Runtime       string    `gorm:"not null;size:50;default:docker" json:"runtime"`
	WorkspacePath string    `gorm:"size:512" json:"workspace_path"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	Agents        []Agent   `gorm:"foreignKey:TeamID;constraint:OnDelete:CASCADE" json:"agents,omitempty"`
}

// Agent represents a single AI agent within a team.
type Agent struct {
	ID              string    `gorm:"primaryKey;size:36" json:"id"`
	TeamID          string    `gorm:"not null;size:36;index" json:"team_id"`
	Name            string    `gorm:"not null;size:255" json:"name"`
	Role            string    `gorm:"not null;size:50;default:worker" json:"role"`
	Specialty       string    `gorm:"size:512" json:"specialty"`
	SystemPrompt    string    `gorm:"type:text" json:"system_prompt"`
	ClaudeMD        string    `gorm:"type:text" json:"claude_md"`
	Skills          JSON      `gorm:"type:text" json:"skills"`
	Permissions     JSON      `gorm:"type:text" json:"permissions"`
	Resources       JSON      `gorm:"type:text" json:"resources"`
	ContainerID     string    `gorm:"size:128" json:"container_id"`
	ContainerStatus string    `gorm:"size:50;default:stopped" json:"container_status"`

	// Sub-agent configuration fields for .claude/agents/{name}.md frontmatter.
	// These are only used for non-leader agents in the native sub-agent architecture.
	SubAgentDescription string `gorm:"type:text" json:"sub_agent_description"`
	SubAgentModel       string `gorm:"size:50;default:inherit" json:"sub_agent_model"`
	SubAgentSkills      JSON   `gorm:"type:text" json:"sub_agent_skills"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TaskLog records inter-agent messages for auditing and replay.
type TaskLog struct {
	ID          string    `gorm:"primaryKey;size:36" json:"id"`
	TeamID      string    `gorm:"not null;size:36;index:idx_tasklog_team_created" json:"team_id"`
	MessageID   string    `gorm:"size:36;index" json:"message_id"`
	FromAgent   string    `gorm:"size:255" json:"from_agent"`
	ToAgent     string    `gorm:"size:255" json:"to_agent"`
	MessageType string    `gorm:"size:50" json:"message_type"`
	Payload     JSON      `gorm:"type:text" json:"payload"`
	CreatedAt   time.Time `gorm:"index:idx_tasklog_team_created" json:"created_at"`
}

// Settings stores application-level key-value configuration.
type Settings struct {
	ID        uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Key       string    `gorm:"uniqueIndex;not null;size:255" json:"key"`
	Value     string    `gorm:"type:text" json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Valid team statuses.
const (
	TeamStatusStopped   = "stopped"
	TeamStatusRunning   = "running"
	TeamStatusError     = "error"
	TeamStatusDeploying = "deploying"
)

// Valid agent roles.
const (
	AgentRoleLeader = "leader"
	AgentRoleWorker = "worker"
)

// Valid container statuses.
const (
	ContainerStatusStopped = "stopped"
	ContainerStatusRunning = "running"
	ContainerStatusError   = "error"
)
