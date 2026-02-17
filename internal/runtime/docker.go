package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

// validNameRe validates Docker-safe names: lowercase alphanumeric, hyphens, underscores.
var validNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// validateName ensures a name is safe for use in Docker resource names.
func validateName(name string) error {
	if !validNameRe.MatchString(name) {
		return fmt.Errorf("invalid name %q: must match %s", name, validNameRe.String())
	}
	return nil
}

const (
	defaultAgentImage = "ghcr.io/helmcode/agent-crew-agent:latest"
	natsImage         = "nats:2.10-alpine"
	labelTeam         = "agentcrew.team"
	labelAgent        = "agentcrew.agent"
	labelRole         = "agentcrew.role"
)

// DockerRuntime implements AgentRuntime using the Docker Engine API.
type DockerRuntime struct {
	client *client.Client
}

// NewDockerRuntime creates a DockerRuntime using the default Docker client from env.
func NewDockerRuntime() (*DockerRuntime, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("creating docker client: %w", err)
	}
	return &DockerRuntime{client: cli}, nil
}

func teamNetworkName(teamName string) string { return "team-" + teamName }
func teamVolumeName(teamName string) string  { return "team-" + teamName + "-workspace" }
func natsContainerName(teamName string) string {
	return "team-" + teamName + "-nats"
}
func agentContainerName(teamName, name string) string {
	return fmt.Sprintf("team-%s-%s", teamName, name)
}

// DeployInfra creates the shared Docker network, NATS container, and workspace volume.
func (d *DockerRuntime) DeployInfra(ctx context.Context, config InfraConfig) error {
	if err := validateName(config.TeamName); err != nil {
		return fmt.Errorf("invalid team name: %w", err)
	}
	netName := teamNetworkName(config.TeamName)
	slog.Info("deploying team infrastructure", "team", config.TeamName, "network", netName)

	// Create network.
	_, err := d.client.NetworkCreate(ctx, netName, network.CreateOptions{
		Labels: map[string]string{labelTeam: config.TeamName},
	})
	if err != nil {
		return fmt.Errorf("creating network %s: %w", netName, err)
	}

	// Create workspace volume.
	volName := teamVolumeName(config.TeamName)
	_, err = d.client.VolumeCreate(ctx, volume.CreateOptions{
		Name:   volName,
		Labels: map[string]string{labelTeam: config.TeamName},
	})
	if err != nil {
		return fmt.Errorf("creating volume %s: %w", volName, err)
	}

	// Start NATS container.
	if config.NATSEnabled {
		if err := d.startNATS(ctx, config.TeamName, netName); err != nil {
			return fmt.Errorf("starting nats: %w", err)
		}
	}

	slog.Info("team infrastructure deployed", "team", config.TeamName)
	return nil
}

func (d *DockerRuntime) startNATS(ctx context.Context, teamName, netName string) error {
	containerName := natsContainerName(teamName)

	// Pull NATS image.
	reader, err := d.client.ImagePull(ctx, natsImage, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pulling nats image: %w", err)
	}
	defer reader.Close()
	_, _ = io.Copy(io.Discard, reader)

	// Build NATS command with JetStream and auth token.
	natsCmd := []string{"--jetstream"}
	if token := os.Getenv("NATS_AUTH_TOKEN"); token != "" {
		natsCmd = append(natsCmd, "--auth", token)
	} else {
		slog.Warn("NATS_AUTH_TOKEN not set, NATS running without authentication")
	}

	resp, err := d.client.ContainerCreate(ctx,
		&container.Config{
			Image: natsImage,
			Cmd:   natsCmd,
			ExposedPorts: nat.PortSet{
				"4222/tcp": struct{}{},
			},
			Labels: map[string]string{
				labelTeam: teamName,
				labelRole: "nats",
			},
		},
		&container.HostConfig{},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				netName: {},
			},
		},
		nil,
		containerName,
	)
	if err != nil {
		return fmt.Errorf("creating nats container: %w", err)
	}

	if err := d.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("starting nats container: %w", err)
	}

	slog.Info("nats container started", "id", resp.ID, "name", containerName)
	return nil
}

