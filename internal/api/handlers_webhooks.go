package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/helmcode/agent-crew/internal/models"
	"github.com/helmcode/agent-crew/internal/postaction"
	"github.com/helmcode/agent-crew/internal/protocol"
)

// generateWebhookToken creates a new webhook token with its hash and prefix.
func generateWebhookToken() (token string, hash string, prefix string, err error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", "", "", fmt.Errorf("generating random bytes: %w", err)
	}
	rawToken := hex.EncodeToString(bytes)
	token = "whk_" + rawToken
	h := sha256.Sum256([]byte(token))
	hash = hex.EncodeToString(h[:])
	prefix = token[:12] + "..."
	return token, hash, prefix, nil
}

// renderPromptTemplate replaces {{key}} placeholders with variable values.
func renderPromptTemplate(tmpl string, vars map[string]string) string {
	result := tmpl
	for k, v := range vars {
		result = strings.ReplaceAll(result, "{{"+k+"}}", v)
	}
	return result
}

// ListWebhooks returns all webhooks with their associated team.
func (s *Server) ListWebhooks(c *fiber.Ctx) error {
	var webhooks []models.Webhook
	if err := s.db.Preload("Team").Find(&webhooks).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to list webhooks")
	}
	return c.JSON(webhooks)
}

// GetWebhook returns a single webhook by ID.
func (s *Server) GetWebhook(c *fiber.Ctx) error {
	id := c.Params("id")
	var webhook models.Webhook
	if err := s.db.Preload("Team").First(&webhook, "id = ?", id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "webhook not found")
	}
	return c.JSON(webhook)
}

// CreateWebhook creates a new webhook and returns it with the secret token.
func (s *Server) CreateWebhook(c *fiber.Ctx) error {
	var req CreateWebhookRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	if req.Name == "" {
		return fiber.NewError(fiber.StatusBadRequest, "name is required")
	}
	if len(req.Name) > 255 {
		return fiber.NewError(fiber.StatusBadRequest, "name must be at most 255 characters")
	}
	if req.TeamID == "" {
		return fiber.NewError(fiber.StatusBadRequest, "team_id is required")
	}
	if req.PromptTemplate == "" {
		return fiber.NewError(fiber.StatusBadRequest, "prompt_template is required")
	}
	if len(req.PromptTemplate) > 50000 {
		return fiber.NewError(fiber.StatusBadRequest, "prompt_template exceeds maximum length of 50000 characters")
	}

	// Validate team exists.
	var team models.Team
	if err := s.db.First(&team, "id = ?", req.TeamID).Error; err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "team_id references a non-existent team")
	}

	// Generate secret token.
	token, hash, prefix, err := generateWebhookToken()
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to generate webhook token")
	}

	timeoutSeconds := 3600
	if req.TimeoutSeconds != nil {
		timeoutSeconds = *req.TimeoutSeconds
	}
	maxConcurrent := 1
	if req.MaxConcurrent != nil {
		maxConcurrent = *req.MaxConcurrent
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	webhook := models.Webhook{
		ID:              uuid.New().String(),
		Name:            req.Name,
		TeamID:          req.TeamID,
		PromptTemplate:  req.PromptTemplate,
		SecretTokenHash: hash,
		SecretPrefix:    prefix,
		Enabled:         enabled,
		TimeoutSeconds:  timeoutSeconds,
		MaxConcurrent:   maxConcurrent,
		Status:          models.WebhookStatusIdle,
	}

	if err := s.db.Create(&webhook).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to create webhook")
	}

	// Reload with team preloaded.
	s.db.Preload("Team").First(&webhook, "id = ?", webhook.ID)

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"webhook": webhook,
		"token":   token,
	})
}

