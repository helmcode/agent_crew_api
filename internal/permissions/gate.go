// Package permissions implements the permission gate logic for agent actions.
package permissions

// PermissionConfig defines what tools, commands, and paths an agent is allowed to use.
type PermissionConfig struct {
	AllowedTools    []string `json:"allowed_tools"`
	AllowedCommands []string `json:"allowed_commands"`
	DeniedCommands  []string `json:"denied_commands"`
	FilesystemScope string   `json:"filesystem_scope"`
}

// Decision represents the outcome of a permission evaluation.
type Decision struct {
	Allowed bool
	Reason  string
}

// Allow returns a Decision that permits the action.
func Allow() Decision {
	return Decision{Allowed: true}
}

// Deny returns a Decision that blocks the action with the given reason.
func Deny(reason string) Decision {
	return Decision{Allowed: false, Reason: reason}
}

// Gate evaluates tool/command requests against a PermissionConfig.
type Gate struct {
	config PermissionConfig
}

// NewGate creates a Gate with the given configuration.
func NewGate(config PermissionConfig) *Gate {
	return &Gate{config: config}
}

// Evaluate checks whether the given tool, command, and filesystem paths are permitted.
//
// Evaluation order:
//  1. Tool must be in AllowedTools.
//  2. Command must NOT match any DeniedCommands pattern (deny takes precedence).
//  3. Command must match at least one AllowedCommands pattern (if AllowedCommands is non-empty).
//  4. All paths must be within FilesystemScope.
func (g *Gate) Evaluate(toolName string, command string, paths []string) Decision {
	// Step 1: check tool allowlist.
	if !g.isToolAllowed(toolName) {
		return Deny("tool not allowed: " + toolName)
	}

	// Step 2: check denied commands (deny takes precedence).
	if command != "" {
		for _, pattern := range g.config.DeniedCommands {
			if MatchPattern(pattern, command) {
				return Deny("command denied by pattern: " + pattern)
			}
		}
	}

	// Step 3: check allowed commands.
	if command != "" && len(g.config.AllowedCommands) > 0 {
		allowed := false
		for _, pattern := range g.config.AllowedCommands {
			if MatchPattern(pattern, command) {
				allowed = true
				break
			}
		}
		if !allowed {
			return Deny("command not in allowed list: " + command)
		}
	}

	// Step 4: check filesystem scope.
	if g.config.FilesystemScope != "" {
		for _, p := range paths {
			if !IsPathInScope(p, g.config.FilesystemScope) {
				return Deny("path outside allowed scope: " + p)
			}
		}
	}

	return Allow()
}

func (g *Gate) isToolAllowed(toolName string) bool {
	if len(g.config.AllowedTools) == 0 {
		return false // fail-closed: no allowlist means no tools are permitted
	}
	for _, t := range g.config.AllowedTools {
		if t == toolName {
			return true
		}
	}
	return false
}