// DeployAgent creates and starts an agent container.
func (d *DockerRuntime) DeployAgent(ctx context.Context, config AgentConfig) (*AgentInstance, error) {
	if err := validateName(config.TeamName); err != nil {
		return nil, fmt.Errorf("invalid team name: %w", err)
	}
	if err := validateName(config.Name); err != nil {
		return nil, fmt.Errorf("invalid agent name: %w", err)
	}
	img := config.Image
	if img == "" {
		img = defaultAgentImage
	}

	containerName := agentContainerName(config.TeamName, config.Name)
	netName := teamNetworkName(config.TeamName)
	volName := teamVolumeName(config.TeamName)

	slog.Info("deploying agent", "agent", config.Name, "team", config.TeamName, "image", img)

	// Pull image.
	reader, err := d.client.ImagePull(ctx, img, image.PullOptions{})
	if err != nil {
		return nil, fmt.Errorf("pulling agent image: %w", err)
	}
	defer reader.Close()
	_, _ = io.Copy(io.Discard, reader)

	// Serialize permissions for env var.
	permJSON, _ := json.Marshal(config.Permissions)

	// Mount API key as a file instead of passing via env var to prevent
	// exposure through docker inspect and /proc/1/environ.
	secretPath, err := writeAPIKeyFile(containerName)
	if err != nil {
		return nil, fmt.Errorf("preparing api key secret: %w", err)
	}

	env := []string{
		"AGENT_NAME=" + config.Name,
		"TEAM_NAME=" + config.TeamName,
		"NATS_URL=" + config.NATSUrl,
		"AGENT_ROLE=" + config.Role,
		"AGENT_PERMISSIONS=" + string(permJSON),
		"ANTHROPIC_API_KEY_FILE=/run/secrets/anthropic_api_key",
	}

	// Resource limits.
	resources := container.Resources{}
	if config.Resources.Memory != "" {
		resources.Memory = parseMemoryLimit(config.Resources.Memory)
	}
	if config.Resources.CPU != "" {
		resources.NanoCPUs = parseCPULimit(config.Resources.CPU)
	}

	resp, err := d.client.ContainerCreate(ctx,
		&container.Config{
			Image: img,
			Env:   env,
			Labels: map[string]string{
				labelTeam:  config.TeamName,
				labelAgent: config.Name,
				labelRole:  config.Role,
			},
		},
		&container.HostConfig{
			Binds: []string{
				volName + ":/workspace",
				secretPath + ":/run/secrets/anthropic_api_key:ro",
			},
			Resources: resources,
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				netName: {},
			},
		},
		nil,
		containerName,
	)
	if err != nil {
		return nil, fmt.Errorf("creating agent container: %w", err)
	}

	if err := d.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return nil, fmt.Errorf("starting agent container: %w", err)
	}

	slog.Info("agent container started", "id", resp.ID, "agent", config.Name)
	return &AgentInstance{
		ID:     resp.ID,
		Name:   config.Name,
		Status: "running",
	}, nil
}

// StopAgent stops a running agent container.
func (d *DockerRuntime) StopAgent(ctx context.Context, id string) error {
	timeout := 30
	return d.client.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout})
}

// RemoveAgent removes an agent container.
func (d *DockerRuntime) RemoveAgent(ctx context.Context, id string) error {
	return d.client.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
}

// GetStatus inspects a container and returns its status.
func (d *DockerRuntime) GetStatus(ctx context.Context, id string) (*AgentStatus, error) {
	info, err := d.client.ContainerInspect(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("inspecting container %s: %w", id, err)
	}

	status := "stopped"
	if info.State.Running {
		status = "running"
	} else if info.State.ExitCode != 0 {
		status = "error"
	}

	startedAt, _ := time.Parse(time.RFC3339, info.State.StartedAt)

	return &AgentStatus{
		ID:        id,
		Name:      info.Name,
		Status:    status,
		StartedAt: startedAt,
	}, nil
}

