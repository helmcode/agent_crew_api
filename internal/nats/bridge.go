package nats

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/helmcode/agent-crew/internal/claude"
	"github.com/helmcode/agent-crew/internal/permissions"
	"github.com/helmcode/agent-crew/internal/protocol"
	"github.com/helmcode/agent-crew/internal/provider"
)

// BridgeConfig holds configuration for the NATS-agent bridge.
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

// pendingMessage holds a queued user message with its correlation metadata.
type pendingMessage struct {
	content        string
	scheduledRunID string
}

// Bridge connects NATS messaging with an AI agent process.
// It receives user messages via the team leader NATS channel, forwards them
// to the agent's input, reads the agent's output events, and publishes leader
// responses back via NATS.
type Bridge struct {
	config  BridgeConfig
	client  publisher
	manager provider.AgentManager
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	userMsgs chan pendingMessage // Queued user messages for serial processing.

	mu              sync.Mutex
	scheduledRunIDs []string // FIFO queue of correlation IDs from scheduled run requests
	errorPublished  bool     // Guards against duplicate error leader_responses within one interaction.
}

// NewBridge creates a Bridge with the given components.
func NewBridge(config BridgeConfig, client *Client, manager provider.AgentManager) *Bridge {
	return &Bridge{
		config:   config,
		client:   client,
		manager:  manager,
		userMsgs: make(chan pendingMessage, 16),
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

	// Start goroutine to process queued user messages serially.
	// This unblocks the NATS subscription callback (handleIncoming) so it
	// can keep receiving messages while SendInput blocks.
	b.wg.Add(1)
	go b.processUserMessages(ctx)

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

// handleUserMessage queues a user message for serial processing.
// This returns immediately so the NATS subscription callback is not blocked
// while SendInput waits for the Claude process to finish.
func (b *Bridge) handleUserMessage(msg *protocol.Message) {
	slog.Info("handling user message", "agent", b.config.AgentName, "from", msg.From)

	payload, err := protocol.ParsePayload[protocol.UserMessagePayload](msg)
	if err != nil {
		slog.Error("failed to parse user message", "error", err)
		return
	}

	pm := pendingMessage{
		content:        payload.Content,
		scheduledRunID: payload.ScheduledRunID,
	}

	select {
	case b.userMsgs <- pm:
		slog.Info("user message queued", "agent", b.config.AgentName, "content_length", len(payload.Content))
	default:
		slog.Warn("user message queue full, dropping message", "agent", b.config.AgentName)
	}
}

// processUserMessages reads queued user messages and forwards them to the
// agent serially. Each SendInput call blocks until the Claude process finishes,
// ensuring conversation turns do not interleave.
func (b *Bridge) processUserMessages(ctx context.Context) {
	defer b.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case pm := <-b.userMsgs:
			// Reset error dedup flag for new interaction.
			b.mu.Lock()
			b.errorPublished = false
			b.scheduledRunIDs = append(b.scheduledRunIDs, pm.scheduledRunID)
			b.mu.Unlock()

			slog.Info("forwarding user message to claude", "agent", b.config.AgentName, "content_length", len(pm.content))
			if err := b.manager.SendInput(pm.content); err != nil {
				slog.Error("failed to send user message to claude", "error", err)
			}
		}
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

// forwardEvents reads agent stdout events and publishes significant ones to NATS.
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
				slog.Info("agent events channel closed", "agent", b.config.AgentName)
				return
			}
			b.processEvent(&event, &currentResult)
		}
	}
}

