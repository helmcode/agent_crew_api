package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/helmcode/agent-crew/internal/protocol"
)

// SubAgentInfo holds the metadata needed to generate a sub-agent file
// at .claude/agents/{name}.md with YAML frontmatter.
type SubAgentInfo struct {
	Name         string
	Description  string
	Model        string
	Skills       json.RawMessage
	GlobalSkills json.RawMessage // Leader skills shared across all agents.
	ClaudeMD     string          // Body content written after the YAML frontmatter.
}

// TeamMemberInfo describes a teammate for inclusion in the leader's CLAUDE.md.
type TeamMemberInfo struct {
	Name      string
	Role      string
	Specialty string
}

// AgentWorkspaceInfo holds the metadata needed to generate an agent's CLAUDE.md.
type AgentWorkspaceInfo struct {
	Name         string
	Role         string
	Specialty    string
	SystemPrompt string
	ClaudeMD     string // Raw CLAUDE.md content; if set, used instead of GenerateClaudeMD.
	Skills       json.RawMessage
	TeamMembers  []TeamMemberInfo
}

// SetupAgentWorkspace creates the .claude directory under workspacePath and
// writes a CLAUDE.md with the agent's configuration. The file is written to
// {workspacePath}/.claude/CLAUDE.md so the Claude Code CLI picks it up as the
// workspace-level configuration. Returns the path to the .claude directory.
func SetupAgentWorkspace(workspacePath string, agent AgentWorkspaceInfo) (string, error) {
	claudeDir := filepath.Join(workspacePath, ".claude")

	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		return "", fmt.Errorf("creating .claude dir %s: %w", claudeDir, err)
	}

	// Use raw ClaudeMD content if provided; otherwise fall back to generating it.
	claudeMD := agent.ClaudeMD
	if claudeMD == "" {
		claudeMD = GenerateClaudeMD(agent)
	}
	claudePath := filepath.Join(claudeDir, "CLAUDE.md")

	if err := os.WriteFile(claudePath, []byte(claudeMD), 0644); err != nil {
		return "", fmt.Errorf("writing CLAUDE.md for agent %s: %w", agent.Name, err)
	}

	return claudeDir, nil
}

// AgentClaudeDir returns the host path for an agent's .claude directory
// without creating it. Used by runtimes to compute mount paths.
func AgentClaudeDir(workspacePath, agentName string) string {
	return filepath.Join(workspacePath, ".claude", sanitizeName(agentName))
}

// GenerateClaudeMD produces the CLAUDE.md content for an agent.
func GenerateClaudeMD(agent AgentWorkspaceInfo) string {
	var b strings.Builder

	b.WriteString("# Agent: " + agent.Name + "\n\n")

	b.WriteString("## Role\n")
	if agent.Role != "" {
		b.WriteString(agent.Role + "\n\n")
	} else {
		b.WriteString("worker\n\n")
	}

	if agent.Specialty != "" {
		b.WriteString("## Specialty\n")
		b.WriteString(agent.Specialty + "\n\n")
	}

	if agent.SystemPrompt != "" {
		b.WriteString("## Instructions\n")
		b.WriteString(agent.SystemPrompt + "\n\n")
	}

	skills := formatSkills(agent.Skills)
	if skills != "" {
		b.WriteString("## Skills\n")
		b.WriteString(skills + "\n")
	}

	// For leaders, add team roster and delegation protocol.
	if agent.Role == "leader" && len(agent.TeamMembers) > 0 {
		b.WriteString("## Team Members\n\n")
		b.WriteString("You are the team leader. The following agents are available for task delegation:\n\n")
		for _, m := range agent.TeamMembers {
			b.WriteString("- **" + m.Name + "**")
			if m.Role != "" {
				b.WriteString(" (role: " + m.Role + ")")
			}
			if m.Specialty != "" {
				b.WriteString(" — " + m.Specialty)
			}
			b.WriteString("\n")
		}

		b.WriteString("\n## Delegation Protocol\n\n")
		b.WriteString("To delegate tasks to team members, use the following format in your response:\n\n")
		b.WriteString("```\n[TASK:agent-name]\nYour instruction for the agent here.\n[/TASK]\n```\n\n")
		b.WriteString("You can delegate to multiple agents in a single response. ")
		b.WriteString("Use the exact agent name from the Team Members list above. ")
		b.WriteString("Each agent will execute the task and report the result back to you.\n\n")
	}

	return b.String()
}

// SubAgentFileName returns the sanitized filename (without path) for a sub-agent,
// e.g. "my-agent.md". Use this to compute the key for SubAgentFiles in AgentConfig.
func SubAgentFileName(name string) string {
	return sanitizeName(name) + ".md"
}

