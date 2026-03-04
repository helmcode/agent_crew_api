# Agent image: Node.js runtime with Claude Code CLI and Go sidecar binary
FROM node:20-slim

# Install common tools needed by agents (build-essential for Python C extensions used by MCP servers)
RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    curl \
    jq \
    ca-certificates \
    build-essential \
    && rm -rf /var/lib/apt/lists/*

# Install Claude Code CLI and skills package manager
RUN npm install -g @anthropic-ai/claude-code skills

# Install uv (Python package manager, includes uvx) for MCP servers that use Python
RUN curl -LsSf https://astral.sh/uv/install.sh | env UV_INSTALL_DIR=/usr/local/bin sh

# Create non-root user for running the agent
RUN groupadd -r agentcrew && useradd -r -g agentcrew -m agentcrew

# Copy the sidecar binary for the target architecture (built externally with Go)
ARG TARGETARCH
COPY sidecar-${TARGETARCH} /usr/local/bin/agent-sidecar
RUN chmod +x /usr/local/bin/agent-sidecar

# Create workspace directory for agent operations
RUN mkdir -p /workspace && chown agentcrew:agentcrew /workspace

WORKDIR /workspace

USER agentcrew

ENTRYPOINT ["agent-sidecar"]