// processEvent handles a single agent stream event.
func (b *Bridge) processEvent(event *provider.StreamEvent, currentResult *string) {
	// Convert to claude.StreamEvent for operations that need the claude-specific type.
	claudeEvent := provider.ToClaudeStreamEvent(event)

	switch event.Type {
	case "tool_use":
		// Publish activity event for the tool call so the UI can show progress.
		toolName, command, paths := claude.ExtractToolCommand(claudeEvent)
		action := toolName
		if command != "" {
			action = toolName + ": " + command
		}
		b.publishActivityEvent(claudeEvent, action)

		// Check permissions before allowing tool execution.
		if b.config.Gate != nil {
			decision := b.config.Gate.Evaluate(toolName, command, paths)
			if !decision.Allowed {
				slog.Warn("tool use denied by permission gate",
					"tool", toolName,
					"command", command,
					"reason", decision.Reason,
				)
				// Send denial result back to the agent.
				denial := claude.FormatToolResult(
					"Permission denied: "+decision.Reason,
					true,
				)
				if err := b.manager.SendInput(denial); err != nil {
					slog.Error("failed to send denial to agent", "error", err)
				}
				return
			}
		}

	case "reasoning":
		// Publish reasoning (chain-of-thought) as activity events for visibility
		// but do NOT accumulate into currentResult to prevent leaking into chat.
		b.publishActivityEvent(claudeEvent, "reasoning")

	case "assistant":
		// Publish assistant messages as activity events so the UI shows
		// intermediate thinking/responses in real time.
		b.publishActivityEvent(claudeEvent, "assistant message")

		// Accumulate assistant text for providers (like OpenCode) that deliver
		// the response in streaming "assistant" parts rather than a single "result".
		if event.Message != "" {
			var msgContent struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal([]byte(event.Message), &msgContent); err == nil && msgContent.Text != "" {
				*currentResult += msgContent.Text
			}
		}

	case "result":
		// Check if the agent returned an error (billing, auth, etc.).
		if event.IsError {
			// Skip if an error was already published for this interaction
			// (e.g. session.error followed by message.updated with error).
			if b.errorPublished {
				*currentResult = ""
				return
			}
			friendlyMsg := claudeEvent.FriendlyError()
			slog.Error("agent result is an error",
				"agent", b.config.AgentName,
				"result", event.Result,
				"friendly", friendlyMsg,
			)

			b.publishLeaderResponse("", "failed", "", friendlyMsg)
			b.errorPublished = true
			*currentResult = ""
			return
		}

		// Final result from agent — extract text and publish.
		// Only overwrite the accumulated currentResult if the result event
		// carries actual text. Claude Code with --resume often delivers the
		// full response via streaming "assistant" events and sends the final
		// "result" event with message: null (JSON null). The provider adapter
		// converts null to the string "null", which json.Unmarshal accepts
		// (producing zero-value fields), silently wiping the accumulated text.
		var msgContent struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if event.Message != "" && event.Message != "null" {
			if err := json.Unmarshal([]byte(event.Message), &msgContent); err == nil && msgContent.Text != "" {
				*currentResult = msgContent.Text
			}
		}
		// Also check Result field (stream-json sometimes uses it directly).
		if *currentResult == "" && event.Result != "" {
			*currentResult = event.Result
		}

		// Skip empty results (e.g. session.idle after an error was already reported).
		if *currentResult == "" {
			return
		}

		// Decode any literal \uXXXX escape sequences that Claude Code's
		// stream-json may produce when its JSON encoder double-encodes
		// non-ASCII characters (e.g. "Descripci\u00f3n" instead of "Descripción").
		*currentResult = decodeUnicodeEscapes(*currentResult)

		// Publish the result to the leader channel.
		b.publishLeaderResponse("", "completed", *currentResult, "")
		*currentResult = ""

	case "tool_result":
		// Publish tool results as activity events for visibility.
		b.publishActivityEvent(claudeEvent, "tool result")

	case "system":
		// Handle system events (e.g. init with MCP server statuses).
		if event.Subtype == "init" && event.MCPServers != "" {
			b.publishMcpRuntimeStatus(event.MCPServers)
		}
		b.publishActivityEvent(claudeEvent, "system: "+event.Subtype)

	case "error":
		slog.Error("agent error event", "agent", b.config.AgentName, "result", event.Result)
		b.publishActivityEvent(claudeEvent, "error")

		// Publish as leader_response so the error appears in the chat UI
		// with the Settings + Redeploy buttons (same as deploy errors).
		if event.IsError && !b.errorPublished {
			friendlyMsg := claudeEvent.FriendlyError()
			b.publishLeaderResponse("", "failed", "", friendlyMsg)
			b.errorPublished = true
			*currentResult = ""
		}
	}
}

// publishActivityEvent sends an intermediate activity event to the team activity NATS channel.
func (b *Bridge) publishActivityEvent(event *claude.StreamEvent, action string) {
	rawEvent, err := json.Marshal(event)
	if err != nil {
		slog.Error("failed to marshal activity event", "error", err)
		return
	}

	payload := protocol.ActivityEventPayload{
		EventType: event.Type,
		AgentName: b.config.AgentName,
		ToolName:  event.Name,
		Action:    action,
		Payload:   rawEvent,
	}

	msg, err := protocol.NewMessage(
		b.config.AgentName,
		"system",
		protocol.TypeActivityEvent,
		payload,
	)
	if err != nil {
		slog.Error("failed to create activity event message", "error", err)
		return
	}

	subject, err := protocol.TeamActivityChannel(b.config.TeamName)
	if err != nil {
		slog.Error("failed to build activity channel", "error", err)
		return
	}

	if err := b.client.Publish(subject, msg); err != nil {
		slog.Debug("failed to publish activity event", "error", err)
	}
}

