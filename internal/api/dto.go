// Package api implements the Fiber HTTP API for the AgentCrew orchestrator.
//
// Team and agent names accept any human-friendly string (e.g. "My Team", "Test").
// When names are used for Docker/K8s infrastructure resources, they are sanitized
// internally via SanitizeName to produce a safe slug.
package api

import (
	"fmt"
	"regexp"
	"strings"
)

// CreateTeamRequest is the payload for POST /api/teams.
type CreateTeamRequest struct {
	Name          string              `json:"name" validate:"required"`
	Description   string              `json:"description"`
	Runtime       string              `json:"runtime"`
	WorkspacePath string              `json:"workspace_path"`
	Agents        []CreateAgentInput  `json:"agents"`
}

// UpdateTeamRequest is the payload for PUT /api/teams/:id.
type UpdateTeamRequest struct {
	Name          *string `json:"name"`
	Description   *string `json:"description"`
	WorkspacePath *string `json:"workspace_path"`
}

// CreateAgentInput defines an agent to be created alongside a team.
type CreateAgentInput struct {
	Name                string      `json:"name" validate:"required"`
	Role                string      `json:"role"`
	Specialty           string      `json:"specialty"`
	SystemPrompt        string      `json:"system_prompt"`
	ClaudeMD            string      `json:"claude_md"`
	Skills              interface{} `json:"skills"`
	Permissions         interface{} `json:"permissions"`
	Resources           interface{} `json:"resources"`
	SubAgentDescription string      `json:"sub_agent_description"`
	SubAgentModel       string      `json:"sub_agent_model"`
	SubAgentSkills      interface{} `json:"sub_agent_skills"`
}

// CreateAgentRequest is the payload for POST /api/teams/:id/agents.
type CreateAgentRequest struct {
	Name                string      `json:"name" validate:"required"`
	Role                string      `json:"role"`
	Specialty           string      `json:"specialty"`
	SystemPrompt        string      `json:"system_prompt"`
	ClaudeMD            string      `json:"claude_md"`
	Skills              interface{} `json:"skills"`
	Permissions         interface{} `json:"permissions"`
	Resources           interface{} `json:"resources"`
	SubAgentDescription string      `json:"sub_agent_description"`
	SubAgentModel       string      `json:"sub_agent_model"`
	SubAgentSkills      interface{} `json:"sub_agent_skills"`
}

// UpdateAgentRequest is the payload for PUT /api/teams/:id/agents/:agentId.
type UpdateAgentRequest struct {
	Name                *string     `json:"name"`
	Role                *string     `json:"role"`
	Specialty           *string     `json:"specialty"`
	SystemPrompt        *string     `json:"system_prompt"`
	ClaudeMD            *string     `json:"claude_md"`
	Skills              interface{} `json:"skills"`
	Permissions         interface{} `json:"permissions"`
	Resources           interface{} `json:"resources"`
	SubAgentDescription *string     `json:"sub_agent_description"`
	SubAgentModel       *string     `json:"sub_agent_model"`
	SubAgentSkills      interface{} `json:"sub_agent_skills"`
}

// ChatRequest is the payload for POST /api/teams/:id/chat.
type ChatRequest struct {
	Message string `json:"message" validate:"required"`
}

// UpdateSettingsRequest is the payload for PUT /api/settings.
type UpdateSettingsRequest struct {
	Key   string `json:"key" validate:"required"`
	Value string `json:"value"`
}

// ErrorResponse is a standard error response.
type ErrorResponse struct {
	Error   string `json:"error"`
	Details string `json:"details,omitempty"`
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
