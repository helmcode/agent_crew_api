package nats

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/helmcode/agent-crew/internal/claude"
	"github.com/helmcode/agent-crew/internal/permissions"
	"github.com/helmcode/agent-crew/internal/protocol"
)

// BridgeConfig holds configuration for the NATS-Claude bridge.
type BridgeConfig struct {
	AgentName string
	TeamName  string
	Role      string // "leader"
	Gate      *permissions.Gate
}

// publisher is the interface used by Bridge to publish protocol messages.
// *Client satisfies this interface.
type publisher interface {
	Publish(subject string, msg *protocol.Message) error
	Subscribe(subject string, handler func(*protocol.Message)) error
}

// Bridge connects NATS messaging with the Claude Code CLI process.
// It receives user messages via the team leader NATS channel, forwards them
// to Claude's stdin, reads Claude's stdout events, and publishes leader
// responses back via NATS.
type Bridge struct {
	config  BridgeConfig
	client  publisher
	manager *claude.Manager
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// NewBridge creates a Bridge with the given components.
func NewBridge(config BridgeConfig, client *Client, manager *claude.Manager) *Bridge {
	return &Bridge{
		config:  config,
		client:  client,
		manager: manager,
	}
}

// Start begins listening for NATS messages and forwarding Claude events.
// It subscribes only to the team leader channel for user↔leader communication.
func (b *Bridge) Start(ctx context.Context) error {
	ctx, b.cancel = context.WithCancel(ctx)

	// Subscribe to the team leader channel.
	leaderSubject, err := protocol.TeamLeaderChannel(b.config.TeamName)
	if err != nil {
		return fmt.Errorf("building leader channel: %w", err)
	}
	if err := b.client.Subscribe(leaderSubject, b.handleIncoming); err != nil {
		return err
	}

	// Start goroutine to forward Claude stdout events to NATS.
	b.wg.Add(1)
	go b.forwardEvents(ctx)

	slog.Info("bridge started",
		"agent", b.config.AgentName,
		"team", b.config.TeamName,
		"role", b.config.Role,
	)
	return nil
}

// Stop gracefully shuts down the bridge.
func (b *Bridge) Stop() {
	if b.cancel != nil {
		b.cancel()
	}
	b.wg.Wait()
	slog.Info("bridge stopped", "agent", b.config.AgentName)
}

// handleIncoming processes an incoming NATS protocol message.
func (b *Bridge) handleIncoming(msg *protocol.Message) {
	slog.Info("bridge received message",
		"from", msg.From,
		"type", msg.Type,
		"agent", b.config.AgentName,
	)

	switch msg.Type {
	case protocol.TypeUserMessage:
		b.handleUserMessage(msg)
	case protocol.TypeSystemCommand:
		b.handleSystemCommand(msg)
	default:
		slog.Debug("unhandled message type", "type", msg.Type)
	}
}

// handleUserMessage forwards a free-form message to Claude.
func (b *Bridge) handleUserMessage(msg *protocol.Message) {
	slog.Info("handling user message", "agent", b.config.AgentName, "from", msg.From)

	payload, err := protocol.ParsePayload[protocol.UserMessagePayload](msg)
	if err != nil {
		slog.Error("failed to parse user message", "error", err)
		return
	}

	slog.Info("forwarding user message to claude", "agent", b.config.AgentName, "content_length", len(payload.Content))
	if err := b.manager.SendInput(payload.Content); err != nil {
		slog.Error("failed to send user message to claude", "error", err)
	}
}

// handleSystemCommand processes system-level commands (shutdown, restart, etc.).
func (b *Bridge) handleSystemCommand(msg *protocol.Message) {
	payload, err := protocol.ParsePayload[protocol.SystemCommandPayload](msg)
	if err != nil {
		slog.Error("failed to parse system command", "error", err)
		return
	}

	switch payload.Command {
	case "shutdown":
		slog.Info("received shutdown command", "from", msg.From)
		if err := b.manager.Stop(); err != nil {
			slog.Error("failed to stop claude process", "error", err)
		}
	case "restart":
		prompt := payload.Args["resume_prompt"]
		slog.Info("received restart command", "from", msg.From)
		if err := b.manager.Restart(prompt); err != nil {
			slog.Error("failed to restart claude process", "error", err)
		}
	case "compact_context":
		slog.Info("received compact_context command", "from", msg.From)
		// Context compaction is handled by the manager internally.
	default:
		slog.Warn("unknown system command", "command", payload.Command)
	}
}

// forwardEvents reads Claude stdout events and publishes significant ones to NATS.
func (b *Bridge) forwardEvents(ctx context.Context) {
	defer b.wg.Done()

	events := b.manager.ReadEvents()
	var currentResult string

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				// Channel closed, process exited.
				slog.Info("claude events channel closed", "agent", b.config.AgentName)
				return
			}
			b.processEvent(&event, &currentResult)
		}
	}
}

// processEvent handles a single Claude stream event.
func (b *Bridge) processEvent(event *claude.StreamEvent, currentResult *string) {
	switch event.Type {
	case "tool_use":
		// Check permissions before allowing tool execution.
		if b.config.Gate != nil {
			toolName, command, paths := claude.ExtractToolCommand(event)
			decision := b.config.Gate.Evaluate(toolName, command, paths)
			if !decision.Allowed {
				slog.Warn("tool use denied by permission gate",
					"tool", toolName,
					"command", command,
					"reason", decision.Reason,
				)
				// Send denial result back to Claude.
				denial := claude.FormatToolResult(
					"Permission denied: "+decision.Reason,
					true,
				)
				if err := b.manager.SendInput(denial); err != nil {
					slog.Error("failed to send denial to claude", "error", err)
				}
				return
			}
		}

	case "result":
		// Check if Claude returned an error (billing, auth, etc.).
		if event.IsError {
			friendlyMsg := event.FriendlyError()
			slog.Error("claude result is an error",
				"agent", b.config.AgentName,
				"error_code", event.ErrorCode,
				"result", event.Result,
				"friendly", friendlyMsg,
			)

			b.publishLeaderResponse("", "failed", "", friendlyMsg)
			*currentResult = ""
			return
		}

		// Final result from Claude — extract text and publish.
		var msgContent struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(event.Message, &msgContent); err == nil {
			*currentResult = msgContent.Text
		}
		// Also check Result field (stream-json sometimes uses it directly).
		if *currentResult == "" && event.Result != "" {
			*currentResult = event.Result
		}

		// Publish the result to the leader channel.
		b.publishLeaderResponse("", "completed", *currentResult, "")
		*currentResult = ""

	case "error":
		slog.Error("claude error event", "agent", b.config.AgentName)
	}
}

// publishLeaderResponse sends a leader response to the team leader NATS channel.
func (b *Bridge) publishLeaderResponse(refMsgID, status, result, errMsg string) {
	payload := protocol.LeaderResponsePayload{
		Status: status,
		Result: result,
		Error:  errMsg,
	}

	msg, err := protocol.NewMessage(
		b.config.AgentName,
		"user",
		protocol.TypeLeaderResponse,
		payload,
	)
	if err != nil {
		slog.Error("failed to create leader response message", "error", err)
		return
	}
	msg.RefMessageID = refMsgID

	subject, err := protocol.TeamLeaderChannel(b.config.TeamName)
	if err != nil {
		slog.Error("failed to build leader channel", "error", err)
		return
	}

	if err := b.client.Publish(subject, msg); err != nil {
		slog.Error("failed to publish leader response", "error", err)
	}
}
