package runtime

import (
	"testing"
)

func TestValidateAgentFilePath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		// Valid paths.
		{
			name:    "leader CLAUDE.md",
			path:    "/workspace/.claude/CLAUDE.md",
			wantErr: false,
		},
		{
			name:    "worker agent file",
			path:    "/workspace/.claude/agents/researcher.md",
			wantErr: false,
		},
		{
			name:    "worker agent file with hyphens",
			path:    "/workspace/.claude/agents/backend-dev.md",
			wantErr: false,
		},
		{
			name:    "worker agent file with underscores",
			path:    "/workspace/.claude/agents/my_agent.md",
			wantErr: false,
		},

		// Path traversal attacks.
		{
			name:    "path traversal with ..",
			path:    "/workspace/.claude/../../../etc/passwd",
			wantErr: true,
		},
		{
			name:    "path traversal in middle",
			path:    "/workspace/.claude/agents/../../secret",
			wantErr: true,
		},
		{
			name:    "double dot in filename",
			path:    "/workspace/.claude/agents/..hidden.md",
			wantErr: true,
		},

		// Outside allowed prefix.
		{
			name:    "root path",
			path:    "/etc/passwd",
			wantErr: true,
		},
		{
			name:    "workspace but not .claude",
			path:    "/workspace/secret.txt",
			wantErr: true,
		},
		{
			name:    "workspace .claude but not subpath",
			path:    "/workspace/.claude-other/file.md",
			wantErr: true,
		},
		{
			name:    "empty path",
			path:    "",
			wantErr: true,
		},

		// Wrong file types / locations.
		{
			name:    "non-md file in agents dir",
			path:    "/workspace/.claude/agents/script.sh",
			wantErr: true,
		},
		{
			name:    "file directly in .claude (not CLAUDE.md)",
			path:    "/workspace/.claude/settings.json",
			wantErr: true,
		},
		{
			name:    "nested subdir under agents",
			path:    "/workspace/.claude/agents/sub/nested.md",
			wantErr: true,
		},
		{
			name:    "CLAUDE.md in agents dir",
			path:    "/workspace/.claude/agents/CLAUDE.md",
			wantErr: false, // This is a valid .md file in agents/
		},
		{
			name:    "skills directory",
			path:    "/workspace/.claude/skills/my-skill.md",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAgentFilePath(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateAgentFilePath(%q) error = %v, wantErr %v", tt.path, err, tt.wantErr)
			}
		})
	}
}
