package api

import (
	"log/slog"
	"time"

	"github.com/gofiber/fiber/v2"
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
