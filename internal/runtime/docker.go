package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

// sanitizeName converts a display name into a Docker-safe slug using the shared
// SanitizeName function from the api package. This is a runtime-local wrapper
// to keep calling code clean.
func sanitizeName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, " ", "-")
	// Strip anything not [a-z0-9_-].
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	s = b.String()
	// Collapse consecutive hyphens.
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-")
	if len(s) > 62 {
		s = s[:62]
		s = strings.TrimRight(s, "-")
	}
	if s == "" {
		s = "team"
	}
	return s
}

// registryAuth returns the base64-encoded RegistryAuth string for pulling an image.
// It reads credentials from the Docker config.json ($DOCKER_CONFIG or $HOME/.docker).
// Returns empty string if no credentials are found (falls back to unauthenticated pull).
func registryAuth(imageName string) string {
	configDir := os.Getenv("DOCKER_CONFIG")
	if configDir == "" {
		home, _ := os.UserHomeDir()
		configDir = filepath.Join(home, ".docker")
	}
	configPath := filepath.Join(configDir, "config.json")

	data, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}

	var dockerConfig struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(data, &dockerConfig); err != nil {
		return ""
	}

	// Extract registry hostname from image name.
	registry := "docker.io"
	if parts := strings.SplitN(imageName, "/", 2); len(parts) == 2 && strings.ContainsAny(parts[0], ".:") {
		registry = parts[0]
	}

	entry, ok := dockerConfig.Auths[registry]
	if !ok || entry.Auth == "" {
		return ""
	}

	// The config.json "auth" field is base64(username:password).
	// The Docker API expects base64(JSON{"username","password"}).
	decoded, err := base64.StdEncoding.DecodeString(entry.Auth)
	if err != nil {
		return ""
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return ""
	}

	authJSON, _ := json.Marshal(map[string]string{
		"username": parts[0],
		"password": parts[1],
	})
	return base64.URLEncoding.EncodeToString(authJSON)
}

// pullImageIfNeeded implements an IfNotPresent pull policy: it checks if the
// image exists locally first and only pulls from the registry when it is missing.
func (d *DockerRuntime) pullImageIfNeeded(ctx context.Context, img string) error {
	_, _, err := d.client.ImageInspectWithRaw(ctx, img)
	if err == nil {
		slog.Info("image already present locally, skipping pull", "image", img)
		return nil
	}

	slog.Info("pulling image", "image", img)
	reader, err := d.client.ImagePull(ctx, img, image.PullOptions{
		RegistryAuth: registryAuth(img),
	})
	if err != nil {
		return fmt.Errorf("pulling image %s: %w", img, err)
	}
	defer reader.Close()
	_, _ = io.Copy(io.Discard, reader)
	return nil
}

// GetNATSURL returns the NATS URL for a team in Docker runtime (internal container network).
func (d *DockerRuntime) GetNATSURL(teamName string) string {
	return "nats://team-" + sanitizeName(teamName) + "-nats:4222"
}

// GetNATSConnectURL returns a host-accessible NATS URL by inspecting the container's
// mapped port. This allows the API server to connect to the team's NATS from outside
// the Docker network. When the API itself runs inside a Docker container,
// it uses host.docker.internal instead of 127.0.0.1.
func (d *DockerRuntime) GetNATSConnectURL(ctx context.Context, teamName string) (string, error) {
	containerName := natsContainerName(sanitizeName(teamName))
	info, err := d.client.ContainerInspect(ctx, containerName)
	if err != nil {
		return "", fmt.Errorf("inspecting nats container %s: %w", containerName, err)
	}

	bindings, ok := info.NetworkSettings.Ports["4222/tcp"]
	if !ok || len(bindings) == 0 {
		return "", fmt.Errorf("nats container %s has no port binding for 4222/tcp", containerName)
	}

	hostPort := bindings[0].HostPort
	host := natsHostAddress()
	url := "nats://" + host + ":" + hostPort
	slog.Info("resolved team NATS connect URL", "team", teamName, "container", containerName, "url", url)
	return url, nil
}

// natsHostAddress returns the address to reach Docker host-mapped ports.
// Inside a container it uses host.docker.internal; on the host it uses 127.0.0.1.
func natsHostAddress() string {
	// /.dockerenv exists inside Docker containers.
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "host.docker.internal"
	}
	return "127.0.0.1"
}

// DockerRuntime implements AgentRuntime using the Docker Engine API.
type DockerRuntime struct {
	client     *client.Client
	agentImage string
}

