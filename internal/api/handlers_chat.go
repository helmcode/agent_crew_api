package api

import (
	"context"
	"encoding/json"
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
func (s *Server) publishToTeamNATS(teamName, message string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	natsURL, err := s.runtime.GetNATSConnectURL(ctx, teamName)
	if err != nil {
		return err
	}

	// Build NATS connection options.
	opts := []nats.Option{nats.Name("agentcrew-api")}
	if token := os.Getenv("NATS_AUTH_TOKEN"); token != "" {
		opts = append(opts, nats.Token(token))
	}

	nc, err := nats.Connect(natsURL, opts...)
	if err != nil {
		return err
	}
	defer nc.Close()

	// Build the protocol message.
	msg, err := protocol.NewMessage("user", "leader", protocol.TypeUserMessage, protocol.UserMessagePayload{
		Content: message,
	})
	if err != nil {
		return err
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	// Publish to the leader channel.
	subject, err := protocol.TeamLeaderChannel(teamName)
	if err != nil {
		return err
	}

	if err := nc.Publish(subject, data); err != nil {
		return err
	}
	return nc.Flush()
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
