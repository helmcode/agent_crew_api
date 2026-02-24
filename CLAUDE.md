# AgentCrew API - AI Assistant Context

## Project Overview

AgentCrew API is a Go backend that orchestrates multi-agent AI teams. It deploys teams of Claude Code agents that communicate through NATS messaging. The API manages agent lifecycles across Docker and Kubernetes runtimes.

## Architecture

### Binaries

- **`cmd/api`** — Orchestrator API server (Fiber HTTP + WebSocket). Manages teams, agents, deployments, and chat routing. Stores state in SQLite via GORM.
- **`cmd/sidecar`** — Agent sidecar process. Runs inside each agent container alongside the Claude Code CLI. Bridges NATS messages to/from the Claude process. Installs skills and validates workspace files before starting Claude.
- **`cmd/testserver`** — Test server with a mock runtime for integration testing without Docker/Kubernetes.

### Internal Packages

| Package | Purpose |
|---------|---------|
| `internal/api` | Fiber route handlers, middleware, DTOs, WebSocket handlers |
| `internal/runtime` | `AgentRuntime` interface with Docker and Kubernetes implementations |
| `internal/models` | GORM models (`Team`, `Agent`, `Setting`, `Message`) and SQLite setup |
| `internal/nats` | NATS client wrapper, pub/sub bridge |
| `internal/protocol` | Shared JSON message types and NATS channel naming |
| `internal/permissions` | Permission gate for agent actions (file access, command execution) |
| `internal/claude` | Claude Code CLI process manager, output stream parsing |

### Runtime Abstraction

The `AgentRuntime` interface (`internal/runtime/runtime.go`) defines the contract for managing agent lifecycles:

```go
type AgentRuntime interface {
    DeployInfra(ctx, InfraConfig) error
    DeployAgent(ctx, AgentConfig) (*AgentInstance, error)
    StopAgent(ctx, id) error
    RemoveAgent(ctx, id) error
    GetStatus(ctx, id) (*AgentStatus, error)
    StreamLogs(ctx, id) (io.ReadCloser, error)
    TeardownInfra(ctx, teamName) error
    GetNATSURL(teamName) string
}
```

Two implementations:
- **`DockerRuntime`** (`docker.go`) — Docker Engine API. Teams get networks + volumes + containers.
- **`K8sRuntime`** (`kubernetes.go`) — Kubernetes client-go. Teams get namespaces + PVCs + pods + services.

Runtime selection is done via the `RUNTIME` env var in `cmd/api/main.go`.

### Key Patterns

- **Fiber handlers** follow REST conventions with JSON request/response
- **GORM** with SQLite for persistence (teams, agents, messages, settings)
- **Async deployment**: `DeployTeam` returns immediately, deployment runs in a goroutine
- **NATS pub/sub** for real-time agent communication with JetStream persistence
- **WebSocket** endpoints for streaming logs and activity to the frontend
- **Name validation**: team/agent names must match `^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`
- **Unique agent names**: agent names must be unique within a team (case-insensitive, enforced in CreateTeam, CreateAgent, and UpdateAgent)

### Skills System

Skills extend agent capabilities via the `skills` CLI (`npx skills add`).

**Installation command:**
```bash
npx skills add <repo_url> --skill <skill_name> --agent claude-code -y
```

The `--agent claude-code` flag is required to create symlinks in `.claude/skills/` pointing to `.agents/skills/`, which is how Claude Code discovers installed skills.

**Two installation paths:**

1. **At deployment** (sidecar): Skills from all agents (leader + workers) are collected, deduplicated, passed via `AGENT_SKILLS_INSTALL` env var, and installed by the sidecar before Claude starts. Results are published via NATS as `skill_status` messages.

2. **Hot-install on running team** (API endpoint): `POST /api/teams/:id/agents/:agentId/skills/install` executes `npx skills add` inside the leader container and updates the agent's `skill_statuses` in the DB.

