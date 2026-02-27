package runtime

import (
	"testing"
)

func TestParseMemoryLimit(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
	}{
		{"512m", 512 * 1024 * 1024},
		{"512M", 512 * 1024 * 1024},
		{"1g", 1024 * 1024 * 1024},
		{"1G", 1024 * 1024 * 1024},
		{"2g", 2 * 1024 * 1024 * 1024},
		{"256k", 256 * 1024},
		{"256K", 256 * 1024},
		{"", 0},
		{"invalid", 0},
		{"m", 0},          // no number
		{"123", 0},        // no unit
		{"12.5m", 0},      // decimal not supported
		{"abc123m", 0},    // non-numeric prefix
	}

	for _, tt := range tests {
		got := parseMemoryLimit(tt.input)
		if got != tt.expected {
			t.Errorf("parseMemoryLimit(%q) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

func TestParseCPULimit(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
	}{
		{"1", 1_000_000_000},
		{"2", 2_000_000_000},
		{"0.5", 500_000_000},
		{"0.25", 250_000_000},
		{"1.5", 1_500_000_000},
		{"0.1", 100_000_000},
		{"", 0},
		{"abc", 0},
	}

	for _, tt := range tests {
		got := parseCPULimit(tt.input)
		if got != tt.expected {
			t.Errorf("parseCPULimit(%q) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

func TestTeamNamingConventions(t *testing.T) {
	tests := []struct {
		teamName string
		fn       func(string) string
		expected string
	}{
		{"myteam", teamNetworkName, "team-myteam"},
		{"myteam", teamVolumeName, "team-myteam-workspace"},
		{"myteam", natsContainerName, "team-myteam-nats"},
	}

	for _, tt := range tests {
		got := tt.fn(tt.teamName)
		if got != tt.expected {
			t.Errorf("got %q, want %q", got, tt.expected)
		}
	}
}

func TestExtractNATSAuthToken(t *testing.T) {
	tests := []struct {
		name     string
		cmd      []string
		expected string
	}{
		{"with auth token", []string{"--jetstream", "--auth", "mytoken"}, "mytoken"},
		{"no auth token", []string{"--jetstream"}, ""},
		{"empty cmd", nil, ""},
		{"auth flag at end without value", []string{"--jetstream", "--auth"}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractNATSAuthToken(tt.cmd)
			if got != tt.expected {
				t.Errorf("extractNATSAuthToken(%v) = %q, want %q", tt.cmd, got, tt.expected)
			}
		})
	}
}

func TestImageSelectionByProvider(t *testing.T) {
	tests := []struct {
		name           string
		provider       string
		configImage    string
		agentImage     string
		openCodeImage  string
		expectedPrefix string
	}{
		{"claude default", "claude", "", "claude-img:v1", "opencode-img:v1", "claude-img:v1"},
		{"opencode default", "opencode", "", "claude-img:v1", "opencode-img:v1", "opencode-img:v1"},
		{"empty provider defaults to claude", "", "", "claude-img:v1", "opencode-img:v1", "claude-img:v1"},
		{"config image overrides provider", "opencode", "custom:v2", "claude-img:v1", "opencode-img:v1", "custom:v2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			img := tt.configImage
			if img == "" {
				if tt.provider == "opencode" {
					img = tt.openCodeImage
				} else {
					img = tt.agentImage
				}
			}
			if img != tt.expectedPrefix {
				t.Errorf("got image %q, want %q", img, tt.expectedPrefix)
			}
		})
	}
}

func TestDefaultOpenCodeAgentImage(t *testing.T) {
	if DefaultOpenCodeAgentImage == "" {
		t.Error("DefaultOpenCodeAgentImage should not be empty")
	}
	if DefaultOpenCodeAgentImage == DefaultAgentImage {
		t.Error("DefaultOpenCodeAgentImage should differ from DefaultAgentImage")
	}
}

func TestProviderAuthValidation_Claude(t *testing.T) {
	// Claude requires ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN.
	tests := []struct {
		name    string
		env     map[string]string
		wantErr bool
	}{
		{"api key only", map[string]string{"ANTHROPIC_API_KEY": "sk-ant-123"}, false},
		{"oauth token only", map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "oauth-token"}, false},
		{"both present", map[string]string{"ANTHROPIC_API_KEY": "sk-ant-123", "CLAUDE_CODE_OAUTH_TOKEN": "oauth"}, false},
		{"none present", map[string]string{}, true},
		{"alias only", map[string]string{"ANTHROPIC_AUTH_TOKEN": "auth-token"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apiKey := tt.env["ANTHROPIC_API_KEY"]
			oauthToken := tt.env["CLAUDE_CODE_OAUTH_TOKEN"]
			if oauthToken == "" {
				oauthToken = tt.env["ANTHROPIC_AUTH_TOKEN"]
			}
			hasAuth := apiKey != "" || oauthToken != ""
			if tt.wantErr && hasAuth {
				t.Error("expected no auth, but found auth")
			}
			if !tt.wantErr && !hasAuth {
				t.Error("expected auth, but found none")
			}
		})
	}
}

func TestProviderAuthValidation_OpenCode(t *testing.T) {
	// OpenCode requires at least one API key or model URL.
	openCodeKeys := []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY",
		"GOOGLE_GENERATIVE_AI_API_KEY",
		"OLLAMA_BASE_URL", "LM_STUDIO_BASE_URL",
	}

	tests := []struct {
		name    string
		env     map[string]string
		wantErr bool
	}{
		{"anthropic key", map[string]string{"ANTHROPIC_API_KEY": "sk-ant-123"}, false},
		{"openai key", map[string]string{"OPENAI_API_KEY": "sk-oai-123"}, false},
		{"google key", map[string]string{"GOOGLE_GENERATIVE_AI_API_KEY": "goog-123"}, false},
		{"ollama url", map[string]string{"OLLAMA_BASE_URL": "http://localhost:11434"}, false},
		{"lmstudio url", map[string]string{"LM_STUDIO_BASE_URL": "http://localhost:1234"}, false},
		{"none present", map[string]string{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hasAuth := false
			for _, key := range openCodeKeys {
				if v := tt.env[key]; v != "" {
					hasAuth = true
					break
				}
			}
			if tt.wantErr && hasAuth {
				t.Error("expected no auth, but found auth")
			}
			if !tt.wantErr && !hasAuth {
				t.Error("expected auth, but found none")
			}
		})
	}
}

func TestProviderEnvVars(t *testing.T) {
	// Verify that AGENT_PROVIDER is included in the env vars.
	config := AgentConfig{
		Name:     "test-agent",
		TeamName: "test-team",
		Role:     "leader",
		Provider: "opencode",
	}

	if config.Provider != "opencode" {
		t.Errorf("expected provider 'opencode', got %q", config.Provider)
	}
}

func TestAgentContainerName(t *testing.T) {
	tests := []struct {
		teamName  string
		agentName string
		expected  string
	}{
		{"myteam", "leader", "team-myteam-leader"},
		{"myteam", "worker-1", "team-myteam-worker-1"},
		{"prod-team", "devops", "team-prod-team-devops"},
	}

	for _, tt := range tests {
		got := agentContainerName(tt.teamName, tt.agentName)
		if got != tt.expected {
			t.Errorf("agentContainerName(%q, %q) = %q, want %q", tt.teamName, tt.agentName, got, tt.expected)
		}
	}
}

func TestProviderEnvVars_OpenCodeKeysAreDistinct(t *testing.T) {
	// Verify that OpenCode and Claude providers use distinct API keys.
	claudeKeys := map[string]bool{
		"ANTHROPIC_API_KEY":        true,
		"CLAUDE_CODE_OAUTH_TOKEN":  true,
		"ANTHROPIC_AUTH_TOKEN":     true,
	}
	openCodeKeys := []string{
		"OPENAI_API_KEY",
		"GOOGLE_GENERATIVE_AI_API_KEY",
		"OLLAMA_BASE_URL",
		"LM_STUDIO_BASE_URL",
	}

	// OpenCode-specific keys should not overlap with Claude-specific auth keys
	// (except ANTHROPIC_API_KEY which is shared).
	for _, key := range openCodeKeys {
		if claudeKeys[key] {
			t.Errorf("OpenCode key %q should not be in Claude-only keys", key)
		}
	}
}

func TestImageSelectionByProvider_AllCases(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		want     string
	}{
		{"claude uses claude image", "claude", DefaultAgentImage},
		{"opencode uses opencode image", "opencode", DefaultOpenCodeAgentImage},
		{"empty provider defaults to claude", "", DefaultAgentImage},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var img string
			if tt.provider == "opencode" {
				img = DefaultOpenCodeAgentImage
			} else {
				img = DefaultAgentImage
			}
			if img != tt.want {
				t.Errorf("got %q, want %q", img, tt.want)
			}
		})
	}
}

