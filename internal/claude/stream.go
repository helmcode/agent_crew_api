package claude

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
)

// StreamEvent represents a single event from Claude Code's stream-json output.
type StreamEvent struct {
	Type    string          `json:"type"`              // assistant, tool_use, tool_result, result, error, system
	Message json.RawMessage `json:"message,omitempty"` // The full message content
	Name    string          `json:"name,omitempty"`    // Tool name (for tool_use events)
	Input   json.RawMessage `json:"input,omitempty"`   // Tool input (for tool_use events)
}

// ToolUseInput holds the parsed fields from a tool_use event's input.
type ToolUseInput struct {
	Command  string `json:"command,omitempty"`   // For Bash tool
	FilePath string `json:"file_path,omitempty"` // For Read/Write tools
	Pattern  string `json:"pattern,omitempty"`   // For Glob/Grep tools
}

// ParseStreamEvent parses a single JSON line into a StreamEvent.
func ParseStreamEvent(line []byte) (*StreamEvent, error) {
	var event StreamEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return nil, err
	}
	return &event, nil
}

// ExtractToolCommand extracts the tool name, command, and filesystem paths
// from a tool_use StreamEvent for permission evaluation.
func ExtractToolCommand(event *StreamEvent) (toolName string, command string, paths []string) {
	toolName = event.Name
	if len(event.Input) == 0 {
		return
	}

	var input ToolUseInput
	if err := json.Unmarshal(event.Input, &input); err != nil {
		slog.Debug("failed to parse tool input", "error", err)
		return
	}

	command = input.Command

	if input.FilePath != "" {
		paths = append(paths, input.FilePath)
	}

	return
}

// FormatToolResult produces a JSON string that can be written to Claude's stdin
// to provide a tool result.
func FormatToolResult(output string, isError bool) string {
	result := map[string]interface{}{
		"type":     "tool_result",
		"output":   output,
		"is_error": isError,
	}
	data, _ := json.Marshal(result)
	return string(data)
}

// ParseStreamOutput reads lines from r and sends parsed events to the channel.
// Returns when the reader is exhausted (e.g., process stdout pipe closes).
// Uses non-blocking sends to prevent goroutine leaks if the channel buffer is full.
func ParseStreamOutput(r io.Reader, ch chan<- StreamEvent) {
	scanner := bufio.NewScanner(r)
	// Allow large lines (Claude can produce verbose JSON).
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		event, err := ParseStreamEvent(line)
		if err != nil {
			slog.Debug("skipping unparseable line", "error", err, "line", string(line))
			continue
		}

		select {
		case ch <- *event:
		default:
			slog.Warn("event channel full, dropping event", "type", event.Type)
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Error("error reading stream", "error", err)
	}
}
