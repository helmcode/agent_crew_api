package api

import (
	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"github.com/helmcode/agent-crew/internal/models"
)

// GetOrgID extracts the organization ID from the request context.
func GetOrgID(c *fiber.Ctx) string {
	v, _ := c.Locals("org_id").(string)
	return v
}

// GetUserID extracts the user ID from the request context.
func GetUserID(c *fiber.Ctx) string {
	v, _ := c.Locals("user_id").(string)
	return v
}

// GetRole extracts the user role from the request context.
func GetRole(c *fiber.Ctx) string {
	v, _ := c.Locals("role").(string)
	return v
}

// IsAdmin returns true if the authenticated user has the admin role.
func IsAdmin(c *fiber.Ctx) bool {
	return GetRole(c) == models.UserRoleAdmin
}

// OrgScope returns a GORM scope that filters queries by the request's org_id.
func OrgScope(c *fiber.Ctx) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("org_id = ?", GetOrgID(c))
	}
}
