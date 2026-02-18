package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AgentWorkspaceInfo holds the metadata needed to generate an agent's CLAUDE.md.
type AgentWorkspaceInfo struct {
	Name         string
	Role         string
	Specialty    string
	SystemPrompt string
	Skills       json.RawMessage
}

// SetupAgentWorkspace creates the per-agent directory under
// {workspacePath}/.agentcrew/{agentName}/ and writes a CLAUDE.md
// with the agent's configuration. Returns the path to the agent's
// config directory.
func SetupAgentWorkspace(workspacePath string, agent AgentWorkspaceInfo) (string, error) {
	safeName := sanitizeName(agent.Name)
	agentDir := filepath.Join(workspacePath, ".agentcrew", safeName)

	if err := os.MkdirAll(agentDir, 0755); err != nil {
		return "", fmt.Errorf("creating agent workspace dir %s: %w", agentDir, err)
	}

	claudeMD := generateClaudeMD(agent)
	claudePath := filepath.Join(agentDir, "CLAUDE.md")

	if err := os.WriteFile(claudePath, []byte(claudeMD), 0644); err != nil {
		return "", fmt.Errorf("writing CLAUDE.md for agent %s: %w", agent.Name, err)
	}

	return agentDir, nil
}

// AgentConfigDir returns the host path for an agent's config directory
// without creating it. Used by runtimes to compute mount paths.
func AgentConfigDir(workspacePath, agentName string) string {
	return filepath.Join(workspacePath, ".agentcrew", sanitizeName(agentName))
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
