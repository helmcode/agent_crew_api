package api

import (
	"github.com/gofiber/contrib/websocket"
	"github.com/gofiber/fiber/v2"
)

func (s *Server) registerRoutes() {
	// Health check (public).
	s.App.Get("/health", s.HealthCheck)

	// Webhook trigger (public, token-authenticated).
	s.App.Post("/webhook/trigger/:token", s.TriggerWebhook)

	api := s.App.Group("/api")

	// Auth (public endpoints — no JWT required).
	authGroup := api.Group("/auth")
	authGroup.Get("/config", s.GetAuthConfig)
	authGroup.Post("/login", s.Login)
	authGroup.Post("/register", s.Register)
	authGroup.Post("/register/invite", s.RegisterWithInvite)
	authGroup.Post("/refresh", s.RefreshToken)
	authGroup.Get("/invite/:token", s.GetInviteInfo)

	// --- All routes below require authentication ---
	api.Use(authMiddleware(s.authProvider))

	// Auth (authenticated endpoints).
	authGroup.Get("/me", s.GetMe)
	authGroup.Put("/me", s.UpdateMe)
	authGroup.Put("/me/password", s.ChangePassword)

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

	// Reverse lookups: post-actions bound to a specific webhook or schedule.
	webhooks.Get("/:id/post-actions", s.GetWebhookPostActions)
	schedules.Get("/:id/post-actions", s.GetSchedulePostActions)

	// Post-Actions.
	postActions := api.Group("/post-actions")
	postActions.Get("/", s.ListPostActions)
	postActions.Post("/", s.CreatePostAction)
	postActions.Get("/:id", s.GetPostAction)
	postActions.Put("/:id", s.UpdatePostAction)
	postActions.Delete("/:id", s.DeletePostAction)
	postActions.Post("/:id/bindings", s.CreateBinding)
	postActions.Put("/:id/bindings/:bid", s.UpdateBinding)
	postActions.Delete("/:id/bindings/:bid", s.DeleteBinding)
	postActions.Get("/:id/runs", s.ListPostActionRuns)

	// Settings.
	api.Get("/settings", s.GetSettings)
	api.Put("/settings", s.UpdateSettings)
	api.Delete("/settings/:key", s.DeleteSetting)

	// Organization management.
	org := api.Group("/org")
	org.Get("/", s.GetOrg)
	org.Put("/", s.UpdateOrg)
	org.Get("/members", s.ListMembers)
	org.Put("/members/:id/role", s.UpdateMemberRole)
	org.Post("/members/:id/reset-password", s.ResetMemberPassword)
	org.Delete("/members/:id", s.RemoveMember)
	org.Get("/invites", s.ListInvites)
	org.Post("/invites", s.CreateInvite)
	org.Delete("/invites/:id", s.DeleteInvite)

	// WebSocket endpoints — authenticated via query param ?token= or noop auto-auth.
	s.App.Use("/ws", func(c *fiber.Ctx) error {
		if !websocket.IsWebSocketUpgrade(c) {
			return fiber.ErrUpgradeRequired
		}

		// Authenticate: noop provider skips token validation.
		if s.authProvider.ProviderName() == "noop" {
			claims, _ := s.authProvider.ValidateToken(c.Context(), "")
			c.Locals("user_id", claims.UserID)
			c.Locals("org_id", claims.OrgID)
			c.Locals("role", claims.Role)
			return c.Next()
		}

		// For other providers, require token as query param.
		token := c.Query("token")
		if token == "" {
			return fiber.NewError(fiber.StatusUnauthorized, "missing token query parameter")
		}
		claims, err := s.authProvider.ValidateToken(c.Context(), token)
		if err != nil {
			return fiber.NewError(fiber.StatusUnauthorized, "invalid or expired token")
		}
		c.Locals("user_id", claims.UserID)
		c.Locals("org_id", claims.OrgID)
		c.Locals("role", claims.Role)
		return c.Next()
	})
	s.App.Get("/ws/teams/:id/logs", websocket.New(s.StreamLogs))
	s.App.Get("/ws/teams/:id/activity", websocket.New(s.StreamActivity))
}
