# Agent image: Node.js runtime with Claude Code CLI and Go sidecar binary
FROM node:20-slim

# Install common tools needed by agents
RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    curl \
    jq \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Install Claude Code CLI
RUN npm install -g @anthropic-ai/claude-code

# Create non-root user for running the agent
RUN groupadd -r agentcrew && useradd -r -g agentcrew -m agentcrew

# Copy the sidecar binary (built externally with Go)
COPY sidecar /usr/local/bin/agent-sidecar
RUN chmod +x /usr/local/bin/agent-sidecar

# Create workspace directory for agent operations
RUN mkdir -p /workspace && chown agentcrew:agentcrew /workspace

WORKDIR /workspace

USER agentcrew

ENTRYPOINT ["agent-sidecar"]
