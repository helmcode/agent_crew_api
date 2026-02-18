package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/helmcode/agent-crew/internal/models"
	"github.com/helmcode/agent-crew/internal/protocol"
)

// SendChat sends a user message to the team leader via NATS.
func (s *Server) SendChat(c *fiber.Ctx) error {
	teamID := c.Params("id")

	var team models.Team
	if err := s.db.First(&team, "id = ?", teamID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "team not found")
	}

	if team.Status != models.TeamStatusRunning {
		return fiber.NewError(fiber.StatusConflict, "team is not running")
	}

	var req ChatRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	if req.Message == "" {
		return fiber.NewError(fiber.StatusBadRequest, "message is required")
	}

	// Log to task log for persistence and Activity panel.
	content, _ := json.Marshal(map[string]string{"content": req.Message})
	taskLog := models.TaskLog{
		ID:          uuid.New().String(),
		TeamID:      teamID,
		FromAgent:   "user",
		ToAgent:     "leader",
		MessageType: "user_message",
		Payload:     models.JSON(content),
	}
	s.db.Create(&taskLog)

	// Publish to NATS leader channel so the agent actually receives the message.
	sanitizedName := SanitizeName(team.Name)
	if err := s.publishToTeamNATS(sanitizedName, req.Message); err != nil {
		slog.Error("failed to publish chat to NATS", "team", team.Name, "error", err)
		return c.JSON(fiber.Map{
			"status":  "queued",
			"message": "Message logged but NATS delivery failed: " + err.Error(),
		})
	}

	return c.JSON(fiber.Map{
		"status":  "sent",
		"message": "Message sent to team leader",
	})
}

// publishToTeamNATS connects to the team's NATS, publishes a user_message to
// the leader channel, and disconnects. The connection is short-lived on purpose
// to avoid managing per-team NATS connections in the API server.
// It retries up to 3 times to handle cases where the NATS container was just
// recreated (e.g. after port binding fix).
func (s *Server) publishToTeamNATS(teamName, message string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	natsURL, err := s.runtime.GetNATSConnectURL(ctx, teamName)
	if err != nil {
		return fmt.Errorf("resolving NATS URL: %w", err)
	}

	// Build NATS connection options.
	token := os.Getenv("NATS_AUTH_TOKEN")
	opts := []nats.Option{
		nats.Name("agentcrew-api"),
		nats.Timeout(5 * time.Second),
	}
	if token != "" {
		opts = append(opts, nats.Token(token))
	}

	slog.Info("connecting to team NATS",
		"team", teamName,
		"url", natsURL,
		"auth", token != "",
	)

	// Retry connection up to 3 times (NATS may have just been recreated).
	var nc *nats.Conn
	for attempt := 1; attempt <= 3; attempt++ {
		nc, err = nats.Connect(natsURL, opts...)
		if err == nil {
			break
		}
		slog.Warn("NATS connect attempt failed",
			"team", teamName,
			"url", natsURL,
			"attempt", attempt,
			"error", err,
		)
		if attempt < 3 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("context cancelled waiting for NATS: %w", ctx.Err())
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
	}
	if err != nil {
		return fmt.Errorf("connecting to NATS at %s (auth=%t): %w", natsURL, token != "", err)
	}
	defer nc.Close()

	// Build the protocol message.
	msg, err := protocol.NewMessage("user", "leader", protocol.TypeUserMessage, protocol.UserMessagePayload{
		Content: message,
	})
	if err != nil {
		return fmt.Errorf("building protocol message: %w", err)
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshaling message: %w", err)
	}

	// Publish to the leader channel.
	subject, err := protocol.TeamLeaderChannel(teamName)
	if err != nil {
		return fmt.Errorf("building leader channel: %w", err)
	}

	if err := nc.Publish(subject, data); err != nil {
		return fmt.Errorf("publishing to %s: %w", subject, err)
	}

	if err := nc.Flush(); err != nil {
		return fmt.Errorf("flushing NATS: %w", err)
	}

	slog.Info("chat message published to NATS", "team", teamName, "subject", subject)
	return nil
}

// GetMessages returns task logs for a team.
func (s *Server) GetMessages(c *fiber.Ctx) error {
	teamID := c.Params("id")

	var team models.Team
	if err := s.db.First(&team, "id = ?", teamID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "team not found")
	}

	limit := c.QueryInt("limit", 50)
	if limit > 200 {
		limit = 200
	}

	var logs []models.TaskLog
	if err := s.db.Where("team_id = ?", teamID).
		Order("created_at DESC").
		Limit(limit).
		Find(&logs).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to list messages")
	}

	return c.JSON(logs)
}
