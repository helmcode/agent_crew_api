// Package api implements the Fiber HTTP API for the AgentCrew orchestrator.
//
// Team and agent names accept any human-friendly string (e.g. "My Team", "Test").
// When names are used for Docker/K8s infrastructure resources, they are sanitized
// internally via SanitizeName to produce a safe slug.
package api

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/helmcode/agent-crew/internal/models"
)

// CreateTeamRequest is the payload for POST /api/teams.
type CreateTeamRequest struct {
	Name          string              `json:"name" validate:"required"`
	Description   string              `json:"description"`
	Runtime       string              `json:"runtime"`
	Provider      string              `json:"provider"`
	WorkspacePath string              `json:"workspace_path"`
	AgentImage    string              `json:"agent_image"`
	Agents        []CreateAgentInput  `json:"agents"`
	McpServers    interface{}         `json:"mcp_servers"`
}

// UpdateTeamRequest is the payload for PUT /api/teams/:id.
type UpdateTeamRequest struct {
	Name          *string     `json:"name"`
	Description   *string     `json:"description"`
	Provider      *string     `json:"provider"`
	WorkspacePath *string     `json:"workspace_path"`
	AgentImage    *string     `json:"agent_image"`
	McpServers    interface{} `json:"mcp_servers"`
}

// CreateAgentInput defines an agent to be created alongside a team.
type CreateAgentInput struct {
	Name                string      `json:"name" validate:"required"`
	Role                string      `json:"role"`
	Specialty           string      `json:"specialty"`
	SystemPrompt        string      `json:"system_prompt"`
	InstructionsMD      string      `json:"instructions_md"`
	ClaudeMD            string      `json:"claude_md"` // Deprecated: backward compat alias for instructions_md
	Skills              interface{} `json:"skills"`
	Permissions         interface{} `json:"permissions"`
	Resources           interface{} `json:"resources"`
	SubAgentDescription  string      `json:"sub_agent_description"`
	SubAgentInstructions string      `json:"sub_agent_instructions"`
	SubAgentModel        string      `json:"sub_agent_model"`
	SubAgentSkills       interface{} `json:"sub_agent_skills"`
}

// CreateAgentRequest is the payload for POST /api/teams/:id/agents.
type CreateAgentRequest struct {
	Name                string      `json:"name" validate:"required"`
	Role                string      `json:"role"`
	Specialty           string      `json:"specialty"`
	SystemPrompt        string      `json:"system_prompt"`
	InstructionsMD      string      `json:"instructions_md"`
	ClaudeMD            string      `json:"claude_md"` // Deprecated: backward compat alias for instructions_md
	Skills              interface{} `json:"skills"`
	Permissions         interface{} `json:"permissions"`
	Resources           interface{} `json:"resources"`
	SubAgentDescription  string      `json:"sub_agent_description"`
	SubAgentInstructions string      `json:"sub_agent_instructions"`
	SubAgentModel        string      `json:"sub_agent_model"`
	SubAgentSkills       interface{} `json:"sub_agent_skills"`
}

// UpdateAgentRequest is the payload for PUT /api/teams/:id/agents/:agentId.
type UpdateAgentRequest struct {
	Name                *string     `json:"name"`
	Role                *string     `json:"role"`
	Specialty           *string     `json:"specialty"`
	SystemPrompt        *string     `json:"system_prompt"`
	InstructionsMD      *string     `json:"instructions_md"`
	ClaudeMD            *string     `json:"claude_md"` // Deprecated: backward compat alias for instructions_md
	Skills              interface{} `json:"skills"`
	Permissions         interface{} `json:"permissions"`
	Resources           interface{} `json:"resources"`
	SubAgentDescription  *string     `json:"sub_agent_description"`
	SubAgentInstructions *string     `json:"sub_agent_instructions"`
	SubAgentModel        *string     `json:"sub_agent_model"`
	SubAgentSkills       interface{} `json:"sub_agent_skills"`
}

// ChatRequest is the payload for POST /api/teams/:id/chat.
type ChatRequest struct {
	Message string `json:"message" validate:"required"`
}

// UpdateSettingsRequest is the payload for PUT /api/settings.
type UpdateSettingsRequest struct {
	Key      string `json:"key" validate:"required"`
	Value    string `json:"value"`
	IsSecret *bool  `json:"is_secret"`
}

// ErrorResponse is a standard error response.
type ErrorResponse struct {
	Error   string `json:"error"`
	Details string `json:"details,omitempty"`
}

// CreateScheduleRequest is the payload for POST /api/schedules.
type CreateScheduleRequest struct {
	Name           string `json:"name" validate:"required"`
	TeamID         string `json:"team_id" validate:"required"`
	Prompt         string `json:"prompt" validate:"required"`
	CronExpression string `json:"cron_expression" validate:"required"`
	Timezone       string `json:"timezone"`
	Enabled        *bool  `json:"enabled"`
}

