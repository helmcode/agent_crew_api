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
	Provider        string // "claude" (default) or "opencode"
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
	DefaultAgentImage         = "ghcr.io/helmcode/agent_crew_agent:latest"
	DefaultOpenCodeAgentImage = "ghcr.io/helmcode/agent_crew_opencode_agent:latest"
	NATSImage                 = "nats:2.10-alpine"
	LabelTeam                 = "agentcrew.team"
	LabelAgent                = "agentcrew.agent"
	LabelRole                 = "agentcrew.role"
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
	// CopyToContainer writes arbitrary file content to a container at the given
	// path. Unlike WriteFile, it does NOT apply ValidateAgentFilePath checks.
	// It uses Docker's CopyToContainer API (tar archive) to avoid shell ARG_MAX
	// limits, making it safe for large binary files (e.g. PDF uploads).
	CopyToContainer(ctx context.Context, containerID string, destPath string, content []byte) error
}

// TeamNetworkName returns the Docker network name for a given sanitized team name.
// Exported so handlers can compute network names for Ollama connectivity.
func TeamNetworkName(sanitizedTeamName string) string {
	return teamNetworkName(sanitizedTeamName)
}

// OllamaManager is an optional interface for runtimes that support Ollama
// lifecycle management. Use a type assertion to check if a runtime supports it:
//
//	if om, ok := rt.(OllamaManager); ok { ... }
type OllamaManager interface {
	EnsureOllama(ctx context.Context) (string, error)
	ConnectOllamaToNetwork(ctx context.Context, networkName string) error
	DisconnectOllamaFromNetwork(ctx context.Context, networkName string) error
	PullOllamaModel(ctx context.Context, model string, progressFn func(status string)) error
	WarmUpOllamaModel(ctx context.Context, model string) error
	StopOllama(ctx context.Context) error
	IsOllamaRunning(ctx context.Context) (bool, error)
}

// ValidateAgentFilePath checks that the given path is safe for agent file
// operations. It rejects path traversal attempts and only allows paths under
// /workspace/.claude/ or /workspace/.opencode/. Specifically:
//   - /workspace/.claude/CLAUDE.md or /workspace/.opencode/AGENTS.MD (leader instructions)
//   - /workspace/.claude/agents/<name>.md or /workspace/.opencode/agents/<name>.md (worker instructions)
func ValidateAgentFilePath(filePath string) error {
	if strings.Contains(filePath, "..") {
		return fmt.Errorf("path traversal not allowed: %s", filePath)
	}

	cleaned := filepath.Clean(filePath)

	// Check if path is under one of the allowed prefixes.
	allowedPrefixes := []string{"/workspace/.claude/", "/workspace/.opencode/"}
	hasAllowedPrefix := false
	for _, prefix := range allowedPrefixes {
		if strings.HasPrefix(cleaned, prefix) {
			hasAllowedPrefix = true
			break
		}
	}
	if !hasAllowedPrefix {
		return fmt.Errorf("path must be under /workspace/.claude/ or /workspace/.opencode/: %s", filePath)
	}

	// Allow leader instruction files.
	if cleaned == "/workspace/.claude/CLAUDE.md" || cleaned == "/workspace/.opencode/AGENTS.MD" {
		return nil
	}

	// Allow agent files under agents/ subdirectory.
	dir := filepath.Dir(cleaned)
	base := filepath.Base(cleaned)
	if (dir == "/workspace/.claude/agents" || dir == "/workspace/.opencode/agents") && strings.HasSuffix(base, ".md") {
		return nil
	}

	return fmt.Errorf("path not allowed: %s", filePath)
}
