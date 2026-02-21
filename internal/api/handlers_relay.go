package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/helmcode/agent-crew/internal/models"
	"github.com/helmcode/agent-crew/internal/protocol"
)

// startTeamRelay starts a goroutine that subscribes to the team's NATS and
// saves agent messages as TaskLogs in the DB. The StreamActivity WebSocket
// handler polls the DB, so messages appear in the frontend automatically.
func (s *Server) startTeamRelay(teamID, teamName string) {
	ctx, cancel := context.WithCancel(context.Background())

	s.relaysMu.Lock()
	if existing, ok := s.relays[teamID]; ok {
		existing()
	}
	s.relays[teamID] = cancel
	s.relaysMu.Unlock()

	go func() {
		defer func() {
			s.relaysMu.Lock()
			delete(s.relays, teamID)
			s.relaysMu.Unlock()
		}()
		s.runTeamRelay(ctx, teamID, teamName)
	}()
}

// stopTeamRelay cancels the relay goroutine for a team.
func (s *Server) stopTeamRelay(teamID string) {
	s.relaysMu.Lock()
	defer s.relaysMu.Unlock()
	if cancel, ok := s.relays[teamID]; ok {
		cancel()
		delete(s.relays, teamID)
	}
}

// runTeamRelay connects to the team's NATS, subscribes to all team subjects,
// and saves incoming agent messages as TaskLogs.
func (s *Server) runTeamRelay(ctx context.Context, teamID, teamName string) {
	sanitized := SanitizeName(teamName)

	// Retry getting the NATS URL up to 5 times (team NATS may still be starting).
	var natsURL string
	var err error
	for i := 1; i <= 5; i++ {
		natsURL, err = s.runtime.GetNATSConnectURL(ctx, sanitized)
		if err == nil {
			break
		}
		slog.Warn("relay: waiting for team NATS", "team", teamName, "attempt", i, "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(i) * time.Second):
		}
	}
	if err != nil {
		slog.Error("relay: failed to resolve team NATS URL", "team", teamName, "error", err)
		return
	}

	token := os.Getenv("NATS_AUTH_TOKEN")
	opts := []nats.Option{
		nats.Name("agentcrew-relay-" + sanitized),
		nats.Timeout(5 * time.Second),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2 * time.Second),
	}
	if token != "" {
		opts = append(opts, nats.Token(token))
	}

	nc, err := nats.Connect(natsURL, opts...)
	if err != nil {
		slog.Error("relay: failed to connect to team NATS", "team", teamName, "error", err)
		return
	}
	defer nc.Close()

	subject := "team." + sanitized + ".>"
	sub, err := nc.Subscribe(subject, func(msg *nats.Msg) {
		if err := s.processRelayMessage(teamID, teamName, msg.Data); err != nil {
			slog.Error("relay: failed to process message", "team", teamName, "error", err)
		}
	})
	if err != nil {
		slog.Error("relay: failed to subscribe to team NATS", "team", teamName, "error", err)
		return
	}
	defer sub.Unsubscribe()

	slog.Info("relay: watching team NATS", "team", teamName, "subject", subject)
	<-ctx.Done()
	slog.Info("relay: stopped", "team", teamName)
}

// processRelayMessage parses a raw NATS payload and saves it as a TaskLog.
// It is extracted from the inline callback so it can be unit-tested without
// a real NATS server.
func (s *Server) processRelayMessage(teamID, teamName string, data []byte) error {
	var protoMsg protocol.Message
	if err := json.Unmarshal(data, &protoMsg); err != nil {
		return err
	}
	// Only save leader responses and activity events — user messages are
	// saved by the chat handler and system commands are internal control messages.
	var messageType string
	switch protoMsg.Type {
	case protocol.TypeLeaderResponse:
		messageType = "task_result"
	case protocol.TypeActivityEvent:
		messageType = "activity_event"
	default:
		return nil
	}

	log := models.TaskLog{
		ID:          uuid.New().String(),
		TeamID:      teamID,
		MessageID:   protoMsg.MessageID,
		FromAgent:   protoMsg.From,
		ToAgent:     protoMsg.To,
		MessageType: messageType,
		Payload:     models.JSON(protoMsg.Payload),
	}
	if err := s.db.Create(&log).Error; err != nil {
		slog.Error("relay: failed to save task log", "team", teamName, "error", err)
		return err
	}
	slog.Info("relay: saved agent message", "team", teamName, "type", protoMsg.Type, "from", protoMsg.From)
	return nil
}