// NewDockerRuntime creates a DockerRuntime using the default Docker client from env.
func NewDockerRuntime() (*DockerRuntime, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("creating docker client: %w", err)
	}

	agentImage := os.Getenv("AGENT_IMAGE")
	if agentImage == "" {
		agentImage = DefaultAgentImage
	}

	return &DockerRuntime{client: cli, agentImage: agentImage}, nil
}

func teamNetworkName(teamName string) string { return "team-" + teamName }
func teamVolumeName(teamName string) string  { return "team-" + teamName + "-workspace" }
func natsContainerName(teamName string) string {
	return "team-" + teamName + "-nats"
}
func agentContainerName(teamName, name string) string {
	return fmt.Sprintf("team-%s-%s", teamName, name)
}

// isAlreadyExistsErr checks if a Docker API error indicates the resource already exists.
func isAlreadyExistsErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "already exists") || strings.Contains(msg, "already in use")
}

// DeployInfra creates the shared Docker network, NATS container, and workspace volume.
func (d *DockerRuntime) DeployInfra(ctx context.Context, config InfraConfig) error {
	config.TeamName = sanitizeName(config.TeamName)
	netName := teamNetworkName(config.TeamName)
	slog.Info("deploying team infrastructure", "team", config.TeamName, "network", netName)

	// Create network (idempotent).
	_, err := d.client.NetworkCreate(ctx, netName, network.CreateOptions{
		Labels: map[string]string{LabelTeam: config.TeamName},
	})
	if err != nil && !isAlreadyExistsErr(err) {
		return fmt.Errorf("creating network %s: %w", netName, err)
	}

	// Create workspace volume (idempotent).
	volName := teamVolumeName(config.TeamName)
	_, err = d.client.VolumeCreate(ctx, volume.CreateOptions{
		Name:   volName,
		Labels: map[string]string{LabelTeam: config.TeamName},
	})
	if err != nil && !isAlreadyExistsErr(err) {
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

	// Check if NATS container already exists.
	info, err := d.client.ContainerInspect(ctx, containerName)
	if err == nil {
		bindings := info.NetworkSettings.Ports["4222/tcp"]
		hasPortBinding := len(bindings) > 0 && bindings[0].HostPort != ""

		if info.State.Running && hasPortBinding {
			slog.Info("nats container already running with port binding", "name", containerName)
			return nil
		}

		// Container exists but either not running or missing port binding — recreate.
		if info.State.Running && !hasPortBinding {
			slog.Info("nats container missing port binding, recreating", "name", containerName)
		} else {
			slog.Info("removing stale nats container", "name", containerName)
		}
		_ = d.client.ContainerRemove(ctx, containerName, container.RemoveOptions{Force: true})
	}

	// Pull NATS image if not present locally.
	if err := d.pullImageIfNeeded(ctx, NATSImage); err != nil {
		return fmt.Errorf("nats image: %w", err)
	}

	// Build NATS command with JetStream and auth token.
	natsCmd := []string{"--jetstream"}
	if token := os.Getenv("NATS_AUTH_TOKEN"); token != "" {
		natsCmd = append(natsCmd, "--auth", token)
	} else {
		slog.Warn("NATS_AUTH_TOKEN not set, NATS running without authentication")
	}

	resp, err := d.client.ContainerCreate(ctx,
		&container.Config{
			Image: NATSImage,
			Cmd:   natsCmd,
			ExposedPorts: nat.PortSet{
				"4222/tcp": struct{}{},
			},
			Labels: map[string]string{
				LabelTeam: teamName,
				LabelRole: "nats",
			},
		},
		&container.HostConfig{
			PortBindings: nat.PortMap{
				"4222/tcp": []nat.PortBinding{
					{HostIP: "127.0.0.1", HostPort: "0"}, // random available port
				},
			},
			RestartPolicy: container.RestartPolicy{
				Name:              "on-failure",
				MaximumRetryCount: 5,
			},
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
	config.TeamName = sanitizeName(config.TeamName)
	config.Name = sanitizeName(config.Name)
	img := config.Image
	if img == "" {
		img = d.agentImage
	}

	// Validate workspace path exists on the host before attempting to mount it.
	if config.WorkspacePath != "" {
		info, err := os.Stat(config.WorkspacePath)
		if err != nil {
			return nil, fmt.Errorf("workspace path %q does not exist: %w", config.WorkspacePath, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("workspace path %q is not a directory", config.WorkspacePath)
		}
	}

	containerName := agentContainerName(config.TeamName, config.Name)
	netName := teamNetworkName(config.TeamName)
	volName := teamVolumeName(config.TeamName)

	slog.Info("deploying agent", "agent", config.Name, "team", config.TeamName, "image", img)

	// Remove any stale container with the same name from a previous failed deploy.
	_ = d.client.ContainerRemove(ctx, containerName, container.RemoveOptions{Force: true})

	// Pull image if not present locally (IfNotPresent policy).
	if err := d.pullImageIfNeeded(ctx, img); err != nil {
		return nil, fmt.Errorf("agent image: %w", err)
	}

	// Serialize permissions for env var.
	permJSON, _ := json.Marshal(config.Permissions)

	// Read API key: prefer config.Env (from Settings DB), fall back to process env.
	apiKey := config.Env["ANTHROPIC_API_KEY"]
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}

	// Read OAuth token: CLAUDE_CODE_OAUTH_TOKEN or its alias ANTHROPIC_AUTH_TOKEN.
	oauthToken := config.Env["CLAUDE_CODE_OAUTH_TOKEN"]
	if oauthToken == "" {
		oauthToken = os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
	}
	if oauthToken == "" {
		oauthToken = config.Env["ANTHROPIC_AUTH_TOKEN"]
	}
	if oauthToken == "" {
		oauthToken = os.Getenv("ANTHROPIC_AUTH_TOKEN")
	}

	// Require at least one authentication method.
	if apiKey == "" && oauthToken == "" {
		return nil, fmt.Errorf("no auth configured: set ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN in Settings")
	}

	// Read NATS auth token: same token used to start the NATS container.
	natsToken := os.Getenv("NATS_AUTH_TOKEN")

	env := []string{
		"AGENT_NAME=" + config.Name,
		"TEAM_NAME=" + config.TeamName,
		"NATS_URL=" + config.NATSUrl,
		"AGENT_ROLE=" + config.Role,
		"AGENT_PERMISSIONS=" + string(permJSON),
	}

	if apiKey != "" {
		env = append(env, "ANTHROPIC_API_KEY="+apiKey)
	}
	if oauthToken != "" {
		env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)
	}
	if natsToken != "" {
		env = append(env, "NATS_AUTH_TOKEN="+natsToken)
	}

	// Set WORKSPACE_PATH env var when a host workspace is mounted.
	if config.WorkspacePath != "" {
		env = append(env, "WORKSPACE_PATH=/workspace")
	}

	// Pass CLAUDE.md content via env var so the sidecar can write it at startup.
	// This works even when the API container has no access to the host workspace path.
	if config.ClaudeMD != "" {
		env = append(env, "AGENT_CLAUDE_MD="+config.ClaudeMD)
	}

	// Pass sub-agent file contents via env var so the sidecar can write them to
	// .claude/agents/. Required when using a Docker volume (no WorkspacePath).
	if len(config.SubAgentFiles) > 0 {
		filesJSON, _ := json.Marshal(config.SubAgentFiles)
		env = append(env, "AGENT_SUB_AGENT_FILES="+string(filesJSON))
	}

	// Forward remaining env vars from config.Env (e.g. AGENT_SKILLS_INSTALL)
	// that were not already handled above via specific logic.
	handledEnvKeys := map[string]bool{
		"ANTHROPIC_API_KEY":       true,
		"CLAUDE_CODE_OAUTH_TOKEN": true,
		"ANTHROPIC_AUTH_TOKEN":    true,
	}
	for k, v := range config.Env {
		if !handledEnvKeys[k] && v != "" {
			env = append(env, k+"="+v)
		}
	}

	// Resource limits.
	resources := container.Resources{}
	if config.Resources.Memory != "" {
		resources.Memory = parseMemoryLimit(config.Resources.Memory)
	}
	if config.Resources.CPU != "" {
		resources.NanoCPUs = parseCPULimit(config.Resources.CPU)
	}

	// Determine workspace bind: use host path (bind mount) if provided,
	// otherwise fall back to the shared Docker volume.
	binds := []string{}
	if config.WorkspacePath != "" {
		binds = append(binds, config.WorkspacePath+":/workspace")
	} else {
		binds = append(binds, volName+":/workspace")
	}

	resp, err := d.client.ContainerCreate(ctx,
		&container.Config{
			Image: img,
			Env:   env,
			Labels: map[string]string{
				LabelTeam:  config.TeamName,
				LabelAgent: config.Name,
				LabelRole:  config.Role,
			},
		},
		&container.HostConfig{
			Binds:     binds,
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
	teamName = sanitizeName(teamName)
	slog.Info("tearing down team infrastructure", "team", teamName)

	// Find all containers for this team.
	containers, err := d.client.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", LabelTeam+"="+teamName)),
	})
	if err != nil {
		return fmt.Errorf("listing team containers: %w", err)
	}

	// Stop and remove all team containers.
	for _, c := range containers {
		slog.Info("removing container", "id", c.ID[:12], "names", c.Names)
		timeout := 10
		_ = d.client.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: &timeout})
		_ = d.client.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true})
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
