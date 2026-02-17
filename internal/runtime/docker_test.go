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