// UpdateScheduleRequest is the payload for PUT /api/schedules/:id.
type UpdateScheduleRequest struct {
	Name           *string `json:"name"`
	TeamID         *string `json:"team_id"`
	Prompt         *string `json:"prompt"`
	CronExpression *string `json:"cron_expression"`
	Timezone       *string `json:"timezone"`
	Enabled        *bool   `json:"enabled"`
}

// CreateWebhookRequest is the payload for POST /api/webhooks.
type CreateWebhookRequest struct {
	Name           string `json:"name" validate:"required"`
	TeamID         string `json:"team_id" validate:"required"`
	PromptTemplate string `json:"prompt_template" validate:"required"`
	TimeoutSeconds *int   `json:"timeout_seconds"`
	MaxConcurrent  *int   `json:"max_concurrent"`
	Enabled        *bool  `json:"enabled"`
}

// UpdateWebhookRequest is the payload for PUT /api/webhooks/:id.
type UpdateWebhookRequest struct {
	Name           *string `json:"name"`
	PromptTemplate *string `json:"prompt_template"`
	TimeoutSeconds *int    `json:"timeout_seconds"`
	MaxConcurrent  *int    `json:"max_concurrent"`
	Enabled        *bool   `json:"enabled"`
}

// TriggerWebhookRequest is the payload for POST /webhook/trigger/:token.
type TriggerWebhookRequest struct {
	Variables map[string]string `json:"variables"`
}

// TriggerWebhookResponse is the response for a webhook trigger.
type TriggerWebhookResponse struct {
	RunID      string `json:"run_id"`
	Status     string `json:"status"`
	Response   string `json:"response,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
}

// InstallSkillRequest is the payload for POST /api/teams/:id/agents/:agentId/skills/install.
type InstallSkillRequest struct {
	RepoURL   string `json:"repo_url"`
	SkillName string `json:"skill_name"`
}

// InstallSkillResponse is the response for a skill installation request.
type InstallSkillResponse struct {
	Output        string              `json:"output"`
	Error         string              `json:"error,omitempty"`
	UpdatedSkills []map[string]string `json:"updated_skills,omitempty"`
}

// McpConfigResponse returns the raw MCP config file from a running container.
type McpConfigResponse struct {
	Content  string `json:"content"`
	Path     string `json:"path"`
	Provider string `json:"provider"`
}

// UpdateMcpConfigRequest is the payload for PUT /api/teams/:id/mcp.
type UpdateMcpConfigRequest struct {
	Content string `json:"content"`
}

// AddMcpServerRequest is the payload for POST /api/teams/:id/mcp/servers.
type AddMcpServerRequest struct {
	Name      string            `json:"name" validate:"required"`
	Transport string            `json:"transport" validate:"required"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

// InstructionsResponse is the response for GET/PUT agent instructions.
type InstructionsResponse struct {
	Content string `json:"content"`
	Path    string `json:"path"`
}

// UpdateInstructionsRequest is the payload for PUT /api/teams/:id/agents/:agentId/instructions.
type UpdateInstructionsRequest struct {
	Content string `json:"content"`
}

// invalidSlugChars matches any character that is not lowercase alphanumeric, hyphen, or underscore.
var invalidSlugChars = regexp.MustCompile(`[^a-z0-9_-]`)

// validateName checks that a name is a non-empty string of at most 255 characters.
// Any human-friendly name is accepted; infrastructure-safe slugs are produced by SanitizeName.
func validateName(name string) error {
	if len(strings.TrimSpace(name)) == 0 {
		return fmt.Errorf("name is required")
	}
	if len(name) > 255 {
		return fmt.Errorf("name must be at most 255 characters")
	}
	return nil
}

// SanitizeName converts a human-friendly display name into a Docker/K8s-safe slug.
// It lowercases the string, replaces spaces with hyphens, strips invalid characters,
// collapses consecutive hyphens, trims leading/trailing hyphens, and truncates to 62 chars.
func SanitizeName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, " ", "-")
	s = invalidSlugChars.ReplaceAllString(s, "")
	// Collapse consecutive hyphens.
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-")
	if len(s) > 62 {
		s = s[:62]
		s = strings.TrimRight(s, "-")
	}
	if s == "" {
		s = "team"
	}
	return s
}

// validSkillNameRe matches safe skill names: alphanumeric, hyphens, underscores, dots, @, forward slashes.
var validSkillNameRe = regexp.MustCompile(`^[a-zA-Z0-9@/_.-]+$`)

