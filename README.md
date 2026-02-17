# AgentCrew Backend

Go backend for the AgentCrew AI agent orchestration system.

## Architecture

- **Orchestrator API** (`cmd/api/`) — Fiber-based REST API that manages agent lifecycles
- **Agent Sidecar** (`cmd/sidecar/`) — Runs alongside Claude Code containers, bridging NATS messages to the Claude CLI process

## Internal Packages

| Package | Description |
|---------|-------------|
| `internal/models` | GORM models and SQLite database setup |
| `internal/protocol` | Shared message types for the JSON protocol |
| `internal/permissions` | Permission gate logic for agent actions |
| `internal/nats` | NATS client wrapper for pub/sub messaging |
| `internal/claude` | Claude Code process manager (sidecar) |
| `internal/runtime` | Container runtime interface (Docker) |
| `internal/api` | Fiber route handlers and middleware |

## Development

```bash
# Build both binaries
make build-all

# Run tests
make test

# Lint
make lint

# Clean build artifacts
make clean
```

## Requirements

- Go 1.23+
- CGO enabled (for SQLite)
- Docker (for runtime operations)
- NATS server (for messaging)
