package api

import (
	"encoding/json"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/helmcode/agent-crew/internal/models"
)

// SendChat sends a user message to the team leader.
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

	// TODO: Publish message to NATS leader channel when NATS client is available.
	// For now, log it to the task log.
	content, _ := json.Marshal(map[string]string{"content": req.Message})
	log := models.TaskLog{
		ID:          uuid.New().String(),
		TeamID:      teamID,
		FromAgent:   "user",
		ToAgent:     "leader",
		MessageType: "user_message",
		Payload:     models.JSON(content),
	}
	s.db.Create(&log)

	return c.JSON(fiber.Map{
		"status":  "queued",
		"message": "Message sent to team leader",
	})
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