// SetupSubAgentFile creates a sub-agent definition file at
// {workspacePath}/.claude/agents/{agentName}.md with YAML frontmatter.
// This is used for non-leader agents in the native Claude Code sub-agent architecture.
func SetupSubAgentFile(workspacePath string, agent SubAgentInfo) (string, error) {
	agentsDir := filepath.Join(workspacePath, ".claude", "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		return "", fmt.Errorf("creating agents dir %s: %w", agentsDir, err)
	}

	safeName := sanitizeName(agent.Name)
	filePath := filepath.Join(agentsDir, safeName+".md")
	content := GenerateSubAgentContent(agent)

	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("writing sub-agent file for %s: %w", agent.Name, err)
	}

	return filePath, nil
}

// GenerateSubAgentContent produces the YAML frontmatter + body content for a
// sub-agent file. background, isolation, and permissionMode are always emitted
// with fixed values so sub-agents run isolated and with full permissions.
func GenerateSubAgentContent(agent SubAgentInfo) string {
	var b strings.Builder

	b.WriteString("---\n")
	b.WriteString("name: " + agent.Name + "\n")

	if agent.Description != "" {
		b.WriteString("description: " + yamlQuoteIfNeeded(agent.Description) + "\n")
	}
	if agent.Model != "" && agent.Model != "inherit" {
		b.WriteString("model: " + agent.Model + "\n")
	}

	// Always set these fields for isolated, unrestricted execution.
	b.WriteString("background: true\n")
	b.WriteString("isolation: worktree\n")
	b.WriteString("permissionMode: bypassPermissions\n")

	// Emit skills list if provided, merging the agent's own skills with global
	// leader skills so every worker has access to all shared capabilities.
	mergedSkills := mergeSkillsRaw(agent.Skills, agent.GlobalSkills)
	if skills := formatSkillsYAML(mergedSkills); skills != "" {
		b.WriteString("skills:\n")
		b.WriteString(skills)
	}

	b.WriteString("---\n")

	if agent.ClaudeMD != "" {
		b.WriteString("\n")
		b.WriteString(agent.ClaudeMD)
		// Ensure trailing newline.
		if !strings.HasSuffix(agent.ClaudeMD, "\n") {
			b.WriteString("\n")
		}
	}

	return b.String()
}

// skillConfig mirrors protocol.SkillConfig for JSON unmarshaling in this package.
type skillConfig struct {
	RepoURL   string `json:"repo_url"`
	SkillName string `json:"skill_name"`
}

// formatSkillsYAML converts the JSON skills field into a YAML list of strings
// for inclusion in sub-agent frontmatter (each line: "  - skill-name\n").
func formatSkillsYAML(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}

	// Try as array of SkillConfig objects first.
	var configs []skillConfig
	if err := json.Unmarshal(raw, &configs); err == nil && len(configs) > 0 {
		var b strings.Builder
		for _, cfg := range configs {
			if cfg.RepoURL != "" && cfg.SkillName != "" {
				// Extract owner/repo from URL for YAML display.
				repoPath := cfg.RepoURL
				if strings.HasPrefix(repoPath, "https://github.com/") {
					repoPath = strings.TrimPrefix(repoPath, "https://github.com/")
				}
				b.WriteString("  - " + repoPath + ":" + cfg.SkillName + "\n")
			}
		}
		if b.Len() > 0 {
			return b.String()
		}
	}

	// Fallback: try as array of strings (backward compat).
	var skills []string
	if err := json.Unmarshal(raw, &skills); err == nil && len(skills) > 0 {
		var b strings.Builder
		for _, s := range skills {
			if s != "" {
				b.WriteString("  - " + s + "\n")
			}
		}
		return b.String()
	}

	return ""
}