**Global (leader) skills:**
- Skills installed on the leader are global — available to all agents in the team.
- When a leader skill is installed, all worker sub-agent `.md` files are regenerated to include the global skill.
- `SubAgentInfo.GlobalSkills` carries leader skills to `GenerateSubAgentContent()`, which merges them with the worker's own skills (deduplicated).

**Key fields on Agent model:**
- `SubAgentSkills` (JSON) — configured skills (`[{repo_url, skill_name}]`), used for both leaders and workers
- `SkillStatuses` (JSON) — installation results (`[{name, status, error?}]`), updated by both the NATS relay and the hot-install endpoint

## Build Commands

```bash
make build-all          # Build api and sidecar binaries to bin/
make build-api          # Build only the API server
make build-sidecar      # Build only the sidecar (native, for local testing)
make build-sidecar-linux # Cross-compile sidecar for Linux (for Docker)
make build-agent-image  # Build agent Docker image (cross-compiles sidecar for Linux)
make build-images       # Build Docker images for both API and agent
make test               # Run all tests with race detector
make lint               # Run golangci-lint
make clean              # Remove build artifacts
```

**Important:** When building the agent image, always use `make build-agent-image` (not `make build-sidecar`). The agent image requires a Linux binary, and `build-agent-image` handles cross-compilation via `build-sidecar-linux`.

## Test Commands

```bash
# All tests
go test -v -race ./...

# Runtime tests only
go test -v -race ./internal/runtime/...

# API handler tests
go test -v -race ./internal/api/...

# With coverage
go test -v -race -cover ./...
```

## Important Files

| File | Description |
|------|-------------|
| `internal/runtime/runtime.go` | `AgentRuntime` interface, shared types and constants |
| `internal/runtime/docker.go` | Docker runtime implementation |
| `internal/runtime/kubernetes.go` | Kubernetes runtime implementation |
| `internal/runtime/workspace.go` | Agent workspace setup, sub-agent `.md` generation, global skills merging |
| `internal/api/routes.go` | All HTTP and WebSocket route definitions |
| `internal/api/handlers_teams.go` | Team CRUD, deploy/stop, skill collection from all agents |
| `internal/api/handlers_agents.go` | Agent CRUD, hot skill installation, skill_statuses update |
| `internal/api/handlers_relay.go` | NATS relay, persists skill_statuses from sidecar reports |
| `internal/api/dto.go` | Request/response DTOs and name validation |
| `internal/api/server.go` | Server struct, middleware setup |
| `internal/models/models.go` | GORM model definitions |
| `internal/protocol/messages.go` | NATS message types (SkillConfig, SkillInstallResult, etc.) |
| `cmd/api/main.go` | API server entrypoint with runtime selection |
| `cmd/sidecar/main.go` | Sidecar entrypoint, workspace validation |
| `cmd/sidecar/skills.go` | Skill installation logic (`installSkills`, `publishSkillStatus`) |
| `build/agent/Dockerfile` | Agent container image (Node.js + Claude Code CLI + skills CLI + sidecar) |
| `docker-compose.yml` | Local dev stack (API + NATS) |
| `Makefile` | Build, test, lint targets |

## Docker Compose

The `docker-compose.yml` runs the API server and NATS for local development. The API server gets Docker socket access to manage agent containers. Set `ANTHROPIC_API_KEY` and optionally `NATS_AUTH_TOKEN` before starting.

## Kubernetes Runtime

When `RUNTIME=kubernetes`, the API server uses client-go to manage resources:
- **Namespace** per team: `agentcrew-{teamName}`
- **PVC** for shared workspace: `workspace`
- **NATS Deployment + Service**: `nats` (ClusterIP:4222)
- **Pod** per agent: `agent-{agentName}`
- **Secret** for API key: `anthropic-api-key`
- Agent IDs are encoded as `namespace/podName`

The API server needs a ServiceAccount with permissions to manage namespaces, pods, services, deployments, PVCs, and secrets.
