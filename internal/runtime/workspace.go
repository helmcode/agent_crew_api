package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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
	ClaudeMD     string // Raw CLAUDE.md content; if set, used instead of generateClaudeMD.
	Skills       json.RawMessage
	TeamMembers  []TeamMemberInfo
}

// SetupAgentWorkspace creates the per-agent directory under
// {workspacePath}/.claude/{agentName}/ and writes a CLAUDE.md
// with the agent's configuration. Returns the path to the agent's
// config directory.
func SetupAgentWorkspace(workspacePath string, agent AgentWorkspaceInfo) (string, error) {
	safeName := sanitizeName(agent.Name)
	agentDir := filepath.Join(workspacePath, ".claude", safeName)

	if err := os.MkdirAll(agentDir, 0755); err != nil {
		return "", fmt.Errorf("creating agent workspace dir %s: %w", agentDir, err)
	}

	// Use raw ClaudeMD content if provided; otherwise fall back to generating it.
	claudeMD := agent.ClaudeMD
	if claudeMD == "" {
		claudeMD = generateClaudeMD(agent)
	}
	claudePath := filepath.Join(agentDir, "CLAUDE.md")

	if err := os.WriteFile(claudePath, []byte(claudeMD), 0644); err != nil {
		return "", fmt.Errorf("writing CLAUDE.md for agent %s: %w", agent.Name, err)
	}

	return agentDir, nil
}

// AgentClaudeDir returns the host path for an agent's .claude directory
// without creating it. Used by runtimes to compute mount paths.
func AgentClaudeDir(workspacePath, agentName string) string {
	return filepath.Join(workspacePath, ".claude", sanitizeName(agentName))
}

// generateClaudeMD produces the CLAUDE.md content for an agent.
func generateClaudeMD(agent AgentWorkspaceInfo) string {
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
