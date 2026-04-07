package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
)

// Qdrant infrastructure constants.
const (
	QdrantContainerName = "agentcrew-qdrant"
	QdrantVolumeName    = "agentcrew-qdrant-data"
	QdrantImage         = "qdrant/qdrant:v1.12.6"
	QdrantInternalPort  = "6333"
	QdrantInternalURL   = "http://agentcrew-qdrant:6333"
)

// EnsureQdrant creates or restarts the Qdrant container. Returns the container ID.
func (d *DockerRuntime) EnsureQdrant(ctx context.Context) (string, error) {
	// Check if the container already exists.
	info, err := d.client.ContainerInspect(ctx, QdrantContainerName)
	if err == nil {
		// Container exists.
		if info.State.Running {
			slog.Info("qdrant container already running", "id", info.ID[:12])
			return info.ID, nil
		}
		// Exists but stopped — start it.
		slog.Info("starting existing qdrant container", "id", info.ID[:12])
		if err := d.client.ContainerStart(ctx, info.ID, container.StartOptions{}); err != nil {
			return "", fmt.Errorf("starting qdrant container: %w", err)
		}
		if err := d.waitForQdrantHealthy(ctx, info.ID, 60*time.Second); err != nil {
			return "", err
		}
		return info.ID, nil
	}

	// Container doesn't exist — create it.
	slog.Info("creating qdrant container")

	// Pull image.
	if err := d.pullImageIfNeeded(ctx, QdrantImage); err != nil {
		return "", fmt.Errorf("qdrant image: %w", err)
	}

	// Ensure volume exists.
	_, err = d.client.VolumeCreate(ctx, volume.CreateOptions{
		Name:   QdrantVolumeName,
		Labels: map[string]string{LabelInfra: "qdrant"},
	})
	if err != nil && !isAlreadyExistsErr(err) {
		return "", fmt.Errorf("creating qdrant volume: %w", err)
	}

	// Build container config.
	// Qdrant v1.12+ images have neither curl nor wget; use bash /dev/tcp
	// to verify the HTTP port is open.
	healthCheck := &container.HealthConfig{
		Test:     []string{"CMD-SHELL", "bash -c 'echo > /dev/tcp/localhost/6333' || exit 1"},
		Interval: 5 * time.Second,
		Timeout:  10 * time.Second,
		Retries:  12,
	}

	hostConfig := &container.HostConfig{
		Mounts: []mount.Mount{
			{
				Type:   mount.TypeVolume,
				Source: QdrantVolumeName,
				Target: "/qdrant/storage",
			},
		},
		RestartPolicy: container.RestartPolicy{
			Name: "unless-stopped",
		},
	}

	resp, err := d.client.ContainerCreate(ctx,
		&container.Config{
			Image: QdrantImage,
			Labels: map[string]string{
				LabelInfra: "qdrant",
			},
			Healthcheck: healthCheck,
		},
		hostConfig,
		nil, // no initial network — connected per-team via ConnectQdrantToNetwork
		nil,
		QdrantContainerName,
	)
	if err != nil {
		return "", fmt.Errorf("creating qdrant container: %w", err)
	}

	if err := d.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("starting qdrant container: %w", err)
	}

	slog.Info("qdrant container started", "id", resp.ID[:12])

	if err := d.waitForQdrantHealthy(ctx, resp.ID, 60*time.Second); err != nil {
		return "", err
	}

	return resp.ID, nil
}

// waitForQdrantHealthy polls the Docker HEALTHCHECK status until the container
// becomes healthy or the timeout expires.
func (d *DockerRuntime) waitForQdrantHealthy(ctx context.Context, containerID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		info, err := d.client.ContainerInspect(ctx, containerID)
		if err != nil {
			return fmt.Errorf("inspecting qdrant container: %w", err)
		}
		if info.State.Health != nil && info.State.Health.Status == "healthy" {
			slog.Info("qdrant container is healthy", "id", containerID[:12])
			return nil
		}
		slog.Debug("waiting for qdrant to become healthy",
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
	return fmt.Errorf("qdrant container did not become healthy within %s", timeout)
}

// ConnectQdrantToNetwork connects the Qdrant container to a Docker network,
// enabling DNS resolution of "agentcrew-qdrant" from containers on that network.
func (d *DockerRuntime) ConnectQdrantToNetwork(ctx context.Context, networkName string) error {
	err := d.client.NetworkConnect(ctx, networkName, QdrantContainerName, &network.EndpointSettings{})
	if err != nil {
		// Handle "already connected" gracefully.
		if strings.Contains(err.Error(), "already exists") {
			slog.Info("qdrant already connected to network", "network", networkName)
			return nil
		}
		return fmt.Errorf("connecting qdrant to network %s: %w", networkName, err)
	}
	slog.Info("qdrant connected to network", "network", networkName)
	return nil
}

// DisconnectQdrantFromNetwork disconnects the Qdrant container from a Docker network.
func (d *DockerRuntime) DisconnectQdrantFromNetwork(ctx context.Context, networkName string) error {
	err := d.client.NetworkDisconnect(ctx, networkName, QdrantContainerName, false)
	if err != nil {
		// Handle "not connected" gracefully.
		if strings.Contains(err.Error(), "is not connected") || strings.Contains(err.Error(), "not found") {
			slog.Info("qdrant not connected to network, skipping disconnect", "network", networkName)
			return nil
		}
		return fmt.Errorf("disconnecting qdrant from network %s: %w", networkName, err)
	}
	slog.Info("qdrant disconnected from network", "network", networkName)
	return nil
}

// EnsureNetwork creates a Docker bridge network if it doesn't already exist,
// then connects the given container (e.g. the API itself) to it.
func (d *DockerRuntime) EnsureNetwork(ctx context.Context, networkName string) error {
	_, err := d.client.NetworkCreate(ctx, networkName, network.CreateOptions{
		Labels: map[string]string{LabelInfra: "knowledge"},
	})
	if err != nil && !isAlreadyExistsErr(err) {
		return fmt.Errorf("creating network %s: %w", networkName, err)
	}
	return nil
}

// ConnectSelfToNetwork connects the current container (the API process) to a
// Docker network so it can reach other containers via DNS. It uses os.Hostname()
// which returns the short container ID inside Docker.
func (d *DockerRuntime) ConnectSelfToNetwork(ctx context.Context, networkName string) error {
	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("getting hostname for self-connect: %w", err)
	}
	if err := d.client.NetworkConnect(ctx, networkName, hostname, &network.EndpointSettings{}); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return nil
		}
		return fmt.Errorf("connecting self (%s) to network %s: %w", hostname, networkName, err)
	}
	slog.Info("connected API container to network", "network", networkName, "container", hostname)
	return nil
}

// IsQdrantRunning checks if the Qdrant container exists and is running.
func (d *DockerRuntime) IsQdrantRunning(ctx context.Context) (bool, error) {
	info, err := d.client.ContainerInspect(ctx, QdrantContainerName)
	if err != nil {
		if strings.Contains(err.Error(), "No such container") || strings.Contains(err.Error(), "not found") {
			return false, nil
		}
		return false, fmt.Errorf("inspecting qdrant container: %w", err)
	}
	return info.State.Running, nil
}