// mergeSkillsRaw combines two JSON skill arrays into one, deduplicating entries.
// It supports both []skillConfig and []string formats. If both inputs are empty/null,
// it returns nil. The result is always a JSON array of skillConfig objects when at
// least one input contains skillConfig entries, or a JSON array of strings otherwise.
func mergeSkillsRaw(a, b json.RawMessage) json.RawMessage {
	aEmpty := len(a) == 0 || string(a) == "null"
	bEmpty := len(b) == 0 || string(b) == "null"

	if aEmpty && bEmpty {
		return nil
	}
	if aEmpty {
		return b
	}
	if bEmpty {
		return a
	}

	// Parse both as skillConfig arrays first.
	type dedupeKey struct{ RepoURL, SkillName string }
	seen := map[dedupeKey]struct{}{}
	seenStrings := map[string]struct{}{}
	var merged []skillConfig
	var mergedStrings []string

	parseConfigs := func(raw json.RawMessage) {
		var configs []skillConfig
		if err := json.Unmarshal(raw, &configs); err == nil && len(configs) > 0 {
			hasRepo := false
			for _, c := range configs {
				if c.RepoURL != "" {
					hasRepo = true
					break
				}
			}
			if hasRepo {
				for _, c := range configs {
					if c.RepoURL == "" || c.SkillName == "" {
						continue
					}
					key := dedupeKey{c.RepoURL, c.SkillName}
					if _, exists := seen[key]; !exists {
						seen[key] = struct{}{}
						merged = append(merged, c)
					}
				}
				return
			}
		}
		// Fallback: try as string array.
		var strs []string
		if err := json.Unmarshal(raw, &strs); err == nil {
			for _, s := range strs {
				if s == "" {
					continue
				}
				if _, exists := seenStrings[s]; !exists {
					seenStrings[s] = struct{}{}
					mergedStrings = append(mergedStrings, s)
				}
			}
		}
	}

	parseConfigs(a)
	parseConfigs(b)

	// When both formats are present, promote strings to skillConfig format.
	if len(merged) > 0 && len(mergedStrings) > 0 {
		for _, s := range mergedStrings {
			merged = append(merged, skillConfig{SkillName: s})
		}
		result, _ := json.Marshal(merged)
		return result
	}
	if len(merged) > 0 {
		result, _ := json.Marshal(merged)
		return result
	}
	if len(mergedStrings) > 0 {
		result, _ := json.Marshal(mergedStrings)
		return result
	}
	return nil
}

// yamlQuoteIfNeeded wraps a string in double quotes if it contains characters
// that could be problematic in YAML (colons, brackets, newlines, etc.).
func yamlQuoteIfNeeded(s string) string {
	if strings.ContainsAny(s, ":{}[]&*#?|->!%@`,\n\r") {
		escaped := strings.ReplaceAll(s, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, `"`, `\"`)
		escaped = strings.ReplaceAll(escaped, "\n", `\n`)
		escaped = strings.ReplaceAll(escaped, "\r", `\r`)
		return `"` + escaped + `"`
	}
	return s
}

// formatSkills converts the JSON skills field into a readable markdown list.
func formatSkills(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}

	// Try as array of SkillConfig objects first.
	var configs []skillConfig
	if err := json.Unmarshal(raw, &configs); err == nil && len(configs) > 0 {
		var b strings.Builder
		for _, cfg := range configs {
			if cfg.RepoURL != "" && cfg.SkillName != "" {
				repoPath := cfg.RepoURL
				if strings.HasPrefix(repoPath, "https://github.com/") {
					repoPath = strings.TrimPrefix(repoPath, "https://github.com/")
				}
				b.WriteString("- " + repoPath + ":" + cfg.SkillName + "\n")
			} else if cfg.SkillName != "" {
				b.WriteString("- " + cfg.SkillName + "\n")
			}
		}
		if b.Len() > 0 {
			return b.String()
		}
	}

	// Fallback: try as array of strings.
	var strSkills []string
	if err := json.Unmarshal(raw, &strSkills); err == nil && len(strSkills) > 0 {
		var b strings.Builder
		for _, s := range strSkills {
			b.WriteString("- " + s + "\n")
		}
		return b.String()
	}

	// Try as array of objects with a "name" field.
	var objSkills []map[string]interface{}
	if err := json.Unmarshal(raw, &objSkills); err == nil && len(objSkills) > 0 {
		var b strings.Builder
		for _, obj := range objSkills {
			name, _ := obj["name"].(string)
			desc, _ := obj["description"].(string)
			if name != "" {
				b.WriteString("- **" + name + "**")
				if desc != "" {
					b.WriteString(": " + desc)
				}
				b.WriteString("\n")
			}
		}
		return b.String()
	}

	return ""
}

// --- OpenCode workspace generation ---

