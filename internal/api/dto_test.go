package api

import (
	"strings"
	"testing"

	"github.com/helmcode/agent-crew/internal/models"
)

func TestValidateAgentImage(t *testing.T) {
	tests := []struct {
		name    string
		image   string
		wantErr bool
		errMsg  string
	}{
		// Valid cases.
		{name: "empty string uses default", image: "", wantErr: false},
		{name: "ghcr image with tag", image: "ghcr.io/helmcode/agent_crew_agent:latest", wantErr: false},
		{name: "docker hub with namespace", image: "myorg/myimage:v1.0", wantErr: false},
		{name: "ecr image", image: "123456789.dkr.ecr.us-east-1.amazonaws.com/myapp:latest", wantErr: false},
		{name: "image with sha256 digest", image: "ghcr.io/org/image@sha256:abc123", wantErr: false},
		{name: "nested registry path", image: "registry.example.com/org/team/image:v2", wantErr: false},

		// Invalid cases.
		{name: "bare image name", image: "nginx", wantErr: true, errMsg: "must include a registry"},
		{name: "bare image with tag", image: "alpine:3.18", wantErr: true, errMsg: "must include a registry"},
		{name: "bare ubuntu", image: "ubuntu", wantErr: true, errMsg: "must include a registry"},
		{name: "contains semicolon", image: "ghcr.io/org/img;cmd", wantErr: true, errMsg: "invalid characters"},
		{name: "contains pipe", image: "ghcr.io/org/img|cat", wantErr: true, errMsg: "invalid characters"},
		{name: "contains ampersand", image: "ghcr.io/org/img&echo", wantErr: true, errMsg: "invalid characters"},
		{name: "contains dollar", image: "ghcr.io/org/$img", wantErr: true, errMsg: "invalid characters"},
		{name: "contains backtick", image: "ghcr.io/org/`img`", wantErr: true, errMsg: "invalid characters"},
		{name: "contains double quote", image: `ghcr.io/org/"img"`, wantErr: true, errMsg: "invalid characters"},
		{name: "contains single quote", image: "ghcr.io/org/'img'", wantErr: true, errMsg: "invalid characters"},
		{name: "contains angle brackets", image: "ghcr.io/org/<img>", wantErr: true, errMsg: "invalid characters"},
		{name: "contains parens", image: "ghcr.io/org/(img)", wantErr: true, errMsg: "invalid characters"},
		{name: "contains braces", image: "ghcr.io/org/{img}", wantErr: true, errMsg: "invalid characters"},
		{name: "contains exclamation", image: "ghcr.io/org/img!", wantErr: true, errMsg: "invalid characters"},
		{name: "contains backslash", image: "ghcr.io/org/img\\cmd", wantErr: true, errMsg: "invalid characters"},
		{name: "contains newline", image: "ghcr.io/org/img\ncmd", wantErr: true, errMsg: "invalid characters"},
		{name: "contains tab", image: "ghcr.io/org/img\tcmd", wantErr: true, errMsg: "invalid characters"},
		{name: "contains null byte", image: "ghcr.io/org/img\x00cmd", wantErr: true, errMsg: "invalid characters"},
		{name: "contains space", image: "ghcr.io/org/my image", wantErr: true, errMsg: "must not contain spaces"},
		{name: "exceeds 512 chars", image: "ghcr.io/org/" + strings.Repeat("a", 501), wantErr: true, errMsg: "at most 512"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAgentImage(tt.image)
			if tt.wantErr {
				if err == nil {
					t.Errorf("validateAgentImage(%q) = nil, want error containing %q", tt.image, tt.errMsg)
				} else if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("validateAgentImage(%q) error = %q, want error containing %q", tt.image, err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("validateAgentImage(%q) = %v, want nil", tt.image, err)
				}
			}
		})
	}
}