// validateSubAgentSkills validates the SubAgentSkills field. It accepts:
//   - []SkillConfig objects ({repo_url, skill_name}) — validated as installable repo skills
//   - []string — can be plain tool names ("Read", "Bash") or legacy "repo:skill" format
//
// Returns an error if any entry contains injection-unsafe characters.
func validateSubAgentSkills(raw interface{}) error {
	if raw == nil {
		return nil
	}

	// Marshal to JSON for uniform parsing.
	data, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("invalid sub_agent_skills: %w", err)
	}

	// Ignore null or empty array.
	s := strings.TrimSpace(string(data))
	if s == "null" || s == "[]" {
		return nil
	}

	// Try as array of SkillConfig objects (has repo_url and skill_name fields).
	var configs []struct {
		RepoURL   string `json:"repo_url"`
		SkillName string `json:"skill_name"`
	}
	if err := json.Unmarshal(data, &configs); err == nil && len(configs) > 0 {
		// Check if these are actual SkillConfig objects (have non-empty repo_url).
		hasRepoURL := false
		for _, cfg := range configs {
			if cfg.RepoURL != "" {
				hasRepoURL = true
				break
			}
		}
		if hasRepoURL {
			for i, cfg := range configs {
				if err := validateSingleSkillConfig(cfg.RepoURL, cfg.SkillName); err != nil {
					return fmt.Errorf("sub_agent_skills[%d]: %w", i, err)
				}
			}
			return nil
		}
	}

	// Try as array of strings.
	var strSkills []string
	if err := json.Unmarshal(data, &strSkills); err == nil && len(strSkills) > 0 {
		for i, sk := range strSkills {
			idx := strings.LastIndex(sk, ":")
			if idx > 0 && idx < len(sk)-1 {
				// Has a colon — treat as "repo:skill" format.
				repoURL := sk[:idx]
				skillName := sk[idx+1:]
				if !strings.HasPrefix(repoURL, "https://") {
					repoURL = "https://github.com/" + repoURL
				}
				if err := validateSingleSkillConfig(repoURL, skillName); err != nil {
					return fmt.Errorf("sub_agent_skills[%d]: %w", i, err)
				}
			} else {
				// No colon — plain tool/skill name. Validate it's safe.
				if !validSkillNameRe.MatchString(sk) {
					return fmt.Errorf("sub_agent_skills[%d]: skill name contains invalid characters", i)
				}
			}
		}
		return nil
	}

	return fmt.Errorf("sub_agent_skills must be an array of {repo_url, skill_name} objects or strings")
}

