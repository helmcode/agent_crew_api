# AgentCrew API - AI Assistant Context

## Project Overview

AgentCrew API is a Go backend that orchestrates multi-agent AI teams. It deploys teams of Claude Code agents that communicate through NATS messaging. The API manages agent lifecycles across Docker and Kubernetes runtimes.

## Architecture

### Binaries

- **`cmd/api`** — Orchestrator API server (Fiber HTTP + WebSocket). Manages teams, agents, deployments, and chat routing. Stores state in SQLite via GORM.
- **`cmd/sidecar`** — Agent sidecar process. Runs inside each agent container alongside the Claude Code CLI. Bridges NATS messages to/from the Claude process.
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

## Build Commands

```bash
make build-all        # Build api and sidecar binaries to bin/
make build-api        # Build only the API server
make build-sidecar    # Build only the sidecar
make build-images     # Build Docker images for both
make test             # Run all tests with race detector
make lint             # Run golangci-lint
make clean            # Remove build artifacts
```

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
| `internal/api/routes.go` | All HTTP and WebSocket route definitions |
| `internal/api/handlers_teams.go` | Team CRUD and deploy/stop handlers |
| `internal/api/dto.go` | Request/response DTOs and name validation |
| `internal/api/server.go` | Server struct, middleware setup |
| `internal/models/models.go` | GORM model definitions |
| `internal/protocol/messages.go` | NATS message types |
| `cmd/api/main.go` | API server entrypoint with runtime selection |
| `cmd/sidecar/main.go` | Sidecar entrypoint |
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
