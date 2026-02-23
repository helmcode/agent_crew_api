package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
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
		messageType = string(protocol.TypeLeaderResponse)
	case protocol.TypeActivityEvent:
		messageType = "activity_event"
	case protocol.TypeContainerValidation:
		messageType = "container_validation"
	case protocol.TypeSkillStatus:
		messageType = string(protocol.TypeSkillStatus)
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

	// Persist skill installation results on the agent record so that
	// GET /api/teams/:id returns skill_statuses for each agent.
	if protoMsg.Type == protocol.TypeSkillStatus {
		s.persistSkillStatuses(teamID, protoMsg)
	}

	return nil
}

// persistSkillStatuses extracts skill installation results from a skill_status
// NATS message and distributes them to the correct worker agents based on each
// worker's SubAgentSkills configuration. The sidecar runs inside the leader
// container and reports ALL skills in a flat list, so we match each result's
// Package (format "repo_url:skill_name") against each worker's configured skills.
func (s *Server) persistSkillStatuses(teamID string, msg protocol.Message) {
	var payload protocol.SkillStatusPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		slog.Error("relay: failed to parse skill_status payload", "error", err)
		return
	}

	// Load all worker agents for this team.
	var agents []models.Agent
	if err := s.db.Where("team_id = ? AND role = ?", teamID, models.AgentRoleWorker).Find(&agents).Error; err != nil {
		slog.Error("relay: failed to load team agents for skill distribution", "error", err)
		return
	}

	// Build a lookup: Package string → SkillInstallResult.
	type skillStatus struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}
	resultMap := make(map[string]skillStatus, len(payload.Skills))
	for _, sk := range payload.Skills {
		resultMap[sk.Package] = skillStatus{
			Name:   sk.Package,
			Status: sk.Status,
			Error:  sk.Error,
		}
	}

	// For each worker, find which skills belong to it and update its SkillStatuses.
	for _, agent := range agents {
		// Parse the worker's configured skills (try SkillConfig objects first, then legacy strings).
		var agentSkillKeys []string
		var cfgs []protocol.SkillConfig
		if err := json.Unmarshal(agent.SubAgentSkills, &cfgs); err == nil {
			for _, cfg := range cfgs {
				if cfg.RepoURL != "" && cfg.SkillName != "" {
					agentSkillKeys = append(agentSkillKeys, cfg.RepoURL+":"+cfg.SkillName)
				}
			}
		} else {
			// Fallback: legacy string format "owner/repo:skill-name".
			var strSkills []string
			if err := json.Unmarshal(agent.SubAgentSkills, &strSkills); err == nil {
				for _, sk := range strSkills {
					parts := strings.SplitN(sk, ":", 2)
					if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
						repoURL := parts[0]
						if !strings.HasPrefix(repoURL, "https://") {
							repoURL = "https://github.com/" + repoURL
						}
						agentSkillKeys = append(agentSkillKeys, repoURL+":"+parts[1])
					}
				}
			}
		}

		if len(agentSkillKeys) == 0 {
			continue
		}

		// Collect matching skill results for this worker.
		statuses := make([]skillStatus, 0, len(agentSkillKeys))
		for _, key := range agentSkillKeys {
			if st, ok := resultMap[key]; ok {
				statuses = append(statuses, st)
			}
		}

		if len(statuses) == 0 {
			continue
		}

		data, err := json.Marshal(statuses)
		if err != nil {
			slog.Error("relay: failed to marshal skill statuses", "agent", agent.Name, "error", err)
			continue
		}

		result := s.db.Model(&models.Agent{}).
			Where("id = ?", agent.ID).
			Update("skill_statuses", models.JSON(data))
		if result.Error != nil {
			slog.Error("relay: failed to persist skill_statuses", "agent", agent.Name, "error", result.Error)
		} else if result.RowsAffected > 0 {
			slog.Info("relay: updated agent skill_statuses", "agent", agent.Name, "skills", len(statuses))
		}
	}
}
