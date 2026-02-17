// Package runtime defines the container runtime interface and Docker implementation.
package runtime

import (
	"context"
	"io"
	"time"

	"github.com/helmcode/agent-crew/internal/permissions"
)

// AgentConfig holds the configuration needed to deploy a single agent container.
type AgentConfig struct {
	Name            string
	TeamName        string
	Role            string
	SystemPrompt    string
	Permissions     permissions.PermissionConfig
	Resources       ResourceConfig
	NATSUrl         string
	Image           string
	WorkspacePath   string
	AgentConfigYAML string // serialized agent config to mount into the container
}

// ResourceConfig defines compute resource limits for an agent.
type ResourceConfig struct {
	CPU     string `json:"cpu"`
	Memory  string `json:"memory"`
	Timeout int    `json:"timeout_seconds"`
}

// InfraConfig holds the configuration for shared team infrastructure.
type InfraConfig struct {
	TeamName      string
	NATSEnabled   bool
	WorkspacePath string
}

// AgentInstance represents a deployed agent container.
type AgentInstance struct {
	ID     string
	Name   string
	Status string
}

// AgentStatus holds the runtime status of an agent container.
type AgentStatus struct {
	ID        string
	Name      string
	Status    string // running, stopped, error
	StartedAt time.Time
}

// Shared constants used by both Docker and Kubernetes runtimes.
const (
	DefaultAgentImage = "ghcr.io/helmcode/agent-crew-agent:latest"
	NATSImage         = "nats:2.10-alpine"
	LabelTeam         = "agentcrew.team"
	LabelAgent        = "agentcrew.agent"
	LabelRole         = "agentcrew.role"
)

// AgentRuntime is the interface for managing agent container lifecycles.
type AgentRuntime interface {
	DeployInfra(ctx context.Context, config InfraConfig) error
	DeployAgent(ctx context.Context, config AgentConfig) (*AgentInstance, error)
	StopAgent(ctx context.Context, id string) error
	RemoveAgent(ctx context.Context, id string) error
	GetStatus(ctx context.Context, id string) (*AgentStatus, error)
	StreamLogs(ctx context.Context, id string) (io.ReadCloser, error)
	TeardownInfra(ctx context.Context, teamName string) error
	GetNATSURL(teamName string) string
}