// UpdateWebhook updates a webhook's fields.
func (s *Server) UpdateWebhook(c *fiber.Ctx) error {
	id := c.Params("id")
	var webhook models.Webhook
	if err := s.db.First(&webhook, "id = ?", id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "webhook not found")
	}

	var req UpdateWebhookRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	updates := map[string]interface{}{}

	if req.Name != nil {
		if *req.Name == "" {
			return fiber.NewError(fiber.StatusBadRequest, "name cannot be empty")
		}
		if len(*req.Name) > 255 {
			return fiber.NewError(fiber.StatusBadRequest, "name must be at most 255 characters")
		}
		updates["name"] = *req.Name
	}
	if req.PromptTemplate != nil {
		if *req.PromptTemplate == "" {
			return fiber.NewError(fiber.StatusBadRequest, "prompt_template cannot be empty")
		}
		if len(*req.PromptTemplate) > 50000 {
			return fiber.NewError(fiber.StatusBadRequest, "prompt_template exceeds maximum length of 50000 characters")
		}
		updates["prompt_template"] = *req.PromptTemplate
	}
	if req.TimeoutSeconds != nil {
		updates["timeout_seconds"] = *req.TimeoutSeconds
	}
	if req.MaxConcurrent != nil {
		updates["max_concurrent"] = *req.MaxConcurrent
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}

	if len(updates) > 0 {
		if err := s.db.Model(&webhook).Updates(updates).Error; err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "failed to update webhook")
		}
	}

	s.db.Preload("Team").First(&webhook, "id = ?", id)
	return c.JSON(webhook)
}

// DeleteWebhook removes a webhook and cascades to its runs.
func (s *Server) DeleteWebhook(c *fiber.Ctx) error {
	id := c.Params("id")
	var webhook models.Webhook
	if err := s.db.First(&webhook, "id = ?", id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "webhook not found")
	}

	if err := s.db.Select("Runs").Delete(&webhook).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to delete webhook")
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// ToggleWebhook toggles a webhook's enabled state.
func (s *Server) ToggleWebhook(c *fiber.Ctx) error {
	id := c.Params("id")
	var webhook models.Webhook
	if err := s.db.First(&webhook, "id = ?", id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "webhook not found")
	}

	newEnabled := !webhook.Enabled
	if err := s.db.Model(&webhook).Update("enabled", newEnabled).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to toggle webhook")
	}

	s.db.Preload("Team").First(&webhook, "id = ?", id)
	return c.JSON(webhook)
}

// RegenerateWebhookToken generates a new secret token for a webhook.
func (s *Server) RegenerateWebhookToken(c *fiber.Ctx) error {
	id := c.Params("id")
	var webhook models.Webhook
	if err := s.db.First(&webhook, "id = ?", id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "webhook not found")
	}

	token, hash, prefix, err := generateWebhookToken()
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to generate webhook token")
	}

	if err := s.db.Model(&webhook).Updates(map[string]interface{}{
		"secret_token_hash": hash,
		"secret_prefix":     prefix,
	}).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to update webhook token")
	}

	s.db.Preload("Team").First(&webhook, "id = ?", id)
	return c.JSON(fiber.Map{
		"webhook": webhook,
		"token":   token,
	})
}

// ListWebhookRuns returns paginated runs for a webhook, newest first.
func (s *Server) ListWebhookRuns(c *fiber.Ctx) error {
	id := c.Params("id")

	// Verify webhook exists.
	var webhook models.Webhook
	if err := s.db.First(&webhook, "id = ?", id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "webhook not found")
	}

	page, _ := strconv.Atoi(c.Query("page", "1"))
	perPage, _ := strconv.Atoi(c.Query("per_page", "20"))
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}

	var total int64
	s.db.Model(&models.WebhookRun{}).Where("webhook_id = ?", id).Count(&total)

	var runs []models.WebhookRun
	offset := (page - 1) * perPage
	if err := s.db.Where("webhook_id = ?", id).
		Order("started_at DESC").
		Limit(perPage).
		Offset(offset).
		Find(&runs).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to list webhook runs")
	}

	return c.JSON(fiber.Map{
		"data":     runs,
		"total":    total,
		"page":     page,
		"per_page": perPage,
	})
}

