package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// AgentConfig holds the full configuration for the agent sidecar.
// Values are loaded from a YAML file and can be overridden by environment variables.
type AgentConfig struct {
	Agent AgentSection `yaml:"agent"`
}

// AgentSection contains agent-specific configuration.
type AgentSection struct {
	Name         string            `yaml:"name"`
	Team         string            `yaml:"team"`
	Role         string            `yaml:"role"`
	SystemPrompt string            `yaml:"system_prompt"`
	NATS         NATSSection       `yaml:"nats"`
	Permissions  PermissionsSection `yaml:"permissions"`
	Resources    ResourcesSection  `yaml:"resources"`
}

// NATSSection holds NATS connection settings.
type NATSSection struct {
	URL string `yaml:"url"`
}

// PermissionsSection maps to the permission gate configuration.
type PermissionsSection struct {
	AllowedTools    []string `yaml:"allowed_tools"`
	AllowedCommands []string `yaml:"allowed_commands"`
	DeniedCommands  []string `yaml:"denied_commands"`
	FilesystemScope string   `yaml:"filesystem_scope"`
}

// ResourcesSection holds resource limits for the agent.
type ResourcesSection struct {
	TimeoutSeconds int    `yaml:"timeout_seconds"`
	CPU            string `yaml:"cpu"`
	Memory         string `yaml:"memory"`
}

// LoadConfig reads a YAML config file and applies environment variable overrides.
// Environment variables take precedence over YAML values.
func LoadConfig(path string) (*AgentConfig, error) {
	cfg := &AgentConfig{}

	// Load from YAML file if path is provided and file exists.
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading config file %s: %w", path, err)
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parsing config file %s: %w", path, err)
		}
	}

	// Environment variable overrides.
	if v := os.Getenv("AGENT_NAME"); v != "" {
		cfg.Agent.Name = v
	}
	if v := os.Getenv("TEAM_NAME"); v != "" {
		cfg.Agent.Team = v
	}
	if v := os.Getenv("AGENT_ROLE"); v != "" {
		cfg.Agent.Role = v
	}
	if v := os.Getenv("AGENT_SYSTEM_PROMPT"); v != "" {
		cfg.Agent.SystemPrompt = v
	}
	if v := os.Getenv("NATS_URL"); v != "" {
		cfg.Agent.NATS.URL = v
	}
	if v := os.Getenv("AGENT_FILESYSTEM_SCOPE"); v != "" {
		cfg.Agent.Permissions.FilesystemScope = v
	}

	// Parse JSON permissions from env if provided (set by Docker runtime).
	if v := os.Getenv("AGENT_PERMISSIONS"); v != "" {
		// AGENT_PERMISSIONS is JSON from the runtime; parse into struct fields.
		// This is a simplified overlay — the YAML values are the primary source.
		var perms PermissionsSection
		if err := yaml.Unmarshal([]byte(v), &perms); err == nil {
			if len(perms.AllowedTools) > 0 {
				cfg.Agent.Permissions.AllowedTools = perms.AllowedTools
			}
			if len(perms.AllowedCommands) > 0 {
				cfg.Agent.Permissions.AllowedCommands = perms.AllowedCommands
			}
			if len(perms.DeniedCommands) > 0 {
				cfg.Agent.Permissions.DeniedCommands = perms.DeniedCommands
			}
			if perms.FilesystemScope != "" {
				cfg.Agent.Permissions.FilesystemScope = perms.FilesystemScope
			}
		}
	}

	// Validate required fields.
	if cfg.Agent.Name == "" {
		return nil, fmt.Errorf("agent name is required (set via config file or AGENT_NAME env)")
	}
	if cfg.Agent.Team == "" {
		return nil, fmt.Errorf("team name is required (set via config file or TEAM_NAME env)")
	}
	if cfg.Agent.NATS.URL == "" {
		return nil, fmt.Errorf("NATS URL is required (set via config file or NATS_URL env)")
	}

	// Defaults.
	if cfg.Agent.Role == "" {
		cfg.Agent.Role = "leader"
	}
	if cfg.Agent.Permissions.FilesystemScope == "" {
		cfg.Agent.Permissions.FilesystemScope = "/workspace"
	}

	return cfg, nil
}