// StreamLogs returns a reader for the container's log stream.
func (d *DockerRuntime) StreamLogs(ctx context.Context, id string) (io.ReadCloser, error) {
	return d.client.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
	})
}

// TeardownInfra removes all containers, the NATS container, network, and volume
// for a given team.
func (d *DockerRuntime) TeardownInfra(ctx context.Context, teamName string) error {
	slog.Info("tearing down team infrastructure", "team", teamName)

	// Find all containers for this team.
	containers, err := d.client.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", labelTeam+"="+teamName)),
	})
	if err != nil {
		return fmt.Errorf("listing team containers: %w", err)
	}

	// Stop and remove all team containers, cleaning up secret files.
	for _, c := range containers {
		slog.Info("removing container", "id", c.ID[:12], "names", c.Names)
		timeout := 10
		_ = d.client.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: &timeout})
		_ = d.client.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true})

		// Clean up API key secret file for agent containers.
		for _, name := range c.Names {
			removeAPIKeyFile(name)
		}
	}

	// Remove network.
	netName := teamNetworkName(teamName)
	if err := d.client.NetworkRemove(ctx, netName); err != nil {
		slog.Warn("failed to remove network", "network", netName, "error", err)
	}

	// Remove volume.
	volName := teamVolumeName(teamName)
	if err := d.client.VolumeRemove(ctx, volName, false); err != nil {
		slog.Warn("failed to remove volume", "volume", volName, "error", err)
	}

	slog.Info("team infrastructure torn down", "team", teamName)
	return nil
}

// apiKeySecretPath returns the deterministic path for an agent's API key secret file.
func apiKeySecretPath(containerName string) string {
	return filepath.Join(os.TempDir(), "agentcrew-apikey-"+containerName)
}

// writeAPIKeyFile creates a temporary file containing the API key with restrictive
// permissions. Uses a deterministic path based on container name for cleanup.
func writeAPIKeyFile(containerName string) (string, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("ANTHROPIC_API_KEY environment variable not set")
	}

	secretPath := apiKeySecretPath(containerName)
	if err := os.WriteFile(secretPath, []byte(apiKey), 0400); err != nil {
		return "", fmt.Errorf("writing api key file: %w", err)
	}

	return secretPath, nil
}

// removeAPIKeyFile removes the temporary API key file for a container.
func removeAPIKeyFile(containerName string) {
	if err := os.Remove(apiKeySecretPath(containerName)); err != nil && !os.IsNotExist(err) {
		slog.Warn("failed to remove api key file", "container", containerName, "error", err)
	}
}

// parseMemoryLimit converts a human-readable memory string (e.g. "512m", "1g")
// to bytes. Returns 0 if parsing fails.
func parseMemoryLimit(mem string) int64 {
	if len(mem) == 0 {
		return 0
	}
	var multiplier int64
	unit := mem[len(mem)-1]
	switch unit {
	case 'g', 'G':
		multiplier = 1024 * 1024 * 1024
	case 'm', 'M':
		multiplier = 1024 * 1024
	case 'k', 'K':
		multiplier = 1024
	default:
		return 0
	}
	numStr := mem[:len(mem)-1]
	var num int64
	for _, c := range numStr {
		if c < '0' || c > '9' {
			return 0
		}
		num = num*10 + int64(c-'0')
	}
	return num * multiplier
}

// parseCPULimit converts a CPU string (e.g. "0.5", "2") to nanoCPUs.
// Returns 0 if parsing fails.
func parseCPULimit(cpu string) int64 {
	var whole, frac int64
	var inFrac bool
	var fracDiv int64 = 1

	for _, c := range cpu {
		if c == '.' {
			inFrac = true
			continue
		}
		if c < '0' || c > '9' {
			return 0
		}
		if inFrac {
			frac = frac*10 + int64(c-'0')
			fracDiv *= 10
		} else {
			whole = whole*10 + int64(c-'0')
		}
	}

	nanos := whole * 1_000_000_000
	if fracDiv > 0 {
		nanos += frac * 1_000_000_000 / fracDiv
	}
	return nanos
}