func TestValidateModelProvider(t *testing.T) {
	tests := []struct {
		name          string
		provider      string
		modelProvider string
		wantErr       bool
		errMsg        string
	}{
		// Claude provider — model_provider is always ignored.
		{name: "claude ignores empty", provider: models.ProviderClaude, modelProvider: "", wantErr: false},
		{name: "claude ignores anthropic", provider: models.ProviderClaude, modelProvider: "anthropic", wantErr: false},
		{name: "claude ignores invalid", provider: models.ProviderClaude, modelProvider: "invalid", wantErr: false},

		// OpenCode provider — valid values.
		{name: "opencode empty allowed", provider: models.ProviderOpenCode, modelProvider: "", wantErr: false},
		{name: "opencode anthropic", provider: models.ProviderOpenCode, modelProvider: "anthropic", wantErr: false},
		{name: "opencode openai", provider: models.ProviderOpenCode, modelProvider: "openai", wantErr: false},
		{name: "opencode google", provider: models.ProviderOpenCode, modelProvider: "google", wantErr: false},
		{name: "opencode ollama", provider: models.ProviderOpenCode, modelProvider: "ollama", wantErr: false},

		// OpenCode provider — invalid values.
		{name: "opencode invalid value", provider: models.ProviderOpenCode, modelProvider: "aws-bedrock", wantErr: true, errMsg: "invalid model_provider"},
		{name: "opencode random string", provider: models.ProviderOpenCode, modelProvider: "foobar", wantErr: true, errMsg: "invalid model_provider"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateModelProvider(tt.provider, tt.modelProvider)
			if tt.wantErr {
				if err == nil {
					t.Errorf("validateModelProvider(%q, %q) = nil, want error containing %q", tt.provider, tt.modelProvider, tt.errMsg)
				} else if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("validateModelProvider(%q, %q) error = %q, want error containing %q", tt.provider, tt.modelProvider, err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("validateModelProvider(%q, %q) = %v, want nil", tt.provider, tt.modelProvider, err)
				}
			}
		})
	}
}

func TestValidateAgentModelConsistency(t *testing.T) {
	tests := []struct {
		name          string
		modelProvider string
		agents        []CreateAgentInput
		wantErr       bool
		errMsg        string
	}{
		// No restriction when model_provider is empty.
		{name: "empty provider allows anything", modelProvider: "", agents: []CreateAgentInput{
			{Name: "agent1", SubAgentModel: "openai/gpt-4"},
		}, wantErr: false},

		// Inherit and empty models are always allowed.
		{name: "inherit model allowed", modelProvider: "anthropic", agents: []CreateAgentInput{
			{Name: "agent1", SubAgentModel: "inherit"},
		}, wantErr: false},
		{name: "empty model allowed", modelProvider: "anthropic", agents: []CreateAgentInput{
			{Name: "agent1", SubAgentModel: ""},
		}, wantErr: false},

		// Matching models.
		{name: "anthropic model matches", modelProvider: "anthropic", agents: []CreateAgentInput{
			{Name: "agent1", SubAgentModel: "anthropic/claude-sonnet-4-20250514"},
		}, wantErr: false},
		{name: "openai model matches", modelProvider: "openai", agents: []CreateAgentInput{
			{Name: "agent1", SubAgentModel: "openai/gpt-4o"},
		}, wantErr: false},
		{name: "multiple agents all match", modelProvider: "google", agents: []CreateAgentInput{
			{Name: "agent1", SubAgentModel: "google/gemini-pro"},
			{Name: "agent2", SubAgentModel: "inherit"},
			{Name: "agent3", SubAgentModel: "google/gemini-flash"},
		}, wantErr: false},

		// Mismatching models.
		{name: "wrong provider prefix", modelProvider: "anthropic", agents: []CreateAgentInput{
			{Name: "agent1", SubAgentModel: "openai/gpt-4"},
		}, wantErr: true, errMsg: "doesn't match team model_provider"},
		{name: "no slash in model", modelProvider: "anthropic", agents: []CreateAgentInput{
			{Name: "agent1", SubAgentModel: "claude-sonnet"},
		}, wantErr: true, errMsg: "doesn't match team model_provider"},
		{name: "one agent mismatches", modelProvider: "openai", agents: []CreateAgentInput{
			{Name: "agent1", SubAgentModel: "openai/gpt-4"},
			{Name: "agent2", SubAgentModel: "anthropic/claude-sonnet-4-20250514"},
		}, wantErr: true, errMsg: "doesn't match team model_provider"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAgentModelConsistency(tt.modelProvider, tt.agents)
			if tt.wantErr {
				if err == nil {
					t.Errorf("validateAgentModelConsistency(%q, ...) = nil, want error containing %q", tt.modelProvider, tt.errMsg)
				} else if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("validateAgentModelConsistency(%q, ...) error = %q, want error containing %q", tt.modelProvider, err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("validateAgentModelConsistency(%q, ...) = %v, want nil", tt.modelProvider, err)
				}
			}
		})
	}
}
