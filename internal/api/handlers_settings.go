package api

import (
	"github.com/gofiber/fiber/v2"

	"github.com/helmcode/agent-crew/internal/models"
)

// GetSettings returns all settings.
func (s *Server) GetSettings(c *fiber.Ctx) error {
	var settings []models.Settings
	if err := s.db.Find(&settings).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to list settings")
	}
	return c.JSON(settings)
}

// UpdateSettings creates or updates a setting.
func (s *Server) UpdateSettings(c *fiber.Ctx) error {
	var req UpdateSettingsRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	if req.Key == "" {
		return fiber.NewError(fiber.StatusBadRequest, "key is required")
	}

	var setting models.Settings
	result := s.db.Where("key = ?", req.Key).First(&setting)

	if result.Error != nil {
		// Create new.
		setting = models.Settings{
			Key:   req.Key,
			Value: req.Value,
		}
		if err := s.db.Create(&setting).Error; err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "failed to create setting")
		}
	} else {
		// Update existing.
		if err := s.db.Model(&setting).Update("value", req.Value).Error; err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "failed to update setting")
		}
	}

	return c.JSON(setting)
}
