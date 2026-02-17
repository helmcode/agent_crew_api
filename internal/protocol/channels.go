package protocol

import (
	"fmt"
	"strings"
)

// ValidateSubjectToken checks that a name is safe for use in NATS subjects.
// NATS treats '.', '*', and '>' as special characters in subjects.
// Returns an error if the name contains any of these or is empty.
func ValidateSubjectToken(name string) error {
	if name == "" {
		return fmt.Errorf("subject token must not be empty")
	}
	if strings.ContainsAny(name, ".*> \t\n\r") {
		return fmt.Errorf("subject token %q contains invalid NATS characters (.*> or whitespace)", name)
	}
	return nil
}

// TeamLeaderChannel returns the NATS subject for the team leader.
func TeamLeaderChannel(teamName string) (string, error) {
	if err := ValidateSubjectToken(teamName); err != nil {
		return "", fmt.Errorf("invalid team name: %w", err)
	}
	return fmt.Sprintf("team.%s.leader", teamName), nil
}

// AgentChannel returns the NATS subject for a specific agent.
func AgentChannel(teamName, agentName string) (string, error) {
	if err := ValidateSubjectToken(teamName); err != nil {
		return "", fmt.Errorf("invalid team name: %w", err)
	}
	if err := ValidateSubjectToken(agentName); err != nil {
		return "", fmt.Errorf("invalid agent name: %w", err)
	}
	return fmt.Sprintf("team.%s.%s", teamName, agentName), nil
}

// BroadcastChannel returns the NATS subject for team-wide broadcasts.
func BroadcastChannel(teamName string) (string, error) {
	if err := ValidateSubjectToken(teamName); err != nil {
		return "", fmt.Errorf("invalid team name: %w", err)
	}
	return fmt.Sprintf("team.%s.broadcast", teamName), nil
}

// StatusChannel returns the NATS subject for agent status updates.
func StatusChannel(teamName string) (string, error) {
	if err := ValidateSubjectToken(teamName); err != nil {
		return "", fmt.Errorf("invalid team name: %w", err)
	}
	return fmt.Sprintf("team.%s.status", teamName), nil
}