// GetWebhookRun returns a single run by webhook and run ID.
func (s *Server) GetWebhookRun(c *fiber.Ctx) error {
	webhookID := c.Params("id")
	runID := c.Params("runId")

	// Verify webhook exists.
	var webhook models.Webhook
	if err := s.db.First(&webhook, "id = ?", webhookID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "webhook not found")
	}

	var run models.WebhookRun
	if err := s.db.First(&run, "id = ? AND webhook_id = ?", runID, webhookID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "webhook run not found")
	}

	return c.JSON(run)
}

// TriggerWebhook handles POST /webhook/trigger/:token — authenticates by token and executes the webhook.
func (s *Server) TriggerWebhook(c *fiber.Ctx) error {
	token := c.Params("token")
	if token == "" {
		return fiber.NewError(fiber.StatusUnauthorized, "missing token")
	}

	// Hash the provided token and look up the webhook.
	h := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(h[:])

	var webhook models.Webhook
	if err := s.db.First(&webhook, "secret_token_hash = ?", tokenHash).Error; err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "invalid token")
	}

	if !webhook.Enabled {
		return fiber.NewError(fiber.StatusForbidden, "webhook is disabled")
	}

	// Verify team is running.
	var team models.Team
	if err := s.db.First(&team, "id = ?", webhook.TeamID).Error; err != nil {
		return fiber.NewError(fiber.StatusConflict, "team not found")
	}
	if team.Status != models.TeamStatusRunning {
		return fiber.NewError(fiber.StatusConflict, "team is not running")
	}

	// Check per-webhook concurrency.
	var perWebhookRunning int64
	s.db.Model(&models.WebhookRun{}).Where("webhook_id = ? AND status = ?", webhook.ID, models.WebhookRunStatusRunning).Count(&perWebhookRunning)
	if int(perWebhookRunning) >= webhook.MaxConcurrent {
		return fiber.NewError(fiber.StatusTooManyRequests, "webhook concurrency limit reached")
	}

	// Check global concurrency.
	var globalRunning int64
	s.db.Model(&models.WebhookRun{}).Where("status = ?", models.WebhookRunStatusRunning).Count(&globalRunning)
	if int(globalRunning) >= s.webhookMaxConcurrent {
		return fiber.NewError(fiber.StatusTooManyRequests, "global webhook concurrency limit reached")
	}

	// Parse request body for variables.
	var req TriggerWebhookRequest
	if err := c.BodyParser(&req); err != nil {
		// Body is optional — variables may be absent.
		req.Variables = nil
	}

	// Validate variables.
	if req.Variables != nil {
		if len(req.Variables) > 50 {
			return fiber.NewError(fiber.StatusBadRequest, "too many variables (max 50)")
		}
		for k, v := range req.Variables {
			if len(k) > 1000 {
				return fiber.NewError(fiber.StatusBadRequest, "variable key exceeds 1000 characters")
			}
			if len(v) > 10000 {
				return fiber.NewError(fiber.StatusBadRequest, "variable value exceeds 10000 characters")
			}
		}
	}

	// Render prompt template.
	prompt := renderPromptTemplate(webhook.PromptTemplate, req.Variables)
	if len(prompt) > 50000 {
		return fiber.NewError(fiber.StatusBadRequest, "rendered prompt exceeds maximum length of 50000 characters")
	}

	// Serialize request payload.
	payloadJSON, _ := json.Marshal(req)

	// Create webhook run record.
	now := time.Now()
	run := models.WebhookRun{
		ID:             uuid.New().String(),
		WebhookID:      webhook.ID,
		StartedAt:      now,
		Status:         models.WebhookRunStatusRunning,
		PromptSent:     prompt,
		RequestPayload: string(payloadJSON),
		CallerIP:       c.IP(),
	}

	if err := s.db.Create(&run).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to create webhook run")
	}

	// Update webhook status.
	s.db.Model(&webhook).Updates(map[string]interface{}{
		"last_triggered_at": now,
		"status":            models.WebhookStatusRunning,
	})

	// Detect execution mode.
	if c.Query("wait") == "true" {
		// Synchronous: execute inline and return response.
		timeout := time.Duration(webhook.TimeoutSeconds) * time.Second
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		start := time.Now()
		responseText, err := s.sendWebhookPromptAndWait(ctx, SanitizeName(team.Name), prompt, run.ID)
		durationMs := time.Since(start).Milliseconds()

		finished := time.Now()
		updates := map[string]interface{}{"finished_at": finished}

		resp := TriggerWebhookResponse{
			RunID:      run.ID,
			DurationMs: durationMs,
		}

		if err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				updates["status"] = models.WebhookRunStatusTimeout
				updates["error"] = fmt.Sprintf("execution timed out after %ds", webhook.TimeoutSeconds)
			} else {
				updates["status"] = models.WebhookRunStatusFailed
				updates["error"] = err.Error()
			}
			resp.Status = updates["status"].(string)
			resp.Error = updates["error"].(string)
		} else {
			updates["status"] = models.WebhookRunStatusSuccess
			updates["response_received"] = responseText
			resp.Status = models.WebhookRunStatusSuccess
			resp.Response = responseText
		}

		s.db.Model(&models.WebhookRun{}).Where("id = ?", run.ID).Updates(updates)
		s.updateWebhookIdleStatus(webhook.ID)

		// Fire post-actions (fire-and-forget, uses goroutine internally).
		runStatus := updates["status"].(string)
		runError, _ := updates["error"].(string)
		runResponse, _ := updates["response_received"].(string)
		s.postActionExec.ExecutePostActions(postaction.PostActionContext{
			SourceType:  "webhook",
			TriggerID:   webhook.ID,
			RunID:       run.ID,
			Status:      runStatus,
			Response:    runResponse,
			Error:       runError,
			TriggerName: webhook.Name,
			TeamName:    team.Name,
			Prompt:      prompt,
			StartedAt:   run.StartedAt.Format(time.RFC3339),
			FinishedAt:  finished.Format(time.RFC3339),
		})

		return c.JSON(resp)
	}

	// Asynchronous: execute in goroutine.
	s.executeWebhookAsync(webhook, run, team, prompt)

	return c.Status(fiber.StatusAccepted).JSON(TriggerWebhookResponse{
		RunID:  run.ID,
		Status: models.WebhookRunStatusRunning,
	})
}

