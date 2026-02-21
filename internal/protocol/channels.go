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

// TeamLeaderChannel returns the NATS subject for user↔leader communication.
func TeamLeaderChannel(teamName string) (string, error) {
	if err := ValidateSubjectToken(teamName); err != nil {
		return "", fmt.Errorf("invalid team name: %w", err)
	}
	return fmt.Sprintf("team.%s.leader", teamName), nil
}

// TeamActivityChannel returns the NATS subject for streaming intermediate
// activity events (tool calls, assistant messages, etc.) from agents.
func TeamActivityChannel(teamName string) (string, error) {
	if err := ValidateSubjectToken(teamName); err != nil {
		return "", fmt.Errorf("invalid team name: %w", err)
	}
	return fmt.Sprintf("team.%s.activity", teamName), nil
}
