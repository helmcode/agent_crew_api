package api

import (
	"log/slog"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/helmcode/agent-crew/internal/auth"
)

// requestLogger returns a middleware that logs each request.
func requestLogger() fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()
		err := c.Next()
		slog.Info("request",
			"method", c.Method(),
			"path", c.Path(),
			"status", c.Response().StatusCode(),
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", c.Locals("requestid"),
		)
		return err
	}
}

// authMiddleware validates the JWT token and injects user/org claims into
// the request context. For the noop provider, it injects default claims
// without requiring an Authorization header.
func authMiddleware(provider auth.AuthProvider) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Noop provider: inject default claims, no token required.
		if provider.ProviderName() == "noop" {
			claims, _ := provider.ValidateToken(c.Context(), "")
			c.Locals("user_id", claims.UserID)
			c.Locals("org_id", claims.OrgID)
			c.Locals("email", claims.Email)
			c.Locals("name", claims.Name)
			c.Locals("role", claims.Role)
			return c.Next()
		}

		// Extract Bearer token from Authorization header.
		authHeader := c.Get("Authorization")
		if authHeader == "" {
			return fiber.NewError(fiber.StatusUnauthorized, "missing authorization header")
		}

		token := strings.TrimPrefix(authHeader, "Bearer ")
		if token == authHeader {
			return fiber.NewError(fiber.StatusUnauthorized, "invalid authorization format")
		}

		claims, err := provider.ValidateToken(c.Context(), token)
		if err != nil {
			return fiber.NewError(fiber.StatusUnauthorized, "invalid or expired token")
		}

		c.Locals("user_id", claims.UserID)
		c.Locals("org_id", claims.OrgID)
		c.Locals("email", claims.Email)
		c.Locals("name", claims.Name)
		c.Locals("role", claims.Role)
		return c.Next()
	}
}

// globalErrorHandler handles unhandled errors and returns JSON.
// Internal errors (5xx) return a generic message to avoid leaking implementation details.
func globalErrorHandler(c *fiber.Ctx, err error) error {
	code := fiber.StatusInternalServerError
	msg := "internal server error"

	if e, ok := err.(*fiber.Error); ok {
		code = e.Code
		// Only expose error messages for client errors (4xx).
		if code < 500 {
			msg = e.Message
		} else {
			slog.Error("internal error", "error", e.Message, "path", c.Path())
		}
	} else {
		slog.Error("unhandled error", "error", err.Error(), "path", c.Path())
	}

	return c.Status(code).JSON(ErrorResponse{
		Error: msg,
	})
}
