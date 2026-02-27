// Package runtime defines the container runtime interface and Docker implementation.
package runtime

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
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
	ClaudeMD        string            // CLAUDE.md content passed via env var for sidecar to write
	AgentConfigYAML string            // serialized agent config to mount into the container
	SubAgentFiles   map[string]string // filename → content for .claude/agents/*.md, passed via env var to sidecar
	Env             map[string]string // extra environment variables (e.g. from Settings DB)
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
	// GetNATSConnectURL returns a NATS URL reachable from the API server process
	// (e.g. nats://127.0.0.1:<host-port> for Docker, in-cluster DNS for K8s).
	GetNATSConnectURL(ctx context.Context, teamName string) (string, error)
	// ExecInContainer runs a command inside a running agent container and returns
	// the combined stdout+stderr output.
	ExecInContainer(ctx context.Context, id string, cmd []string) (string, error)
	// ReadFile reads a file from a running agent container at the given path.
	// The path must pass ValidateAgentFilePath checks.
	ReadFile(ctx context.Context, containerID string, path string) ([]byte, error)
	// WriteFile writes content to a file inside a running agent container.
	// The path must pass ValidateAgentFilePath checks.
	WriteFile(ctx context.Context, containerID string, path string, content []byte) error
}

// ValidateAgentFilePath checks that the given path is safe for agent file
// operations. It rejects path traversal attempts and only allows paths under
// /workspace/.claude/. Specifically: /workspace/.claude/CLAUDE.md (leader
// instructions) or /workspace/.claude/agents/<name>.md (worker instructions).
func ValidateAgentFilePath(filePath string) error {
	if strings.Contains(filePath, "..") {
		return fmt.Errorf("path traversal not allowed: %s", filePath)
	}

	cleaned := filepath.Clean(filePath)

	const allowedPrefix = "/workspace/.claude/"
	if !strings.HasPrefix(cleaned, allowedPrefix) {
		return fmt.Errorf("path must be under /workspace/.claude/: %s", filePath)
	}

	// Allow /workspace/.claude/CLAUDE.md
	if cleaned == "/workspace/.claude/CLAUDE.md" {
		return nil
	}

	// Allow /workspace/.claude/agents/<name>.md
	dir := filepath.Dir(cleaned)
	base := filepath.Base(cleaned)
	if dir == "/workspace/.claude/agents" && strings.HasSuffix(base, ".md") {
		return nil
	}

	return fmt.Errorf("path not allowed (must be /workspace/.claude/CLAUDE.md or /workspace/.claude/agents/<name>.md): %s", filePath)
}
