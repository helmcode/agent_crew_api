// Package api implements the Fiber HTTP API for the AgentCrew orchestrator.
//
// Name validation: team and agent names must be 1-64 alphanumeric characters,
// hyphens, or underscores to prevent injection in Docker container names and
// NATS subjects.
package api

import (
	"fmt"
	"regexp"
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
	Name         string      `json:"name" validate:"required"`
	Role         string      `json:"role"`
	Specialty    string      `json:"specialty"`
	SystemPrompt string      `json:"system_prompt"`
	Skills       interface{} `json:"skills"`
	Permissions  interface{} `json:"permissions"`
	Resources    interface{} `json:"resources"`
}

// CreateAgentRequest is the payload for POST /api/teams/:id/agents.
type CreateAgentRequest struct {
	Name         string      `json:"name" validate:"required"`
	Role         string      `json:"role"`
	Specialty    string      `json:"specialty"`
	SystemPrompt string      `json:"system_prompt"`
	Skills       interface{} `json:"skills"`
	Permissions  interface{} `json:"permissions"`
	Resources    interface{} `json:"resources"`
}

// UpdateAgentRequest is the payload for PUT /api/teams/:id/agents/:agentId.
type UpdateAgentRequest struct {
	Name         *string     `json:"name"`
	Role         *string     `json:"role"`
	Specialty    *string     `json:"specialty"`
	SystemPrompt *string     `json:"system_prompt"`
	Skills       interface{} `json:"skills"`
	Permissions  interface{} `json:"permissions"`
	Resources    interface{} `json:"resources"`
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

// validNameRe validates team and agent names: alphanumeric, hyphens, underscores, 1-64 chars.
var validNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// validateName checks that a name is safe for use in Docker container names and NATS subjects.
func validateName(name string) error {
	if !validNameRe.MatchString(name) {
		return fmt.Errorf("name must be 1-64 alphanumeric characters, hyphens, or underscores")
	}
	return nil
}
