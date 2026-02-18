package nats

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/helmcode/agent-crew/internal/claude"
	"github.com/helmcode/agent-crew/internal/permissions"
	"github.com/helmcode/agent-crew/internal/protocol"
)

// BridgeConfig holds configuration for the NATS-Claude bridge.
type BridgeConfig struct {
	AgentName  string
	TeamName   string
	Role       string // "leader" or "worker"
	Gate       *permissions.Gate
}

// Bridge connects NATS messaging with the Claude Code CLI process.
// It receives NATS messages, extracts instructions, writes them to Claude's
// stdin, reads Claude's stdout events, and publishes results back via NATS.
type Bridge struct {
	config  BridgeConfig
	client  *Client
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
// It subscribes to the agent's direct channel and the team broadcast channel.
func (b *Bridge) Start(ctx context.Context) error {
	ctx, b.cancel = context.WithCancel(ctx)

	// Subscribe to direct messages for this agent.
	agentSubject, err := protocol.AgentChannel(b.config.TeamName, b.config.AgentName)
	if err != nil {
		return fmt.Errorf("building agent channel: %w", err)
	}
	if err := b.client.Subscribe(agentSubject, b.handleIncoming); err != nil {
		return err
	}

	// Subscribe to team broadcast messages.
	broadcastSubject, err := protocol.BroadcastChannel(b.config.TeamName)
	if err != nil {
		return fmt.Errorf("building broadcast channel: %w", err)
	}
	if err := b.client.Subscribe(broadcastSubject, b.handleIncoming); err != nil {
		return err
	}

	// If this is the leader, also subscribe to the leader channel.
	if b.config.Role == "leader" {
		leaderSubject, err := protocol.TeamLeaderChannel(b.config.TeamName)
		if err != nil {
			return fmt.Errorf("building leader channel: %w", err)
		}
		if err := b.client.Subscribe(leaderSubject, b.handleIncoming); err != nil {
			return err
		}
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
	case protocol.TypeTaskAssignment:
		b.handleTaskAssignment(msg)
	case protocol.TypeUserMessage:
		b.handleUserMessage(msg)
	case protocol.TypeSystemCommand:
		b.handleSystemCommand(msg)
	case protocol.TypeQuestion:
		b.handleQuestion(msg)
	case protocol.TypeContextShare:
		b.handleContextShare(msg)
	default:
		slog.Debug("unhandled message type", "type", msg.Type)
	}
}

// handleTaskAssignment extracts the instruction and sends it to Claude.
func (b *Bridge) handleTaskAssignment(msg *protocol.Message) {
	payload, err := protocol.ParsePayload[protocol.TaskAssignmentPayload](msg)
	if err != nil {
		slog.Error("failed to parse task assignment", "error", err)
		b.publishError(msg.From, msg.MessageID, "failed to parse task assignment")
		return
	}

	// Send status update: working.
	b.publishStatus("working", payload.Instruction)

	// Write instruction to Claude stdin.
	if err := b.manager.SendInput(payload.Instruction); err != nil {
		slog.Error("failed to send input to claude", "error", err)
		b.publishTaskResult(msg.From, msg.MessageID, "failed", "", err.Error())
		return
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

// handleQuestion forwards questions to Claude for processing.
func (b *Bridge) handleQuestion(msg *protocol.Message) {
	payload, err := protocol.ParsePayload[protocol.QuestionPayload](msg)
	if err != nil {
		slog.Error("failed to parse question", "error", err)
		return
	}

	input := "Question from " + msg.From + ": " + payload.Question
	if len(payload.Options) > 0 {
		input += "\nOptions: "
		for i, opt := range payload.Options {
			if i > 0 {
				input += ", "
			}
			input += opt
		}
	}

	if err := b.manager.SendInput(input); err != nil {
		slog.Error("failed to send question to claude", "error", err)
	}
}

// handleContextShare forwards shared context to Claude.
func (b *Bridge) handleContextShare(msg *protocol.Message) {
	raw, err := json.Marshal(msg.Payload)
	if err != nil {
		slog.Error("failed to marshal context share payload", "error", err)
		return
	}

	input := "Context shared by " + msg.From + ": " + string(raw)
	if err := b.manager.SendInput(input); err != nil {
		slog.Error("failed to send context to claude", "error", err)
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
				b.publishStatus("stopped", "")
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

			// Publish the error as a failed task result so it reaches the
			// Activity panel via TaskLog → WebSocket.
			leaderSubject, err := protocol.TeamLeaderChannel(b.config.TeamName)
			if err != nil {
				slog.Error("failed to build leader channel", "error", err)
				return
			}
			b.publishTaskResult(leaderSubject, "", "failed", "", friendlyMsg)
			b.publishStatus("error", friendlyMsg)
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
		leaderSubject, err := protocol.TeamLeaderChannel(b.config.TeamName)
		if err != nil {
			slog.Error("failed to build leader channel", "error", err)
			return
		}
		b.publishTaskResult(leaderSubject, "", "completed", *currentResult, "")
		b.publishStatus("idle", "")
		*currentResult = ""

	case "error":
		slog.Error("claude error event", "agent", b.config.AgentName)
		b.publishStatus("error", "")
	}
}

// publishStatus sends a status update to the team status channel.
func (b *Bridge) publishStatus(status, currentTask string) {
	statusPayload := protocol.StatusUpdatePayload{
		Agent:       b.config.AgentName,
		Status:      status,
		CurrentTask: currentTask,
		LastActivity: time.Now().UTC(),
	}

	msg, err := protocol.NewMessage(
		b.config.AgentName,
		"",
		protocol.TypeStatusUpdate,
		statusPayload,
	)
	if err != nil {
		slog.Error("failed to create status message", "error", err)
		return
	}

	subject, err := protocol.StatusChannel(b.config.TeamName)
	if err != nil {
		slog.Error("failed to build status channel", "error", err)
		return
	}
	if err := b.client.Publish(subject, msg); err != nil {
		slog.Error("failed to publish status", "error", err)
	}
}

// publishTaskResult sends a task result back to the specified recipient.
func (b *Bridge) publishTaskResult(to, refMsgID, status, result, errMsg string) {
	resultPayload := protocol.TaskResultPayload{
		Status: status,
		Result: result,
		Error:  errMsg,
	}

	msg, err := protocol.NewMessage(
		b.config.AgentName,
		to,
		protocol.TypeTaskResult,
		resultPayload,
	)
	if err != nil {
		slog.Error("failed to create result message", "error", err)
		return
	}
	msg.RefMessageID = refMsgID

	// Determine the NATS subject: if "to" looks like a pre-built subject
	// (e.g., from TeamLeaderChannel), validate it belongs to our team prefix.
	// Otherwise, build it from the agent name.
	var subject string
	teamPrefix := "team." + b.config.TeamName + "."
	if strings.Contains(to, ".") {
		if !strings.HasPrefix(to, teamPrefix) {
			slog.Error("rejected cross-team subject", "to", to, "team", b.config.TeamName)
			return
		}
		subject = to
	} else {
		s, err := protocol.AgentChannel(b.config.TeamName, to)
		if err != nil {
			slog.Error("failed to build agent channel", "to", to, "error", err)
			return
		}
		subject = s
	}

	if err := b.client.Publish(subject, msg); err != nil {
		slog.Error("failed to publish task result", "error", err)
	}
}

// publishError sends an error result back to the sender.
func (b *Bridge) publishError(to, refMsgID, errMsg string) {
	b.publishTaskResult(to, refMsgID, "failed", "", errMsg)
}
