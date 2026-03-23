package runtime

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
)

// Ollama infrastructure constants.
const (
	OllamaContainerName = "agentcrew-ollama"
	OllamaVolumeName    = "agentcrew-ollama-models"
	OllamaImage         = "ollama/ollama:latest"
	OllamaInternalPort  = "11434"
	OllamaInternalURL   = "http://agentcrew-ollama:11434"
	LabelInfra          = "agentcrew.infra"
)

// EnsureOllama creates or restarts the Ollama container. Returns the container ID.
func (d *DockerRuntime) EnsureOllama(ctx context.Context) (string, error) {
	// Check if the container already exists.
	info, err := d.client.ContainerInspect(ctx, OllamaContainerName)
	if err == nil {
		// Container exists.
		if info.State.Running {
			slog.Info("ollama container already running", "id", info.ID[:12])
			return info.ID, nil
		}
		// Exists but stopped — start it.
		slog.Info("starting existing ollama container", "id", info.ID[:12])
		if err := d.client.ContainerStart(ctx, info.ID, container.StartOptions{}); err != nil {
			return "", fmt.Errorf("starting ollama container: %w", err)
		}
		if err := d.waitForOllamaHealthy(ctx, info.ID, 60*time.Second); err != nil {
			return "", err
		}
		return info.ID, nil
	}

	// Container doesn't exist — create it.
	slog.Info("creating ollama container")

	// Pull image.
	if err := d.pullImageIfNeeded(ctx, OllamaImage); err != nil {
		return "", fmt.Errorf("ollama image: %w", err)
	}

	// Ensure volume exists.
	_, err = d.client.VolumeCreate(ctx, volume.CreateOptions{
		Name:   OllamaVolumeName,
		Labels: map[string]string{LabelInfra: "ollama"},
	})
	if err != nil && !isAlreadyExistsErr(err) {
		return "", fmt.Errorf("creating ollama volume: %w", err)
	}

	// Build container config.
	// Use "ollama list" for the health check — the ollama binary is always present
	// in the image, unlike curl/wget which are not installed.
	healthCheck := &container.HealthConfig{
		Test:     []string{"CMD", "ollama", "list"},
		Interval: 5 * time.Second,
		Timeout:  10 * time.Second,
		Retries:  12,
	}

	hostConfig := &container.HostConfig{
		Mounts: []mount.Mount{
			{
				Type:   mount.TypeVolume,
				Source: OllamaVolumeName,
				Target: "/root/.ollama",
			},
		},
		RestartPolicy: container.RestartPolicy{
			Name: "unless-stopped",
		},
	}

	// Configure GPU if available.
	if hasGPU() {
		slog.Info("nvidia GPU detected, enabling GPU passthrough for ollama")
		hostConfig.DeviceRequests = []container.DeviceRequest{
			{
				Count:        -1, // all GPUs
				Capabilities: [][]string{{"gpu"}},
			},
		}
	}

	resp, err := d.client.ContainerCreate(ctx,
		&container.Config{
			Image: OllamaImage,
			Labels: map[string]string{
				LabelInfra: "ollama",
			},
			Healthcheck: healthCheck,
		},
		hostConfig,
		nil, // no initial network — connected per-team via ConnectOllamaToNetwork
		nil,
		OllamaContainerName,
	)
	if err != nil {
		return "", fmt.Errorf("creating ollama container: %w", err)
	}

	if err := d.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("starting ollama container: %w", err)
	}

	slog.Info("ollama container started", "id", resp.ID[:12])

	if err := d.waitForOllamaHealthy(ctx, resp.ID, 60*time.Second); err != nil {
		return "", err
	}

	return resp.ID, nil
}

