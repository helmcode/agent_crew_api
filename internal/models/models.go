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
	Provider      string    `gorm:"type:varchar(50);default:'claude'" json:"provider"`
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
	InstructionsMD  string    `gorm:"column:instructions_md;type:text" json:"instructions_md"`
	Skills          JSON      `gorm:"type:text" json:"skills"`
	Permissions     JSON      `gorm:"type:text" json:"permissions"`
	Resources       JSON      `gorm:"type:text" json:"resources"`
	ContainerID     string    `gorm:"size:128" json:"container_id"`
	ContainerStatus string    `gorm:"size:50;default:stopped" json:"container_status"`

	// Sub-agent configuration fields for .claude/agents/{name}.md frontmatter.
	// These are only used for non-leader agents in the native sub-agent architecture.
	SubAgentDescription string `gorm:"type:text" json:"sub_agent_description"`
	SubAgentModel       string `gorm:"size:255;default:inherit" json:"sub_agent_model"`
	SubAgentSkills      JSON   `gorm:"type:text" json:"sub_agent_skills"`

	// SkillStatuses stores per-skill installation results reported by the sidecar.
	SkillStatuses JSON `gorm:"type:text" json:"skill_statuses"`

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
	IsSecret  bool      `gorm:"default:false" json:"is_secret"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Schedule represents a recurring task that deploys a team and sends a prompt on a cron schedule.
type Schedule struct {
	ID             string     `gorm:"primaryKey;size:36" json:"id"`
	Name           string     `gorm:"not null;size:255" json:"name"`
	TeamID         string     `gorm:"not null;size:36" json:"team_id"`
	Prompt         string     `gorm:"type:text;not null" json:"prompt"`
	CronExpression string     `gorm:"not null;size:100" json:"cron_expression"`
	Timezone       string     `gorm:"not null;size:50;default:'UTC'" json:"timezone"`
	Enabled        bool       `gorm:"default:true" json:"enabled"`
	LastRunAt      *time.Time `json:"last_run_at"`
	NextRunAt      *time.Time `json:"next_run_at"`
	// Status: idle | running | error
	Status    string    `gorm:"size:20;default:'idle'" json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Team      Team      `gorm:"foreignKey:TeamID;constraint:OnDelete:CASCADE" json:"team,omitempty"`
	Runs      []ScheduleRun `gorm:"foreignKey:ScheduleID;constraint:OnDelete:CASCADE" json:"runs,omitempty"`
}

// ScheduleRun records a single execution of a schedule.
type ScheduleRun struct {
	ID               string     `gorm:"primaryKey;size:36" json:"id"`
	ScheduleID       string     `gorm:"not null;size:36;index" json:"schedule_id"`
	TeamDeploymentID string     `gorm:"size:36" json:"team_deployment_id"`
	StartedAt        time.Time  `json:"started_at"`
	FinishedAt       *time.Time `json:"finished_at"`
	// Status: running | success | failed | timeout
	Status           string `gorm:"size:20;default:'running'" json:"status"`
	Error            string `gorm:"type:text" json:"error"`
	PromptSent       string `gorm:"type:text" json:"prompt_sent"`
	ResponseReceived string `gorm:"type:text" json:"response_received"`
	Schedule         Schedule `gorm:"foreignKey:ScheduleID" json:"-"`
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

// Valid schedule statuses.
const (
	ScheduleStatusIdle    = "idle"
	ScheduleStatusRunning = "running"
	ScheduleStatusError   = "error"
)

// Valid schedule run statuses.
const (
	ScheduleRunStatusRunning = "running"
	ScheduleRunStatusSuccess = "success"
	ScheduleRunStatusFailed  = "failed"
	ScheduleRunStatusTimeout = "timeout"
)

// Valid providers.
const (
	ProviderClaude   = "claude"
	ProviderOpenCode = "opencode"
)
