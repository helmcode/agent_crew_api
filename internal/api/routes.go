package api

import (
	"github.com/gofiber/contrib/websocket"
	"github.com/gofiber/fiber/v2"
)

func (s *Server) registerRoutes() {
	// Health check.
	s.App.Get("/health", s.HealthCheck)

	api := s.App.Group("/api")

	// Teams.
	teams := api.Group("/teams")
	teams.Get("/", s.ListTeams)
	teams.Post("/", s.CreateTeam)
	teams.Get("/:id", s.GetTeam)
	teams.Put("/:id", s.UpdateTeam)
	teams.Delete("/:id", s.DeleteTeam)

	// Team lifecycle.
	teams.Post("/:id/deploy", s.DeployTeam)
	teams.Post("/:id/stop", s.StopTeam)

	// Agents (nested under teams).
	teams.Get("/:id/agents", s.ListAgents)
	teams.Post("/:id/agents", s.CreateAgent)
	teams.Get("/:id/agents/:agentId", s.GetAgent)
	teams.Put("/:id/agents/:agentId", s.UpdateAgent)
	teams.Delete("/:id/agents/:agentId", s.DeleteAgent)

	// Chat.
	teams.Post("/:id/chat", s.SendChat)
	teams.Get("/:id/messages", s.GetMessages)

	// Settings.
	api.Get("/settings", s.GetSettings)
	api.Put("/settings", s.UpdateSettings)
	api.Delete("/settings/:key", s.DeleteSetting)

	// WebSocket endpoints.
	s.App.Use("/ws", func(c *fiber.Ctx) error {
		if websocket.IsWebSocketUpgrade(c) {
			return c.Next()
		}
		return fiber.ErrUpgradeRequired
	})
	s.App.Get("/ws/teams/:id/logs", websocket.New(s.StreamLogs))
	s.App.Get("/ws/teams/:id/activity", websocket.New(s.StreamActivity))
}