// waitForOllamaHealthy polls the Docker HEALTHCHECK status until the container
// becomes healthy or the timeout expires. The HEALTHCHECK uses "ollama list"
// which runs inside the container, avoiding network reachability issues between
// the API container and the Ollama container (they may be on different Docker networks).
func (d *DockerRuntime) waitForOllamaHealthy(ctx context.Context, containerID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		info, err := d.client.ContainerInspect(ctx, containerID)
		if err != nil {
			return fmt.Errorf("inspecting ollama container: %w", err)
		}
		if info.State.Health != nil && info.State.Health.Status == "healthy" {
			slog.Info("ollama container is healthy", "id", containerID[:12])
			return nil
		}
		slog.Debug("waiting for ollama to become healthy",
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
	return fmt.Errorf("ollama container did not become healthy within %s", timeout)
}

// ConnectOllamaToNetwork connects the Ollama container to a team's Docker network,
// enabling DNS resolution of "agentcrew-ollama" from agent containers.
func (d *DockerRuntime) ConnectOllamaToNetwork(ctx context.Context, networkName string) error {
	err := d.client.NetworkConnect(ctx, networkName, OllamaContainerName, &network.EndpointSettings{})
	if err != nil {
		// Handle "already connected" gracefully.
		if strings.Contains(err.Error(), "already exists") {
			slog.Info("ollama already connected to network", "network", networkName)
			return nil
		}
		return fmt.Errorf("connecting ollama to network %s: %w", networkName, err)
	}
	slog.Info("ollama connected to network", "network", networkName)
	return nil
}

// DisconnectOllamaFromNetwork disconnects the Ollama container from a team's Docker network.
func (d *DockerRuntime) DisconnectOllamaFromNetwork(ctx context.Context, networkName string) error {
	err := d.client.NetworkDisconnect(ctx, networkName, OllamaContainerName, false)
	if err != nil {
		// Handle "not connected" gracefully.
		if strings.Contains(err.Error(), "is not connected") || strings.Contains(err.Error(), "not found") {
			slog.Info("ollama not connected to network, skipping disconnect", "network", networkName)
			return nil
		}
		return fmt.Errorf("disconnecting ollama from network %s: %w", networkName, err)
	}
	slog.Info("ollama disconnected from network", "network", networkName)
	return nil
}

// PullOllamaModel executes "ollama pull <model>" inside the container via docker exec.
// It parses progress output line by line and calls progressFn with status updates.
func (d *DockerRuntime) PullOllamaModel(ctx context.Context, model string, progressFn func(status string)) error {
	pullCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	execResp, err := d.client.ContainerExecCreate(pullCtx, OllamaContainerName, container.ExecOptions{
		Cmd:          []string{"ollama", "pull", model},
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("creating exec for ollama pull: %w", err)
	}

	resp, err := d.client.ContainerExecAttach(pullCtx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return fmt.Errorf("attaching to ollama pull exec: %w", err)
	}
	defer resp.Close()

	// Read output line by line and report progress.
	scanner := bufio.NewScanner(resp.Reader)
	for scanner.Scan() {
		line := scanner.Text()
		// Docker multiplexed stream may have header bytes; trim non-printable prefix.
		clean := strings.TrimLeftFunc(line, func(r rune) bool {
			return r < 32 && r != '\n' && r != '\r'
		})
		if clean != "" && progressFn != nil {
			progressFn(clean)
		}
	}

	// Check exec exit code.
	inspect, err := d.client.ContainerExecInspect(pullCtx, execResp.ID)
	if err != nil {
		return fmt.Errorf("inspecting ollama pull result: %w", err)
	}
	if inspect.ExitCode != 0 {
		return fmt.Errorf("ollama pull %s failed with exit code %d", model, inspect.ExitCode)
	}

	slog.Info("ollama model pulled successfully", "model", model)
	return nil
}

// StopOllama stops the Ollama container without removing it (preserves downloaded models).
func (d *DockerRuntime) StopOllama(ctx context.Context) error {
	timeout := 30
	if err := d.client.ContainerStop(ctx, OllamaContainerName, container.StopOptions{Timeout: &timeout}); err != nil {
		if strings.Contains(err.Error(), "No such container") || strings.Contains(err.Error(), "not found") {
			slog.Info("ollama container not found, nothing to stop")
			return nil
		}
		return fmt.Errorf("stopping ollama container: %w", err)
	}
	slog.Info("ollama container stopped")
	return nil
}

// IsOllamaRunning checks if the Ollama container exists and is running.
func (d *DockerRuntime) IsOllamaRunning(ctx context.Context) (bool, error) {
	info, err := d.client.ContainerInspect(ctx, OllamaContainerName)
	if err != nil {
		if strings.Contains(err.Error(), "No such container") || strings.Contains(err.Error(), "not found") {
			return false, nil
		}
		return false, fmt.Errorf("inspecting ollama container: %w", err)
	}
	return info.State.Running, nil
}

// hasGPU checks for NVIDIA GPU availability via nvidia-smi.
func hasGPU() bool {
	_, err := exec.LookPath("nvidia-smi")
	return err == nil
}

// HasGPUAvailable is an exported wrapper for hasGPU, used by the API layer.
func HasGPUAvailable() bool {
	return hasGPU()
}
