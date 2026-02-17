package api

import (
	"github.com/gofiber/fiber/v2"
)

// HealthCheck verifies API and database connectivity.
func (s *Server) HealthCheck(c *fiber.Ctx) error {
	var errors []string

	var result int
	if err := s.db.Raw("SELECT 1").Scan(&result).Error; err != nil {
		errors = append(errors, "database: "+err.Error())
	}

	if len(errors) > 0 {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"status": "unhealthy",
			"errors": errors,
		})
	}

	return c.JSON(fiber.Map{"status": "ok"})
}
