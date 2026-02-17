# AgentCrew API

Orchestration backend for multi-agent AI teams. AgentCrew deploys and manages teams of Claude Code agents that collaborate through NATS messaging, with support for both Docker and Kubernetes runtimes.

## Architecture

```
┌─────────────┐       ┌──────────┐       ┌───────────────────────────────┐
│  Frontend /  │       │          │       │  Agent Container / Pod        │
│  API Client  │──────▶│  API     │──────▶│  ┌─────────┐  ┌───────────┐ │
│              │  HTTP │  Server  │       │  │ Sidecar  │──│ Claude    │ │
└─────────────┘       │          │       │  │ (Go)     │  │ Code CLI  │ │
                      └────┬─────┘       │  └────┬─────┘  └───────────┘ │
                           │             └───────┼─────────────────────┘
                           │                     │
                      ┌────▼─────────────────────▼──┐
                      │         NATS Server          │
                      │     (JetStream enabled)      │
                      └─────────────────────────────┘
```

- **API Server** (`cmd/api`) — Fiber-based REST API that manages team lifecycles, agent deployments, and chat routing
- **Agent Sidecar** (`cmd/sidecar`) — Runs inside each agent container, bridging NATS messages to the Claude Code CLI process
- **NATS** — Message bus for real-time communication between the API server and agents

## Tech Stack

| Component | Technology |
|-----------|-----------|
| Language | Go 1.25+ |
| HTTP Framework | [Fiber](https://gofiber.io/) v2 |
| Database | SQLite via [GORM](https://gorm.io/) |
| Messaging | [NATS](https://nats.io/) with JetStream |
| Container Runtimes | Docker Engine API, Kubernetes client-go |
| WebSocket | Fiber WebSocket middleware |

## Quick Start

### Using Docker Compose

```bash
# Clone the repository
git clone https://github.com/helmcode/agent_crew_api.git
cd agent_crew_api

# Copy environment template
cp .env.example .env

# Set your Anthropic API key
export ANTHROPIC_API_KEY=your-api-key-here

# Start all services (API + NATS)
docker compose up -d

# Verify the API is running
curl http://localhost:8080/health
```

### Local Development

```bash
# Build both binaries
make build-all

# Run tests
make test

# Lint
make lint

# Build Docker images
make build-images

# Clean build artifacts
make clean
```

## API Endpoints

### Health

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/health` | Health check |

### Teams

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/teams` | List all teams |
| `POST` | `/api/teams` | Create a team (optionally with agents) |
| `GET` | `/api/teams/:id` | Get a team by ID |
| `PUT` | `/api/teams/:id` | Update a team |
| `DELETE` | `/api/teams/:id` | Delete a team |
| `POST` | `/api/teams/:id/deploy` | Deploy team infrastructure and agents |
| `POST` | `/api/teams/:id/stop` | Stop and teardown team |

### Agents

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/teams/:id/agents` | List agents in a team |
| `POST` | `/api/teams/:id/agents` | Add an agent to a team |
| `GET` | `/api/teams/:id/agents/:agentId` | Get an agent |
| `PUT` | `/api/teams/:id/agents/:agentId` | Update an agent |
| `DELETE` | `/api/teams/:id/agents/:agentId` | Remove an agent |

### Chat

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/teams/:id/chat` | Send a chat message to the team |
| `GET` | `/api/teams/:id/messages` | Get chat message history |

### Settings

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/settings` | Get application settings |
| `PUT` | `/api/settings` | Update a setting |

### WebSocket

| Path | Description |
|------|-------------|
| `ws://host/ws/teams/:id/logs` | Stream agent logs in real-time |
| `ws://host/ws/teams/:id/activity` | Stream team activity events |

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `ANTHROPIC_API_KEY` | *(required)* | API key for Claude Code agents |
| `NATS_AUTH_TOKEN` | *(optional)* | NATS authentication token |
| `NATS_URL` | `nats://nats:4222` | NATS server URL (sidecar) |
| `DATABASE_PATH` | `agentcrew.db` | SQLite database file path |
| `LISTEN_ADDR` | `:8080` | HTTP server listen address |
| `RUNTIME` | `docker` | Container runtime: `docker` or `kubernetes` |
| `KUBECONFIG` | `~/.kube/config` | Kubeconfig path (Kubernetes runtime only) |

## Runtime Support

AgentCrew supports two container runtimes, selected via the `RUNTIME` environment variable:

### Docker (default)

Each team gets a Docker network, workspace volume, and NATS container. Agents run as Docker containers attached to the team network.

### Kubernetes

Each team gets a Kubernetes namespace with a workspace PVC, NATS Deployment + Service, and API key Secret. Agents run as Pods in the team namespace. Supports both in-cluster and kubeconfig-based authentication.

## Project Structure

```
.
├── cmd/
│   ├── api/              # Orchestrator API server entrypoint
│   ├── sidecar/          # Agent sidecar entrypoint
│   └── testserver/       # Test server with mock runtime
├── internal/
│   ├── api/              # Fiber routes, handlers, middleware, DTOs
│   ├── claude/           # Claude Code process manager (sidecar)
│   ├── models/           # GORM models and SQLite database setup
│   ├── nats/             # NATS client wrapper for pub/sub messaging
│   ├── permissions/      # Permission gate logic for agent actions
│   ├── protocol/         # Shared message types (JSON protocol)
│   └── runtime/          # Container runtime interface (Docker, Kubernetes)
├── build/
│   ├── api/              # API server Dockerfile
│   └── agent/            # Agent container Dockerfile
├── docker-compose.yml    # Local development stack
├── Makefile              # Build, test, and lint commands
├── go.mod
└── go.sum
```

## License

See [LICENSE](LICENSE) for details.