// publishLeaderResponse sends a leader response to the team leader NATS channel.
func (b *Bridge) publishLeaderResponse(refMsgID, status, result, errMsg string) {
	// Pop the next scheduled run ID from the FIFO queue.
	// Order is preserved because Claude processes messages sequentially.
	b.mu.Lock()
	var runID string
	if len(b.scheduledRunIDs) > 0 {
		runID = b.scheduledRunIDs[0]
		b.scheduledRunIDs = b.scheduledRunIDs[1:]
	}
	b.mu.Unlock()

	payload := protocol.LeaderResponsePayload{
		Status:         status,
		Result:         result,
		Error:          errMsg,
		ScheduledRunID: runID,
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

// mcpInitServer represents a single MCP server entry from Claude Code's init event.
type mcpInitServer struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "connected", "failed", etc.
	Error  string `json:"error,omitempty"`
}

// mapMcpRuntimeStatus maps Claude Code's MCP server status to the protocol status.
// "connected" → "running", "failed" → "failed", anything else passes through.
func mapMcpRuntimeStatus(claudeStatus string) string {
	switch claudeStatus {
	case "connected":
		return "running"
	case "failed":
		return "failed"
	default:
		return claudeStatus
	}
}

// publishMcpRuntimeStatus parses MCP server statuses from a system/init event
// and publishes them as a TypeMcpStatus message via NATS.
func (b *Bridge) publishMcpRuntimeStatus(rawServers string) {
	var servers []mcpInitServer
	if err := json.Unmarshal([]byte(rawServers), &servers); err != nil {
		slog.Error("failed to parse MCP servers from init event", "error", err)
		return
	}

	statuses := make([]protocol.McpServerStatus, 0, len(servers))
	var running, failed int
	for _, s := range servers {
		mapped := mapMcpRuntimeStatus(s.Status)
		statuses = append(statuses, protocol.McpServerStatus{
			Name:   s.Name,
			Status: mapped,
			Error:  s.Error,
		})
		switch mapped {
		case "running":
			running++
		case "failed":
			failed++
		}
	}

	summary := fmt.Sprintf("%d running, %d failed", running, failed)
	slog.Info("MCP runtime status from init event", "agent", b.config.AgentName, "summary", summary)

	payload := protocol.McpStatusPayload{
		AgentName: b.config.AgentName,
		Servers:   statuses,
		Summary:   summary,
	}

	msg, err := protocol.NewMessage(b.config.AgentName, "system", protocol.TypeMcpStatus, payload)
	if err != nil {
		slog.Error("failed to create MCP runtime status message", "error", err)
		return
	}

	subject, err := protocol.TeamActivityChannel(b.config.TeamName)
	if err != nil {
		slog.Error("failed to build activity channel for MCP runtime status", "error", err)
		return
	}

	if err := b.client.Publish(subject, msg); err != nil {
		slog.Error("failed to publish MCP runtime status", "error", err)
	}
}

// decodeUnicodeEscapes replaces literal \uXXXX escape sequences in a string
// with their actual UTF-8 characters. This handles cases where an upstream
// JSON encoder (e.g. Claude Code CLI) double-encodes non-ASCII characters,
// producing text like "Descripci\u00f3n" instead of "Descripción".
//
// Surrogate pairs (\uD800-\uDFFF) are handled: a high surrogate followed
// by a low surrogate is decoded into the correct supplementary code point.
func decodeUnicodeEscapes(s string) string {
	if !strings.Contains(s, `\u`) {
		return s
	}
	var result strings.Builder
	result.Grow(len(s))
	i := 0
	for i < len(s) {
		if i+5 < len(s) && s[i] == '\\' && s[i+1] == 'u' {
			hex := s[i+2 : i+6]
			cp, err := strconv.ParseUint(hex, 16, 32)
			if err == nil {
				r := rune(cp)
				// Handle surrogate pairs for characters outside BMP.
				if r >= 0xD800 && r <= 0xDBFF && i+11 < len(s) && s[i+6] == '\\' && s[i+7] == 'u' {
					hex2 := s[i+8 : i+12]
					cp2, err2 := strconv.ParseUint(hex2, 16, 32)
					if err2 == nil && cp2 >= 0xDC00 && cp2 <= 0xDFFF {
						r = 0x10000 + (r-0xD800)*0x400 + (rune(cp2) - 0xDC00)
						result.WriteRune(r)
						i += 12
						continue
					}
				}
				result.WriteRune(r)
				i += 6
				continue
			}
		}
		result.WriteByte(s[i])
		i++
	}
	return result.String()
}
