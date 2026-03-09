package api

import (
	"strings"
	"testing"
)

func TestValidateAgentImage(t *testing.T) {
	tests := []struct {
		name    string
		image   string
		wantErr bool
		errMsg  string
	}{
		// Valid cases.
		{name: "empty string uses default", image: "", wantErr: false},
		{name: "ghcr image with tag", image: "ghcr.io/helmcode/agent_crew_agent:latest", wantErr: false},
		{name: "docker hub with namespace", image: "myorg/myimage:v1.0", wantErr: false},
		{name: "ecr image", image: "123456789.dkr.ecr.us-east-1.amazonaws.com/myapp:latest", wantErr: false},
		{name: "image with sha256 digest", image: "ghcr.io/org/image@sha256:abc123", wantErr: false},
		{name: "nested registry path", image: "registry.example.com/org/team/image:v2", wantErr: false},

		// Invalid cases.
		{name: "bare image name", image: "nginx", wantErr: true, errMsg: "must include a registry"},
		{name: "bare image with tag", image: "alpine:3.18", wantErr: true, errMsg: "must include a registry"},
		{name: "bare ubuntu", image: "ubuntu", wantErr: true, errMsg: "must include a registry"},
		{name: "contains semicolon", image: "ghcr.io/org/img;cmd", wantErr: true, errMsg: "invalid characters"},
		{name: "contains pipe", image: "ghcr.io/org/img|cat", wantErr: true, errMsg: "invalid characters"},
		{name: "contains ampersand", image: "ghcr.io/org/img&echo", wantErr: true, errMsg: "invalid characters"},
		{name: "contains dollar", image: "ghcr.io/org/$img", wantErr: true, errMsg: "invalid characters"},
		{name: "contains backtick", image: "ghcr.io/org/`img`", wantErr: true, errMsg: "invalid characters"},
		{name: "contains double quote", image: `ghcr.io/org/"img"`, wantErr: true, errMsg: "invalid characters"},
		{name: "contains single quote", image: "ghcr.io/org/'img'", wantErr: true, errMsg: "invalid characters"},
		{name: "contains angle brackets", image: "ghcr.io/org/<img>", wantErr: true, errMsg: "invalid characters"},
		{name: "contains parens", image: "ghcr.io/org/(img)", wantErr: true, errMsg: "invalid characters"},
		{name: "contains braces", image: "ghcr.io/org/{img}", wantErr: true, errMsg: "invalid characters"},
		{name: "contains exclamation", image: "ghcr.io/org/img!", wantErr: true, errMsg: "invalid characters"},
		{name: "contains backslash", image: "ghcr.io/org/img\\cmd", wantErr: true, errMsg: "invalid characters"},
		{name: "contains newline", image: "ghcr.io/org/img\ncmd", wantErr: true, errMsg: "invalid characters"},
		{name: "contains tab", image: "ghcr.io/org/img\tcmd", wantErr: true, errMsg: "invalid characters"},
		{name: "contains null byte", image: "ghcr.io/org/img\x00cmd", wantErr: true, errMsg: "invalid characters"},
		{name: "contains space", image: "ghcr.io/org/my image", wantErr: true, errMsg: "must not contain spaces"},
		{name: "exceeds 512 chars", image: "ghcr.io/org/" + strings.Repeat("a", 501), wantErr: true, errMsg: "at most 512"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAgentImage(tt.image)
			if tt.wantErr {
				if err == nil {
					t.Errorf("validateAgentImage(%q) = nil, want error containing %q", tt.image, tt.errMsg)
				} else if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("validateAgentImage(%q) error = %q, want error containing %q", tt.image, err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("validateAgentImage(%q) = %v, want nil", tt.image, err)
				}
			}
		})
	}
}
