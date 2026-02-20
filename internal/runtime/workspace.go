package runtime

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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
	Skills       json.RawMessage
	TeamMembers  []TeamMemberInfo
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

// SyncUserClaudeConfig copies files from the user's {workspacePath}/.claude/
// directory into the agent's config directory ({workspacePath}/.agentcrew/{agentName}/).
// This allows users to provide settings.json, commands/, and other Claude Code
// config files that will be available to each agent as ~/.claude/ inside the container.
// Files are copied before SetupAgentWorkspace writes the generated CLAUDE.md,
// so the generated CLAUDE.md takes precedence over any user-provided one.
func SyncUserClaudeConfig(workspacePath, agentName string) error {
	userClaudeDir := filepath.Join(workspacePath, ".claude")

	// Check with Lstat (does not follow symlinks) whether .claude is a symlink.
	// If it is, it was created by ExposeAgentConfig from a previous deploy and
	// should not be used as a source for user config files.
	linfo, err := os.Lstat(userClaudeDir)
	if err != nil || linfo.Mode()&os.ModeSymlink != 0 {
		return nil
	}

	info, err := os.Stat(userClaudeDir)
	if err != nil || !info.IsDir() {
		// No .claude directory in workspace — nothing to sync.
		return nil
	}

	agentDir := AgentConfigDir(workspacePath, agentName)
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		return fmt.Errorf("creating agent config dir: %w", err)
	}

	return copyDir(userClaudeDir, agentDir)
}

// copyDir recursively copies the contents of src into dst.
// Existing files in dst are overwritten. The CLAUDE.md file is skipped
// because SetupAgentWorkspace generates an agent-specific one.
// Symlinks are skipped to prevent path-traversal attacks.
func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("reading dir %s: %w", src, err)
	}

	for _, entry := range entries {
		// Skip symlinks — following them could escape the source directory.
		if entry.Type()&os.ModeSymlink != 0 {
			slog.Debug("skipping symlink in .claude dir", "path", filepath.Join(src, entry.Name()))
			continue
		}

		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := os.MkdirAll(dstPath, 0755); err != nil {
				return fmt.Errorf("creating dir %s: %w", dstPath, err)
			}
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}

		// Skip CLAUDE.md — the generated one takes precedence.
		if strings.EqualFold(entry.Name(), "CLAUDE.md") {
			slog.Debug("skipping user CLAUDE.md in favor of generated one", "path", srcPath)
			continue
		}

		if err := copyFile(srcPath, dstPath); err != nil {
			return err
		}
	}

	return nil
}

// copyFile copies a single file from src to dst, preserving permissions.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening %s: %w", src, err)
	}
	defer srcFile.Close()

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", src, err)
	}

	// Strip setuid/setgid bits — these are not needed for config files and
	// would be a security risk if the agent container runs as a different user.
	mode := srcInfo.Mode() &^ (os.ModeSetuid | os.ModeSetgid)
	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("creating %s: %w", dst, err)
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("copying %s to %s: %w", src, dst, err)
	}

	return nil
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

// ExposeAgentConfig creates a convenience symlink at {workspacePath}/.claude
// pointing to the specified agent's config directory (.agentcrew/{agentName}/).
// This allows the user to access the agent's CLAUDE.md, settings.json, and other
// config files directly from the workspace root on the host filesystem.
//
// If {workspacePath}/.claude already exists as a real directory (user-owned config),
// it is left untouched and no symlink is created. Only existing symlinks (from a
// previous deploy) are replaced.
func ExposeAgentConfig(workspacePath, agentName string) error {
	safeName := sanitizeName(agentName)
	linkPath := filepath.Join(workspacePath, ".claude")
	// Relative target so the symlink works even if the workspace is moved.
	target := filepath.Join(".agentcrew", safeName)

	// Check if .claude already exists.
	linfo, err := os.Lstat(linkPath)
	if err == nil {
		if linfo.Mode()&os.ModeSymlink != 0 {
			// Existing symlink from a previous deploy — remove and recreate.
			if err := os.Remove(linkPath); err != nil {
				return fmt.Errorf("removing old .claude symlink: %w", err)
			}
		} else {
			// Real directory or file owned by the user — do not overwrite.
			slog.Info("skipping .claude symlink: real directory exists", "path", linkPath)
			return nil
		}
	}

	if err := os.Symlink(target, linkPath); err != nil {
		return fmt.Errorf("creating .claude symlink: %w", err)
	}

	slog.Info("exposed agent config at workspace root",
		"symlink", linkPath,
		"target", target,
		"agent", agentName,
	)
	return nil
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