// executeWebhookAsync runs the webhook prompt in a background goroutine.
func (s *Server) executeWebhookAsync(webhook models.Webhook, run models.WebhookRun, team models.Team, prompt string) {
	go func() {
		timeout := time.Duration(webhook.TimeoutSeconds) * time.Second
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		responseText, err := s.sendWebhookPromptAndWait(ctx, SanitizeName(team.Name), prompt, run.ID)

		finished := time.Now()
		updates := map[string]interface{}{"finished_at": finished}

		if err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				updates["status"] = models.WebhookRunStatusTimeout
				updates["error"] = fmt.Sprintf("execution timed out after %ds", webhook.TimeoutSeconds)
			} else {
				updates["status"] = models.WebhookRunStatusFailed
				updates["error"] = err.Error()
			}
		} else {
			updates["status"] = models.WebhookRunStatusSuccess
			updates["response_received"] = responseText
		}

		s.db.Model(&models.WebhookRun{}).Where("id = ?", run.ID).Updates(updates)
		s.updateWebhookIdleStatus(webhook.ID)

		// Fire post-actions (fire-and-forget).
		runStatus := updates["status"].(string)
		runError, _ := updates["error"].(string)
		runResponse, _ := updates["response_received"].(string)
		s.postActionExec.ExecutePostActions(postaction.PostActionContext{
			SourceType:  "webhook",
			TriggerID:   webhook.ID,
			RunID:       run.ID,
			Status:      runStatus,
			Response:    runResponse,
			Error:       runError,
			TriggerName: webhook.Name,
			TeamName:    team.Name,
			Prompt:      prompt,
			StartedAt:   run.StartedAt.Format(time.RFC3339),
			FinishedAt:  finished.Format(time.RFC3339),
		})
	}()
}