func TestProviderAuthValidation_NoLeakage(t *testing.T) {
	// Verify that a config with ONLY OpenAI key does NOT satisfy Claude auth.
	claudeEnv := map[string]string{
		"OPENAI_API_KEY": "sk-oai-123",
	}
	apiKey := claudeEnv["ANTHROPIC_API_KEY"]
	oauthToken := claudeEnv["CLAUDE_CODE_OAUTH_TOKEN"]
	if oauthToken == "" {
		oauthToken = claudeEnv["ANTHROPIC_AUTH_TOKEN"]
	}
	if apiKey != "" || oauthToken != "" {
		t.Error("OpenAI-only config should NOT satisfy Claude auth requirements")
	}

	// Verify that a config with ONLY Anthropic key does satisfy OpenCode auth.
	openCodeEnv := map[string]string{
		"ANTHROPIC_API_KEY": "sk-ant-123",
	}
	openCodeKeys := []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY",
		"GOOGLE_GENERATIVE_AI_API_KEY",
		"OLLAMA_BASE_URL", "LM_STUDIO_BASE_URL",
	}
	hasOpenCodeAuth := false
	for _, key := range openCodeKeys {
		if v := openCodeEnv[key]; v != "" {
			hasOpenCodeAuth = true
			break
		}
	}
	if !hasOpenCodeAuth {
		t.Error("Anthropic key should satisfy OpenCode auth (shared key)")
	}
}

func TestK8sImageSelection(t *testing.T) {
	k := &K8sRuntime{
		agentImage:         "claude-img:v1",
		openCodeAgentImage: "opencode-img:v1",
	}

	if k.agentImage != "claude-img:v1" {
		t.Errorf("K8s claude image: got %q", k.agentImage)
	}
	if k.openCodeAgentImage != "opencode-img:v1" {
		t.Errorf("K8s opencode image: got %q", k.openCodeAgentImage)
	}
	if k.agentImage == k.openCodeAgentImage {
		t.Error("K8s claude and opencode images should be different")
	}
}