// GenerateOpenCodeAgentsMD produces the content for .opencode/AGENTS.MD.
// This is the leader's instructions file, analogous to CLAUDE.md for Claude Code.
func GenerateOpenCodeAgentsMD(teamName string, leader SubAgentInfo, workers []SubAgentInfo) string {
	var b strings.Builder

	b.WriteString("# Team: " + teamName + "\n\n")
	b.WriteString("## Agent: " + leader.Name + "\n\n")

	b.WriteString("## Role\nleader\n\n")

	if leader.Description != "" {
		b.WriteString("## Specialty\n")
		b.WriteString(leader.Description + "\n\n")
	}

	// Include InstructionsMD content if provided.
	if leader.ClaudeMD != "" {
		b.WriteString("## Instructions\n")
		b.WriteString(leader.ClaudeMD)
		if !strings.HasSuffix(leader.ClaudeMD, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	skills := formatSkills(leader.Skills)
	if skills != "" {
		b.WriteString("## Skills\n")
		b.WriteString(skills + "\n")
	}

	if len(workers) > 0 {
		b.WriteString("## Team Members\n\n")
		b.WriteString("You are the team leader. The following agents are available for task delegation:\n\n")
		for _, w := range workers {
			b.WriteString("- **" + w.Name + "**")
			if w.Description != "" {
				b.WriteString(" — " + w.Description)
			}
			b.WriteString("\n")
		}

		b.WriteString("\n## Delegation Protocol\n\n")
		b.WriteString("To delegate tasks to team members, use the following format in your response:\n\n")
		b.WriteString("```\n[TASK:agent-name]\nYour instruction for the agent here.\n[/TASK]\n```\n\n")
		b.WriteString("You can delegate to multiple agents in a single response. ")
		b.WriteString("Use the exact agent name from the Team Members list above. ")
		b.WriteString("Each agent will execute the task and report the result back to you.\n\n")
	}

	return b.String()
}

// GenerateOpenCodeSubAgentContent produces the content for an OpenCode sub-agent
// file at .opencode/agents/{name}.md with YAML frontmatter native to OpenCode.
func GenerateOpenCodeSubAgentContent(agent SubAgentInfo, globalSkills []protocol.SkillConfig) string {
	var b strings.Builder

	b.WriteString("---\n")

	if agent.Description != "" {
		b.WriteString("description: " + yamlQuoteIfNeeded(agent.Description) + "\n")
	}
	if agent.Model != "" && agent.Model != "inherit" {
		b.WriteString("model: " + agent.Model + "\n")
	}

	// Standard tool set for OpenCode agents.
	b.WriteString("tools:\n")
	b.WriteString("  - Bash\n")
	b.WriteString("  - Read\n")
	b.WriteString("  - Write\n")
	b.WriteString("  - Glob\n")
	b.WriteString("  - Grep\n")
	b.WriteString("  - Edit\n")

	// Permissions.
	b.WriteString("permission:\n")
	b.WriteString("  edit: allow\n")
	b.WriteString("  bash: allow\n")

	b.WriteString("---\n")

	// Body: instructions + skills section.
	if agent.ClaudeMD != "" {
		b.WriteString("\n")
		b.WriteString(agent.ClaudeMD)
		if !strings.HasSuffix(agent.ClaudeMD, "\n") {
			b.WriteString("\n")
		}
	}

	// Merge agent's own skills with global leader skills.
	globalRaw := skillConfigsToRaw(globalSkills)
	mergedSkills := mergeSkillsRaw(agent.Skills, globalRaw)
	if skills := formatSkills(mergedSkills); skills != "" {
		b.WriteString("\n## Skills\n")
		b.WriteString(skills)
	}

	return b.String()
}

// skillConfigsToRaw converts a []protocol.SkillConfig to json.RawMessage
// for compatibility with the existing mergeSkillsRaw function.
func skillConfigsToRaw(configs []protocol.SkillConfig) json.RawMessage {
	if len(configs) == 0 {
		return nil
	}
	// Convert to the internal skillConfig format.
	internal := make([]skillConfig, len(configs))
	for i, c := range configs {
		internal[i] = skillConfig{RepoURL: c.RepoURL, SkillName: c.SkillName}
	}
	raw, _ := json.Marshal(internal)
	return raw
}

// SetupOpenCodeWorkspace creates the .opencode directory structure under workspacePath
// and writes AGENTS.MD (leader instructions) and agents/{name}.md for each worker.
// Skills are always installed to .claude/skills/ regardless of provider.
func SetupOpenCodeWorkspace(workspacePath, teamName string, leader SubAgentInfo, workers []SubAgentInfo, globalSkills []protocol.SkillConfig) error {
	opencodeDir := filepath.Join(workspacePath, ".opencode")
	agentsDir := filepath.Join(opencodeDir, "agents")

	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		return fmt.Errorf("creating .opencode/agents dir: %w", err)
	}

	// Write AGENTS.MD (leader instructions).
	agentsMD := GenerateOpenCodeAgentsMD(teamName, leader, workers)
	agentsMDPath := filepath.Join(opencodeDir, "AGENTS.MD")
	if err := os.WriteFile(agentsMDPath, []byte(agentsMD), 0644); err != nil {
		return fmt.Errorf("writing AGENTS.MD: %w", err)
	}

	// Write per-worker agent files.
	for _, w := range workers {
		safeName := sanitizeName(w.Name)
		filePath := filepath.Join(agentsDir, safeName+".md")
		content := GenerateOpenCodeSubAgentContent(w, globalSkills)
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			return fmt.Errorf("writing agent file for %s: %w", w.Name, err)
		}
	}

	return nil
}
