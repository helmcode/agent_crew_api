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

// Organization represents a tenant in the multi-tenant system.
type Organization struct {
	ID        string    `gorm:"primaryKey;size:36" json:"id"`
	Name      string    `gorm:"not null;size:255" json:"name"`
	Slug      string    `gorm:"uniqueIndex;not null;size:255" json:"slug"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// User represents a user belonging to an organization.
type User struct {
	ID                 string    `gorm:"primaryKey;size:36" json:"id"`
	OrgID              string    `gorm:"not null;size:36;index" json:"org_id"`
	Email              string    `gorm:"uniqueIndex;not null;size:255" json:"email"`
	Name               string    `gorm:"not null;size:255" json:"name"`
	PasswordHash       string    `gorm:"size:255" json:"-"`
	IsOwner            bool      `gorm:"default:false" json:"is_owner"`
	Role               string    `gorm:"not null;size:20;default:'member'" json:"role"`
	MustChangePassword bool      `gorm:"default:false" json:"must_change_password"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	Organization       Organization `gorm:"foreignKey:OrgID;constraint:OnDelete:CASCADE" json:"-"`
}

// Valid user roles.
const (
	UserRoleAdmin  = "admin"
	UserRoleMember = "member"
)

// Invite represents an invitation to join an organization.
type Invite struct {
	ID             string     `gorm:"primaryKey;size:36" json:"id"`
	OrgID          string     `gorm:"not null;size:36;index" json:"org_id"`
	Token          string     `gorm:"uniqueIndex;not null;size:64" json:"-"`
	EncryptedToken string     `gorm:"type:text" json:"-"`
	Email          string     `gorm:"size:255" json:"email,omitempty"`
	ExpiresAt      time.Time  `json:"expires_at"`
	UsedAt         *time.Time `json:"used_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	Organization   Organization `gorm:"foreignKey:OrgID;constraint:OnDelete:CASCADE" json:"-"`
}

// Team represents an agent team managed by the orchestrator.
type Team struct {
	ID            string    `gorm:"primaryKey;size:36" json:"id"`
	OrgID         string    `gorm:"size:36;uniqueIndex:idx_team_org_name" json:"org_id"`
	Name          string    `gorm:"not null;size:255;uniqueIndex:idx_team_org_name" json:"name"`
	Description   string    `gorm:"size:1024" json:"description"`
	Status        string    `gorm:"not null;size:50;default:stopped" json:"status"`
	StatusMessage string    `gorm:"type:text" json:"status_message"`
	Runtime       string    `gorm:"not null;size:50;default:docker" json:"runtime"`
	Provider      string    `gorm:"type:varchar(50);default:'claude'" json:"provider"`
	WorkspacePath string    `gorm:"size:512" json:"workspace_path"`
	AgentImage    string    `gorm:"size:512" json:"agent_image"`
	McpServers    JSON      `gorm:"type:text" json:"mcp_servers"`
	McpStatuses   JSON      `gorm:"type:text" json:"mcp_statuses"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	Agents        []Agent   `gorm:"foreignKey:TeamID;constraint:OnDelete:CASCADE" json:"agents,omitempty"`
}

// Agent represents a single AI agent within a team.
type Agent struct {
	ID              string    `gorm:"primaryKey;size:36" json:"id"`
	OrgID           string    `gorm:"size:36;index" json:"org_id"`
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
	OrgID     string    `gorm:"size:36;uniqueIndex:idx_settings_org_key" json:"org_id"`
	Key       string    `gorm:"not null;size:255;uniqueIndex:idx_settings_org_key" json:"key"`
	Value     string    `gorm:"type:text" json:"value"`
	IsSecret  bool      `gorm:"default:false" json:"is_secret"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Schedule represents a recurring task that deploys a team and sends a prompt on a cron schedule.
type Schedule struct {
	ID             string     `gorm:"primaryKey;size:36" json:"id"`
	OrgID          string     `gorm:"size:36;index" json:"org_id"`
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

// Webhook represents an HTTP webhook endpoint that triggers a team execution.
type Webhook struct {
	ID              string       `gorm:"primaryKey;size:36" json:"id"`
	OrgID           string       `gorm:"size:36;index" json:"org_id"`
	Name            string       `gorm:"not null;size:255" json:"name"`
	TeamID          string       `gorm:"not null;size:36" json:"team_id"`
	PromptTemplate  string       `gorm:"type:text;not null" json:"prompt_template"`
	SecretTokenHash string       `gorm:"not null;size:64" json:"-"`
	SecretPrefix    string       `gorm:"size:12" json:"secret_prefix"`
	Enabled         bool         `gorm:"default:true" json:"enabled"`
	TimeoutSeconds  int          `gorm:"default:3600" json:"timeout_seconds"`
	MaxConcurrent   int          `gorm:"default:1" json:"max_concurrent"`
	LastTriggeredAt *time.Time   `json:"last_triggered_at"`
	Status          string       `gorm:"size:20;default:'idle'" json:"status"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
	Team            Team         `gorm:"foreignKey:TeamID;constraint:OnDelete:CASCADE" json:"team,omitempty"`
	Runs            []WebhookRun `gorm:"foreignKey:WebhookID;constraint:OnDelete:CASCADE" json:"runs,omitempty"`
}

