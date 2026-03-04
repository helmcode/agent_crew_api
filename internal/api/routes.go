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
	teams.Get("/:id/agents/:agentId/instructions", s.GetInstructions)
	teams.Put("/:id/agents/:agentId/instructions", s.UpdateInstructions)
	teams.Post("/:id/agents/:agentId/skills/install", s.InstallAgentSkill)

	// MCP server management (team-level).
	teams.Get("/:id/mcp", s.GetMcpConfig)
	teams.Put("/:id/mcp", s.UpdateMcpConfig)
	teams.Post("/:id/mcp/servers", s.AddMcpServer)
	teams.Delete("/:id/mcp/servers/:serverName", s.RemoveMcpServer)

	// Chat.
	teams.Post("/:id/chat", s.SendChat)
	teams.Get("/:id/messages", s.GetMessages)
	teams.Get("/:id/activity", s.GetActivity)

	// Schedules.
	schedules := api.Group("/schedules")
	schedules.Get("/config", s.GetScheduleConfig)
	schedules.Get("/", s.ListSchedules)
	schedules.Post("/", s.CreateSchedule)
	schedules.Get("/:id", s.GetSchedule)
	schedules.Put("/:id", s.UpdateSchedule)
	schedules.Delete("/:id", s.DeleteSchedule)
	schedules.Patch("/:id/toggle", s.ToggleSchedule)
	schedules.Get("/:id/runs", s.ListScheduleRuns)
	schedules.Get("/:id/runs/:runId", s.GetScheduleRun)

	// Webhooks.
	webhooks := api.Group("/webhooks")
	webhooks.Get("/", s.ListWebhooks)
	webhooks.Post("/", s.CreateWebhook)
	webhooks.Get("/:id", s.GetWebhook)
	webhooks.Put("/:id", s.UpdateWebhook)
	webhooks.Delete("/:id", s.DeleteWebhook)
	webhooks.Patch("/:id/toggle", s.ToggleWebhook)
	webhooks.Post("/:id/regenerate", s.RegenerateWebhookToken)
	webhooks.Get("/:id/runs", s.ListWebhookRuns)
	webhooks.Get("/:id/runs/:runId", s.GetWebhookRun)

	// Webhook trigger (outside /api, authenticated by token).
	s.App.Post("/webhook/trigger/:token", s.TriggerWebhook)

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
