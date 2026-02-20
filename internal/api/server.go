package api

import (
	"context"
	"log/slog"
	"sync"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/gofiber/fiber/v2/middleware/requestid"
	"gorm.io/gorm"

	"github.com/helmcode/agent-crew/internal/models"
	"github.com/helmcode/agent-crew/internal/runtime"
)

// Server holds dependencies for the HTTP API.
type Server struct {
	App     *fiber.App
	db      *gorm.DB
	runtime runtime.AgentRuntime

	// relays tracks active NATS relay goroutines per team ID.
	// The cancel function stops the relay when the team is stopped.
	relaysMu sync.Mutex
	relays   map[string]context.CancelFunc
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
		relays:  make(map[string]context.CancelFunc),
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

// ReconnectRelays restarts NATS relay goroutines for all teams that are
// currently in "running" status. This must be called at API startup so
// that teams deployed before a server restart continue to have their
// agent messages relayed from NATS into the database.
func (s *Server) ReconnectRelays() {
	var teams []models.Team
	if err := s.db.Where("status = ?", models.TeamStatusRunning).Find(&teams).Error; err != nil {
		slog.Error("failed to query running teams for relay reconnect", "error", err)
		return
	}

	if len(teams) == 0 {
		slog.Info("no running teams to reconnect relays for")
		return
	}

	for _, team := range teams {
		slog.Info("reconnecting relay for running team", "team", team.Name, "id", team.ID)
		s.startTeamRelay(team.ID, team.Name)
	}
	slog.Info("relay reconnect complete", "teams_reconnected", len(teams))
}