// WebhookRun records a single execution triggered by a webhook.
type WebhookRun struct {
	ID               string     `gorm:"primaryKey;size:36" json:"id"`
	WebhookID        string     `gorm:"not null;size:36;index" json:"webhook_id"`
	StartedAt        time.Time  `json:"started_at"`
	FinishedAt       *time.Time `json:"finished_at"`
	Status           string     `gorm:"size:20;default:'running'" json:"status"`
	Error            string     `gorm:"type:text" json:"error"`
	PromptSent       string     `gorm:"type:text" json:"prompt_sent"`
	ResponseReceived string     `gorm:"type:text" json:"response_received"`
	RequestPayload   string     `gorm:"type:text" json:"request_payload"`
	CallerIP         string     `gorm:"size:45" json:"caller_ip"`
}

// Valid webhook statuses.
const (
	WebhookStatusIdle    = "idle"
	WebhookStatusRunning = "running"
)

// Valid webhook run statuses.
const (
	WebhookRunStatusRunning = "running"
	WebhookRunStatusSuccess = "success"
	WebhookRunStatusFailed  = "failed"
	WebhookRunStatusTimeout = "timeout"
)

// Valid providers.
const (
	ProviderClaude   = "claude"
	ProviderOpenCode = "opencode"
)

// PostAction defines a reusable HTTP action that fires after a trigger completes.
type PostAction struct {
	ID             string    `gorm:"primaryKey;size:36" json:"id"`
	OrgID          string    `gorm:"size:36;index" json:"org_id"`
	Name           string    `gorm:"not null;size:255" json:"name"`
	Description    string    `gorm:"size:1024" json:"description"`
	Method         string    `gorm:"not null;size:10" json:"method"`
	URL            string    `gorm:"not null;type:text" json:"url"`
	Headers        JSON      `gorm:"type:text" json:"headers"`
	BodyTemplate   string    `gorm:"type:text" json:"body_template"`
	AuthType       string    `gorm:"size:20;default:'none'" json:"auth_type"`
	AuthConfig     JSON      `gorm:"type:text" json:"auth_config"`
	TimeoutSeconds int       `gorm:"default:30" json:"timeout_seconds"`
	RetryCount     int       `gorm:"default:0" json:"retry_count"`
	Enabled        bool      `gorm:"default:true" json:"enabled"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	Bindings       []PostActionBinding `gorm:"foreignKey:PostActionID;constraint:OnDelete:CASCADE" json:"bindings,omitempty"`
}

// PostActionBinding links a PostAction to a specific trigger (webhook or schedule).
type PostActionBinding struct {
	ID           string    `gorm:"primaryKey;size:36" json:"id"`
	PostActionID string    `gorm:"not null;size:36;index;uniqueIndex:idx_binding_unique" json:"post_action_id"`
	TriggerType  string    `gorm:"not null;size:20;uniqueIndex:idx_binding_unique" json:"trigger_type"`
	TriggerID    string    `gorm:"not null;size:36;index;uniqueIndex:idx_binding_unique" json:"trigger_id"`
	TriggerOn    string    `gorm:"not null;size:20;uniqueIndex:idx_binding_unique" json:"trigger_on"`
	BodyOverride string    `gorm:"type:text" json:"body_override,omitempty"`
	Enabled      bool      `gorm:"default:true" json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
	PostAction   PostAction `gorm:"foreignKey:PostActionID" json:"post_action,omitempty"`
}

// PostActionRun records a single execution of a post-action.
type PostActionRun struct {
	ID           string     `gorm:"primaryKey;size:36" json:"id"`
	PostActionID string     `gorm:"not null;size:36;index" json:"post_action_id"`
	BindingID    string     `gorm:"not null;size:36;index" json:"binding_id"`
	SourceType   string     `gorm:"size:20" json:"source_type"`
	SourceRunID  string     `gorm:"size:36" json:"source_run_id"`
	TriggeredAt  time.Time  `json:"triggered_at"`
	CompletedAt  *time.Time `json:"completed_at"`
	Status       string     `gorm:"size:20" json:"status"`
	StatusCode   int        `json:"status_code"`
	ResponseBody string     `gorm:"type:text" json:"response_body"`
	Error        string     `gorm:"type:text" json:"error"`
	RequestSent  string     `gorm:"type:text" json:"request_sent"`
}

// Valid HTTP methods for PostAction.
const (
	PostActionMethodGET    = "GET"
	PostActionMethodPOST   = "POST"
	PostActionMethodPUT    = "PUT"
	PostActionMethodPATCH  = "PATCH"
	PostActionMethodDELETE = "DELETE"
)

// Valid auth types for PostAction.
const (
	PostActionAuthNone   = "none"
	PostActionAuthBearer = "bearer"
	PostActionAuthBasic  = "basic"
	PostActionAuthHeader = "header"
)

// Valid trigger types for PostActionBinding.
const (
	PostActionTriggerWebhook  = "webhook"
	PostActionTriggerSchedule = "schedule"
)

// Valid trigger-on conditions for PostActionBinding.
const (
	PostActionTriggerOnSuccess = "success"
	PostActionTriggerOnFailure = "failure"
	PostActionTriggerOnAny     = "any"
)

// Valid statuses for PostActionRun.
const (
	PostActionRunStatusSuccess  = "success"
	PostActionRunStatusFailed   = "failed"
	PostActionRunStatusRetrying = "retrying"
)
