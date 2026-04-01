package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
)

// RAG MCP infrastructure constants.
const (
	RagMcpContainerName = "agentcrew-rag-mcp"
	RagMcpImage         = "agentcrew-rag-mcp:latest"
	RagMcpInternalPort  = "8090"
	RagMcpInternalURL   = "http://agentcrew-rag-mcp:8090"
)

// EnsureRagMcp creates or restarts the RAG MCP server container. Returns the container ID.
func (d *DockerRuntime) EnsureRagMcp(ctx context.Context) (string, error) {
	// Check if the container already exists.
	info, err := d.client.ContainerInspect(ctx, RagMcpContainerName)
	if err == nil {
		// Container exists.
		if info.State.Running {
			slog.Info("rag-mcp container already running", "id", info.ID[:12])
			return info.ID, nil
		}
		// Exists but stopped — start it.
		slog.Info("starting existing rag-mcp container", "id", info.ID[:12])
		if err := d.client.ContainerStart(ctx, info.ID, container.StartOptions{}); err != nil {
			return "", fmt.Errorf("starting rag-mcp container: %w", err)
		}
		if err := d.waitForRagMcpHealthy(ctx, info.ID, 30*time.Second); err != nil {
			return "", err
		}
		return info.ID, nil
	}

	// Container doesn't exist — create it.
	slog.Info("creating rag-mcp container")

	// Pull image.
	if err := d.pullImageIfNeeded(ctx, RagMcpImage); err != nil {
		return "", fmt.Errorf("rag-mcp image: %w", err)
	}

	// Health check via the /health endpoint.
	healthCheck := &container.HealthConfig{
		Test:     []string{"CMD-SHELL", "wget -q --spider http://localhost:8090/health || exit 1"},
		Interval: 5 * time.Second,
		Timeout:  10 * time.Second,
		Retries:  6,
	}

	hostConfig := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{
			Name: "unless-stopped",
		},
	}

	resp, err := d.client.ContainerCreate(ctx,
		&container.Config{
			Image: RagMcpImage,
			Env: []string{
				"QDRANT_URL=" + QdrantInternalURL,
				"OLLAMA_URL=" + OllamaInternalURL,
				"LISTEN_ADDR=:8090",
			},
			Labels: map[string]string{
				LabelInfra: "rag-mcp",
			},
			Healthcheck: healthCheck,
		},
		hostConfig,
		nil, // no initial network — connected via ConnectRagMcpToNetwork
		nil,
		RagMcpContainerName,
	)
	if err != nil {
		return "", fmt.Errorf("creating rag-mcp container: %w", err)
	}

	if err := d.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("starting rag-mcp container: %w", err)
	}

	slog.Info("rag-mcp container started", "id", resp.ID[:12])

	if err := d.waitForRagMcpHealthy(ctx, resp.ID, 30*time.Second); err != nil {
		return "", err
	}

	return resp.ID, nil
}

// waitForRagMcpHealthy polls the Docker HEALTHCHECK status until the container
// becomes healthy or the timeout expires.
func (d *DockerRuntime) waitForRagMcpHealthy(ctx context.Context, containerID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		info, err := d.client.ContainerInspect(ctx, containerID)
		if err != nil {
			return fmt.Errorf("inspecting rag-mcp container: %w", err)
		}
		if info.State.Health != nil && info.State.Health.Status == "healthy" {
			slog.Info("rag-mcp container is healthy", "id", containerID[:12])
			return nil
		}
		slog.Debug("waiting for rag-mcp to become healthy",
			"id", containerID[:12],
			"health", func() string {
				if info.State.Health != nil {
					return info.State.Health.Status
				}
				return "no-healthcheck"
			}())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("rag-mcp container did not become healthy within %s", timeout)
}

// ConnectRagMcpToNetwork connects the RAG MCP container to a Docker network.
func (d *DockerRuntime) ConnectRagMcpToNetwork(ctx context.Context, networkName string) error {
	err := d.client.NetworkConnect(ctx, networkName, RagMcpContainerName, &network.EndpointSettings{})
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			slog.Info("rag-mcp already connected to network", "network", networkName)
			return nil
		}
		return fmt.Errorf("connecting rag-mcp to network %s: %w", networkName, err)
	}
	slog.Info("rag-mcp connected to network", "network", networkName)
	return nil
}

// DisconnectRagMcpFromNetwork disconnects the RAG MCP container from a Docker network.
func (d *DockerRuntime) DisconnectRagMcpFromNetwork(ctx context.Context, networkName string) error {
	err := d.client.NetworkDisconnect(ctx, networkName, RagMcpContainerName, false)
	if err != nil {
		if strings.Contains(err.Error(), "is not connected") || strings.Contains(err.Error(), "not found") {
			slog.Info("rag-mcp not connected to network, skipping disconnect", "network", networkName)
			return nil
		}
		return fmt.Errorf("disconnecting rag-mcp from network %s: %w", networkName, err)
	}
	slog.Info("rag-mcp disconnected from network", "network", networkName)
	return nil
}

// IsRagMcpRunning checks if the RAG MCP container exists and is running.
func (d *DockerRuntime) IsRagMcpRunning(ctx context.Context) (bool, error) {
	info, err := d.client.ContainerInspect(ctx, RagMcpContainerName)
	if err != nil {
		if strings.Contains(err.Error(), "No such container") || strings.Contains(err.Error(), "not found") {
			return false, nil
		}
		return false, fmt.Errorf("inspecting rag-mcp container: %w", err)
	}
	return info.State.Running, nil
}
