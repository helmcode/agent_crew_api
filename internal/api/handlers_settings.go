package api

import (
	"log/slog"

	"github.com/gofiber/fiber/v2"

	"github.com/helmcode/agent-crew/internal/crypto"
	"github.com/helmcode/agent-crew/internal/models"
)

const maskedValue = "********"

// settingsResponse is the API representation of a setting.
// Secret values are masked before being sent to the client.
type settingsResponse struct {
	ID        uint   `json:"id"`
	Key       string `json:"key"`
	Value     string `json:"value"`
	IsSecret  bool   `json:"is_secret"`
	UpdatedAt string `json:"updated_at"`
}

// maskSetting converts a model setting into a response, masking secret values.
func maskSetting(s models.Settings) settingsResponse {
	value := s.Value
	if s.IsSecret {
		value = maskedValue
	}
	return settingsResponse{
		ID:        s.ID,
		Key:       s.Key,
		Value:     value,
		IsSecret:  s.IsSecret,
		UpdatedAt: s.UpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"),
	}
}

// GetSettings returns all settings with secret values masked.
func (s *Server) GetSettings(c *fiber.Ctx) error {
	var settings []models.Settings
	if err := s.db.Find(&settings).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to list settings")
	}

	resp := make([]settingsResponse, len(settings))
	for i, setting := range settings {
		resp[i] = maskSetting(setting)
	}
	return c.JSON(resp)
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

	isSecret := false
	if req.IsSecret != nil {
		isSecret = *req.IsSecret
	}

	// Encrypt the value if marked as secret.
	storedValue := req.Value
	if isSecret {
		encrypted, err := crypto.Encrypt(req.Value)
		if err != nil {
			slog.Error("failed to encrypt secret value", "key", req.Key, "error", err)
			return fiber.NewError(fiber.StatusInternalServerError, "failed to encrypt value")
		}
		storedValue = encrypted
	}

	var setting models.Settings
	result := s.db.Where("key = ?", req.Key).First(&setting)

	if result.Error != nil {
		// Create new.
		setting = models.Settings{
			Key:      req.Key,
			Value:    storedValue,
			IsSecret: isSecret,
		}
		if err := s.db.Create(&setting).Error; err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "failed to create setting")
		}
	} else {
		// Update existing.
		updates := map[string]interface{}{
			"value":     storedValue,
			"is_secret": isSecret,
		}
		if err := s.db.Model(&setting).Updates(updates).Error; err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "failed to update setting")
		}
		setting.Value = storedValue
		setting.IsSecret = isSecret
	}

	return c.JSON(maskSetting(setting))
}

// DeleteSetting removes a setting by key.
func (s *Server) DeleteSetting(c *fiber.Ctx) error {
	key := c.Params("key")
	var setting models.Settings
	if err := s.db.Where("key = ?", key).First(&setting).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "setting not found")
	}
	if err := s.db.Delete(&setting).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to delete setting")
	}
	return c.SendStatus(fiber.StatusNoContent)
}