// updateWebhookIdleStatus resets a webhook's status to idle if no more runs are active.
func (s *Server) updateWebhookIdleStatus(webhookID string) {
	var runningCount int64
	s.db.Model(&models.WebhookRun{}).Where("webhook_id = ? AND status = ?", webhookID, models.WebhookRunStatusRunning).Count(&runningCount)
	if runningCount == 0 {
		s.db.Model(&models.Webhook{}).Where("id = ?", webhookID).Update("status", models.WebhookStatusIdle)
	}
}

// sendWebhookPromptAndWait connects to NATS, sends a prompt, and waits for the leader response.
func (s *Server) sendWebhookPromptAndWait(ctx context.Context, teamName, prompt, runID string) (string, error) {
	natsURL, err := s.runtime.GetNATSConnectURL(ctx, teamName)
	if err != nil {
		return "", fmt.Errorf("resolving NATS URL: %w", err)
	}

	token := os.Getenv("NATS_AUTH_TOKEN")
	opts := []nats.Option{
		nats.Name("agentcrew-webhook"),
		nats.Timeout(5 * time.Second),
	}
	if token != "" {
		opts = append(opts, nats.Token(token))
	}

	nc, err := nats.Connect(natsURL, opts...)
	if err != nil {
		return "", fmt.Errorf("connecting to NATS: %w", err)
	}
	defer nc.Close()

	// Subscribe to the leader channel BEFORE sending the prompt to avoid
	// missing the response in a race.
	subject, err := protocol.TeamLeaderChannel(teamName)
	if err != nil {
		return "", fmt.Errorf("building leader channel: %w", err)
	}

	slog.Info("webhook: subscribing to NATS subject",
		"subject", subject, "team_name", teamName, "run_id", runID)

	type leaderResult struct {
		text string
	}
	responseCh := make(chan leaderResult, 1)
	sub, err := nc.Subscribe(subject, func(msg *nats.Msg) {
		var protoMsg protocol.Message
		if err := json.Unmarshal(msg.Data, &protoMsg); err != nil {
			slog.Warn("webhook: failed to unmarshal NATS message",
				"subject", subject, "error", err)
			return
		}

		if protoMsg.Type == protocol.TypeLeaderResponse {
			var payload protocol.LeaderResponsePayload
			responseText := ""
			if err := json.Unmarshal(protoMsg.Payload, &payload); err == nil {
				if payload.Error != "" {
					responseText = "Error: " + payload.Error
				} else {
					responseText = payload.Result
				}
			}

			// Only accept responses tagged with our exact run ID.
			// The bridge FIFO uses ScheduledRunID for all correlation (chat, scheduler, webhook).
			if payload.ScheduledRunID != runID {
				slog.Debug("webhook: ignoring response for different run",
					"expected_run_id", runID, "got_run_id", payload.ScheduledRunID)
				return
			}

			slog.Info("webhook: received leader response",
				"subject", subject, "status", payload.Status,
				"run_id", runID, "response_length", len(responseText))

			select {
			case responseCh <- leaderResult{text: responseText}:
			default:
			}
		}
	})
	if err != nil {
		return "", fmt.Errorf("subscribing to leader channel: %w", err)
	}
	defer sub.Unsubscribe()

	// Build and send the prompt with webhook metadata.
	// Use ScheduledRunID for correlation — the bridge FIFO queue only handles
	// this field generically, regardless of the source.
	protoMsg, err := protocol.NewMessage("webhook", "leader", protocol.TypeUserMessage, protocol.UserMessagePayload{
		Content:        prompt,
		Source:         "webhook",
		ScheduledRunID: runID,
	})
	if err != nil {
		return "", fmt.Errorf("building protocol message: %w", err)
	}

	data, err := json.Marshal(protoMsg)
	if err != nil {
		return "", fmt.Errorf("marshaling message: %w", err)
	}

	if err := nc.Publish(subject, data); err != nil {
		return "", fmt.Errorf("publishing prompt: %w", err)
	}
	if err := nc.Flush(); err != nil {
		return "", fmt.Errorf("flushing prompt: %w", err)
	}

	slog.Info("webhook: prompt sent, waiting for leader response via NATS",
		"team", teamName, "subject", subject, "run_id", runID)

	// Wait for the response or context cancellation.
	select {
	case result := <-responseCh:
		return result.text, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
