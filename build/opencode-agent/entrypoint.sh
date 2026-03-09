#!/bin/sh
set -e

# Fix workspace permissions on Linux when a host directory is bind-mounted.
#
# On Linux, Docker runs natively and respects real UID/GID ownership.
# The workspace directory on the host is typically owned by the host user
# (e.g., uid 1000), but the container's agentcrew user has uid 999.
# Without this fix, the sidecar gets "permission denied" errors.
#
# On macOS/Windows, Docker Desktop uses a VM with transparent file sharing,
# so permissions are handled automatically and this is a no-op.
#
# Strategy: detect the workspace owner and run the sidecar as that UID/GID.
# This avoids changing host file ownership while giving the agent full access.

if [ "$(id -u)" = "0" ]; then
  WORKSPACE_UID=$(stat -c '%u' /workspace 2>/dev/null || echo 0)
  WORKSPACE_GID=$(stat -c '%g' /workspace 2>/dev/null || echo 0)

  # If workspace is owned by root (Docker volume or root-owned dir),
  # run as the agentcrew user and ensure it owns /workspace.
  if [ "$WORKSPACE_UID" = "0" ]; then
    chown -R agentcrew:agentcrew /workspace
    exec gosu agentcrew agent-sidecar "$@"
  fi

  # Workspace is owned by a non-root host user. Ensure .claude config
  # directories exist and are writable, then run as that user.
  mkdir -p /workspace/.claude/agents /workspace/.claude/skills
  chown -R "$WORKSPACE_UID:$WORKSPACE_GID" /workspace/.claude

  exec gosu "$WORKSPACE_UID:$WORKSPACE_GID" agent-sidecar "$@"
fi

# Not running as root (e.g., Kubernetes with securityContext).
# Just exec the sidecar directly.
exec agent-sidecar "$@"
