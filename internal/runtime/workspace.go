package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SubAgentInfo holds the metadata needed to generate a sub-agent file
// at .claude/agents/{name}.md with YAML frontmatter.
type SubAgentInfo struct {
	Name        string
	Description string
	Model       string
	Skills      json.RawMessage
	ClaudeMD    string // Body content written after the YAML frontmatter.
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

	// Emit skills list if provided.
	if skills := formatSkillsYAML(agent.Skills); skills != "" {
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

// formatSkillsYAML converts the JSON skills field into a YAML list of strings
// for inclusion in sub-agent frontmatter (each line: "  - skill-name\n").
func formatSkillsYAML(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}

	var skills []string
	if err := json.Unmarshal(raw, &skills); err != nil || len(skills) == 0 {
		return ""
	}

	var b strings.Builder
	for _, s := range skills {
		if s != "" {
			b.WriteString("  - " + s + "\n")
		}
	}
	return b.String()
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

	// Try as array of strings first.
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