// validateSingleSkillConfig checks that a repo URL is valid HTTPS and skill name is safe.
func validateSingleSkillConfig(repoURL, skillName string) error {
	if repoURL == "" {
		return fmt.Errorf("repo_url is required")
	}
	if skillName == "" {
		return fmt.Errorf("skill_name is required")
	}

	u, err := url.Parse(repoURL)
	if err != nil {
		return fmt.Errorf("invalid repo_url: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("repo_url must use https scheme, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("repo_url must have a host")
	}

	if strings.ContainsAny(repoURL, ";|&$`\\\"'<>(){}!") {
		return fmt.Errorf("repo_url contains invalid characters")
	}

	if !validSkillNameRe.MatchString(skillName) {
		return fmt.Errorf("skill_name contains invalid characters")
	}

	return nil
}

// CreatePostActionRequest is the payload for POST /api/post-actions.
type CreatePostActionRequest struct {
	Name           string            `json:"name"`
	Description    string            `json:"description"`
	Method         string            `json:"method"`
	URL            string            `json:"url"`
	Headers        map[string]string `json:"headers"`
	BodyTemplate   string            `json:"body_template"`
	AuthType       string            `json:"auth_type"`
	AuthConfig     map[string]string `json:"auth_config"`
	TimeoutSeconds *int              `json:"timeout_seconds"`
	RetryCount     *int              `json:"retry_count"`
	Enabled        *bool             `json:"enabled"`
}

// UpdatePostActionRequest is the payload for PUT /api/post-actions/:id.
type UpdatePostActionRequest struct {
	Name           *string           `json:"name"`
	Description    *string           `json:"description"`
	Method         *string           `json:"method"`
	URL            *string           `json:"url"`
	Headers        map[string]string `json:"headers"`
	BodyTemplate   *string           `json:"body_template"`
	AuthType       *string           `json:"auth_type"`
	AuthConfig     map[string]string `json:"auth_config"`
	TimeoutSeconds *int              `json:"timeout_seconds"`
	RetryCount     *int              `json:"retry_count"`
	Enabled        *bool             `json:"enabled"`
}

// CreateBindingRequest is the payload for POST /api/post-actions/:id/bindings.
type CreateBindingRequest struct {
	TriggerType  string `json:"trigger_type"`
	TriggerID    string `json:"trigger_id"`
	TriggerOn    string `json:"trigger_on"`
	BodyOverride string `json:"body_override"`
	Enabled      *bool  `json:"enabled"`
}

// UpdateBindingRequest is the payload for PUT /api/post-actions/:id/bindings/:bid.
type UpdateBindingRequest struct {
	TriggerOn    *string `json:"trigger_on"`
	BodyOverride *string `json:"body_override"`
	Enabled      *bool   `json:"enabled"`
}

// PostActionBindingResponse enriches a binding with the trigger's display name.
type PostActionBindingResponse struct {
	models.PostActionBinding
	TriggerName string `json:"trigger_name"`
}

// validateAgentImage checks that a custom agent image string is safe for use.
// An empty string is valid (means "use the default image").
// The image must contain at least one '/' (rejecting bare names like "nginx"),
// must not contain shell injection characters, control characters, spaces, or exceed 512 chars.
func validateAgentImage(img string) error {
	if img == "" {
		return nil
	}
	if len(img) > 512 {
		return fmt.Errorf("agent_image must be at most 512 characters")
	}
	// Reject control characters and non-printable ASCII (null bytes, newlines, tabs, etc.).
	for _, r := range img {
		if r < 0x20 || r > 0x7E {
			return fmt.Errorf("agent_image contains invalid characters")
		}
	}
	if strings.Contains(img, " ") {
		return fmt.Errorf("agent_image must not contain spaces")
	}
	if !strings.Contains(img, "/") {
		return fmt.Errorf("agent_image must include a registry or namespace (e.g. myregistry/myimage:tag)")
	}
	// Reuse shellMetaChars for consistency with other validators in this package.
	if strings.ContainsAny(img, shellMetaChars) {
		return fmt.Errorf("agent_image contains invalid characters")
	}
	return nil
}

// validMcpNameRe matches safe MCP server names: alphanumeric, hyphens, underscores.
var validMcpNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// shellMetaChars contains characters that could enable shell injection.
const shellMetaChars = ";|&$`\\\"'<>(){}!"

// validateMcpServers validates the McpServers field on a team request.
func validateMcpServers(raw interface{}) error {
	if raw == nil {
		return nil
	}

	data, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("invalid mcp_servers: %w", err)
	}

	s := strings.TrimSpace(string(data))
	if s == "null" || s == "[]" {
		return nil
	}

	var servers []struct {
		Name      string            `json:"name"`
		Transport string            `json:"transport"`
		Command   string            `json:"command"`
		Args      []string          `json:"args"`
		Env       map[string]string `json:"env"`
		URL       string            `json:"url"`
		Headers   map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(data, &servers); err != nil {
		return fmt.Errorf("mcp_servers must be an array of MCP server objects: %w", err)
	}

	if len(servers) > 50 {
		return fmt.Errorf("mcp_servers: maximum 50 servers allowed, got %d", len(servers))
	}

	seen := map[string]struct{}{}
	for i, srv := range servers {
		// Name validation.
		if srv.Name == "" {
			return fmt.Errorf("mcp_servers[%d]: name is required", i)
		}
		if len(srv.Name) > 64 {
			return fmt.Errorf("mcp_servers[%d]: name must be at most 64 characters", i)
		}
		if !validMcpNameRe.MatchString(srv.Name) {
			return fmt.Errorf("mcp_servers[%d]: name contains invalid characters (use alphanumeric, hyphens, underscores)", i)
		}
		lower := strings.ToLower(srv.Name)
		if _, exists := seen[lower]; exists {
			return fmt.Errorf("mcp_servers[%d]: duplicate server name %q", i, srv.Name)
		}
		seen[lower] = struct{}{}

		// Transport validation.
		switch srv.Transport {
		case "stdio":
			if srv.Command == "" {
				return fmt.Errorf("mcp_servers[%d]: command is required for stdio transport", i)
			}
			if strings.ContainsAny(srv.Command, shellMetaChars) {
				return fmt.Errorf("mcp_servers[%d]: command contains unsafe shell characters", i)
			}
			for j, arg := range srv.Args {
				if strings.ContainsAny(arg, shellMetaChars) {
					return fmt.Errorf("mcp_servers[%d].args[%d]: argument contains unsafe shell characters", i, j)
				}
			}
		case "http", "sse":
			if srv.URL == "" {
				return fmt.Errorf("mcp_servers[%d]: url is required for %s transport", i, srv.Transport)
			}
			u, err := url.Parse(srv.URL)
			if err != nil {
				return fmt.Errorf("mcp_servers[%d]: invalid url: %w", i, err)
			}
			if u.Scheme != "http" && u.Scheme != "https" {
				return fmt.Errorf("mcp_servers[%d]: url must use http or https scheme", i)
			}
		default:
			return fmt.Errorf("mcp_servers[%d]: transport must be stdio, http, or sse", i)
		}
	}

	return nil
}
