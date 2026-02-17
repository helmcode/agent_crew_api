package api

import (
	"log/slog"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/gofiber/fiber/v2/middleware/requestid"
	"gorm.io/gorm"

	"github.com/helmcode/agent-crew/internal/runtime"
)

// Server holds dependencies for the HTTP API.
type Server struct {
	App     *fiber.App
	db      *gorm.DB
	runtime runtime.AgentRuntime
}

// NewServer creates a Fiber app with middleware and registers all routes.
func NewServer(db *gorm.DB, rt runtime.AgentRuntime) *Server {
	app := fiber.New(fiber.Config{
		AppName:      "AgentCrew API",
		ErrorHandler: globalErrorHandler,
	})

	// Middleware.
	app.Use(recover.New())
	app.Use(requestid.New())
	app.Use(cors.New(cors.Config{
		AllowOrigins: "*",
		AllowMethods: "GET,POST,PUT,DELETE,OPTIONS",
		AllowHeaders: "Origin,Content-Type,Accept,Authorization",
	}))
	app.Use(requestLogger())

	s := &Server{
		App:     app,
		db:      db,
		runtime: rt,
	}

	s.registerRoutes()
	return s
}

// Listen starts the HTTP server on the given address.
func (s *Server) Listen(addr string) error {
	slog.Info("starting HTTP server", "addr", addr)
	return s.App.Listen(addr)
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown() error {
	slog.Info("shutting down HTTP server")
	return s.App.Shutdown()
}
