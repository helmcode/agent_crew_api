package api

import (
	"encoding/json"
	"log/slog"
	"net/url"
	"strconv"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/helmcode/agent-crew/internal/crypto"
	"github.com/helmcode/agent-crew/internal/models"
)

// validPostActionMethods is the set of allowed HTTP methods.
var validPostActionMethods = map[string]bool{
	models.PostActionMethodGET:    true,
	models.PostActionMethodPOST:   true,
	models.PostActionMethodPUT:    true,
	models.PostActionMethodPATCH:  true,
	models.PostActionMethodDELETE: true,
}

// validPostActionAuthTypes is the set of allowed auth types.
var validPostActionAuthTypes = map[string]bool{
	models.PostActionAuthNone:   true,
	models.PostActionAuthBearer: true,
	models.PostActionAuthBasic:  true,
	models.PostActionAuthHeader: true,
}

// validTriggerTypes is the set of allowed trigger types.
var validTriggerTypes = map[string]bool{
	models.PostActionTriggerWebhook:  true,
	models.PostActionTriggerSchedule: true,
}

// validTriggerOnValues is the set of allowed trigger-on conditions.
var validTriggerOnValues = map[string]bool{
	models.PostActionTriggerOnSuccess: true,
	models.PostActionTriggerOnFailure: true,
	models.PostActionTriggerOnAny:     true,
}

// ListPostActions returns all post-actions with a bindings count.
func (s *Server) ListPostActions(c *fiber.Ctx) error {
	var postActions []models.PostAction
	if err := s.db.Find(&postActions).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to list post-actions")
	}

	type postActionWithCount struct {
		models.PostAction
		BindingsCount int64 `json:"bindings_count"`
	}

	result := make([]postActionWithCount, len(postActions))
	for i, pa := range postActions {
		var count int64
		s.db.Model(&models.PostActionBinding{}).Where("post_action_id = ?", pa.ID).Count(&count)
		result[i] = postActionWithCount{
			PostAction:    pa,
			BindingsCount: count,
		}
	}

	return c.JSON(result)
}

// CreatePostAction creates a new post-action.
func (s *Server) CreatePostAction(c *fiber.Ctx) error {
	var req CreatePostActionRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	// Validate required fields.
	if req.Name == "" {
		return fiber.NewError(fiber.StatusBadRequest, "name is required")
	}
	if len(req.Name) > 255 {
		return fiber.NewError(fiber.StatusBadRequest, "name must be at most 255 characters")
	}
	if req.Method == "" {
		return fiber.NewError(fiber.StatusBadRequest, "method is required")
	}
	if !validPostActionMethods[req.Method] {
		return fiber.NewError(fiber.StatusBadRequest, "method must be one of GET, POST, PUT, PATCH, DELETE")
	}
	if req.URL == "" {
		return fiber.NewError(fiber.StatusBadRequest, "url is required")
	}
	if _, err := url.ParseRequestURI(req.URL); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "url is not a valid URL")
	}

	// Validate auth_type.
	authType := models.PostActionAuthNone
	if req.AuthType != "" {
		if !validPostActionAuthTypes[req.AuthType] {
			return fiber.NewError(fiber.StatusBadRequest, "auth_type must be one of none, bearer, basic, header")
		}
		authType = req.AuthType
	}

	// Validate timeout and retry.
	timeoutSeconds := 30
	if req.TimeoutSeconds != nil {
		if *req.TimeoutSeconds < 1 || *req.TimeoutSeconds > 300 {
			return fiber.NewError(fiber.StatusBadRequest, "timeout_seconds must be between 1 and 300")
		}
		timeoutSeconds = *req.TimeoutSeconds
	}
	retryCount := 0
	if req.RetryCount != nil {
		if *req.RetryCount < 0 || *req.RetryCount > 5 {
			return fiber.NewError(fiber.StatusBadRequest, "retry_count must be between 0 and 5")
		}
		retryCount = *req.RetryCount
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	// Marshal headers to JSON.
	headersJSON, err := json.Marshal(req.Headers)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid headers format")
	}

	// Encrypt and marshal auth_config.
	authConfigJSON, err := encryptAuthConfig(req.AuthConfig)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to process auth_config")
	}

	postAction := models.PostAction{
		ID:             uuid.New().String(),
		Name:           req.Name,
		Description:    req.Description,
		Method:         req.Method,
		URL:            req.URL,
		Headers:        models.JSON(headersJSON),
		BodyTemplate:   req.BodyTemplate,
		AuthType:       authType,
		AuthConfig:     models.JSON(authConfigJSON),
		TimeoutSeconds: timeoutSeconds,
		RetryCount:     retryCount,
		Enabled:        enabled,
	}

	if err := s.db.Create(&postAction).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to create post-action")
	}

	return c.Status(fiber.StatusCreated).JSON(postAction)
}

// GetPostAction returns a single post-action by ID with enriched bindings.
func (s *Server) GetPostAction(c *fiber.Ctx) error {
	id := c.Params("id")
	var postAction models.PostAction
	if err := s.db.Preload("Bindings").First(&postAction, "id = ?", id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "post-action not found")
	}

	// Enrich bindings with trigger names.
	enrichedBindings := make([]PostActionBindingResponse, len(postAction.Bindings))
	for i, b := range postAction.Bindings {
		enrichedBindings[i] = PostActionBindingResponse{
			PostActionBinding: b,
			TriggerName:       s.resolveTriggerName(b.TriggerType, b.TriggerID),
		}
	}

	// Build response with enriched bindings.
	type postActionDetailResponse struct {
		models.PostAction
		Bindings []PostActionBindingResponse `json:"bindings"`
	}

	resp := postActionDetailResponse{
		PostAction: postAction,
		Bindings:   enrichedBindings,
	}
	// Clear the original bindings so they don't appear twice.
	resp.PostAction.Bindings = nil

	return c.JSON(resp)
}

// UpdatePostAction updates a post-action's fields.
func (s *Server) UpdatePostAction(c *fiber.Ctx) error {
	id := c.Params("id")
	var postAction models.PostAction
	if err := s.db.First(&postAction, "id = ?", id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "post-action not found")
	}

	var req UpdatePostActionRequest
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
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.Method != nil {
		if !validPostActionMethods[*req.Method] {
			return fiber.NewError(fiber.StatusBadRequest, "method must be one of GET, POST, PUT, PATCH, DELETE")
		}
		updates["method"] = *req.Method
	}
	if req.URL != nil {
		if *req.URL == "" {
			return fiber.NewError(fiber.StatusBadRequest, "url cannot be empty")
		}
		if _, err := url.ParseRequestURI(*req.URL); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "url is not a valid URL")
		}
		updates["url"] = *req.URL
	}
	if req.Headers != nil {
		headersJSON, err := json.Marshal(req.Headers)
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid headers format")
		}
		updates["headers"] = string(headersJSON)
	}
	if req.BodyTemplate != nil {
		updates["body_template"] = *req.BodyTemplate
	}
	if req.AuthType != nil {
		if !validPostActionAuthTypes[*req.AuthType] {
			return fiber.NewError(fiber.StatusBadRequest, "auth_type must be one of none, bearer, basic, header")
		}
		updates["auth_type"] = *req.AuthType
	}
	if req.AuthConfig != nil {
		authConfigJSON, err := encryptAuthConfig(req.AuthConfig)
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "failed to process auth_config")
		}
		updates["auth_config"] = string(authConfigJSON)
	}
	if req.TimeoutSeconds != nil {
		if *req.TimeoutSeconds < 1 || *req.TimeoutSeconds > 300 {
			return fiber.NewError(fiber.StatusBadRequest, "timeout_seconds must be between 1 and 300")
		}
		updates["timeout_seconds"] = *req.TimeoutSeconds
	}
	if req.RetryCount != nil {
		if *req.RetryCount < 0 || *req.RetryCount > 5 {
			return fiber.NewError(fiber.StatusBadRequest, "retry_count must be between 0 and 5")
		}
		updates["retry_count"] = *req.RetryCount
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}

	if len(updates) > 0 {
		if err := s.db.Model(&postAction).Updates(updates).Error; err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "failed to update post-action")
		}
	}

	s.db.First(&postAction, "id = ?", id)
	return c.JSON(postAction)
}

// DeletePostAction removes a post-action and cascades to its bindings.
func (s *Server) DeletePostAction(c *fiber.Ctx) error {
	id := c.Params("id")
	var postAction models.PostAction
	if err := s.db.First(&postAction, "id = ?", id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "post-action not found")
	}

	if err := s.db.Select("Bindings").Delete(&postAction).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to delete post-action")
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// CreateBinding creates a new binding for a post-action.
func (s *Server) CreateBinding(c *fiber.Ctx) error {
	postActionID := c.Params("id")

	// Verify post-action exists.
	var postAction models.PostAction
	if err := s.db.First(&postAction, "id = ?", postActionID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "post-action not found")
	}

	var req CreateBindingRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	// Validate trigger_type.
	if !validTriggerTypes[req.TriggerType] {
		return fiber.NewError(fiber.StatusBadRequest, "trigger_type must be webhook or schedule")
	}

	// Validate trigger_id references an existing resource.
	if req.TriggerID == "" {
		return fiber.NewError(fiber.StatusBadRequest, "trigger_id is required")
	}
	if err := s.validateTriggerExists(req.TriggerType, req.TriggerID); err != nil {
		return err
	}

	// Validate trigger_on.
	if !validTriggerOnValues[req.TriggerOn] {
		return fiber.NewError(fiber.StatusBadRequest, "trigger_on must be success, failure, or any")
	}

	// Check for duplicate binding.
	var existingCount int64
	s.db.Model(&models.PostActionBinding{}).
		Where("post_action_id = ? AND trigger_type = ? AND trigger_id = ? AND trigger_on = ?",
			postActionID, req.TriggerType, req.TriggerID, req.TriggerOn).
		Count(&existingCount)
	if existingCount > 0 {
		return fiber.NewError(fiber.StatusConflict, "a binding with this trigger_type, trigger_id, and trigger_on already exists")
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	binding := models.PostActionBinding{
		ID:           uuid.New().String(),
		PostActionID: postActionID,
		TriggerType:  req.TriggerType,
		TriggerID:    req.TriggerID,
		TriggerOn:    req.TriggerOn,
		BodyOverride: req.BodyOverride,
		Enabled:      enabled,
	}

	if err := s.db.Create(&binding).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to create binding")
	}

	resp := PostActionBindingResponse{
		PostActionBinding: binding,
		TriggerName:       s.resolveTriggerName(req.TriggerType, req.TriggerID),
	}

	return c.Status(fiber.StatusCreated).JSON(resp)
}

// UpdateBinding updates a binding's fields.
func (s *Server) UpdateBinding(c *fiber.Ctx) error {
	postActionID := c.Params("id")
	bindingID := c.Params("bid")

	// Verify post-action exists.
	var postAction models.PostAction
	if err := s.db.First(&postAction, "id = ?", postActionID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "post-action not found")
	}

	var binding models.PostActionBinding
	if err := s.db.First(&binding, "id = ? AND post_action_id = ?", bindingID, postActionID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "binding not found")
	}

	var req UpdateBindingRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	updates := map[string]interface{}{}

	if req.TriggerOn != nil {
		if !validTriggerOnValues[*req.TriggerOn] {
			return fiber.NewError(fiber.StatusBadRequest, "trigger_on must be success, failure, or any")
		}
		updates["trigger_on"] = *req.TriggerOn
	}
	if req.BodyOverride != nil {
		updates["body_override"] = *req.BodyOverride
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}

	if len(updates) > 0 {
		if err := s.db.Model(&binding).Updates(updates).Error; err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "failed to update binding")
		}
	}

	s.db.First(&binding, "id = ?", bindingID)
	return c.JSON(binding)
}

// DeleteBinding removes a binding.
func (s *Server) DeleteBinding(c *fiber.Ctx) error {
	postActionID := c.Params("id")
	bindingID := c.Params("bid")

	// Verify post-action exists.
	var postAction models.PostAction
	if err := s.db.First(&postAction, "id = ?", postActionID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "post-action not found")
	}

	var binding models.PostActionBinding
	if err := s.db.First(&binding, "id = ? AND post_action_id = ?", bindingID, postActionID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "binding not found")
	}

	if err := s.db.Delete(&binding).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to delete binding")
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// ListPostActionRuns returns paginated runs for a post-action, newest first.
func (s *Server) ListPostActionRuns(c *fiber.Ctx) error {
	id := c.Params("id")

	// Verify post-action exists.
	var postAction models.PostAction
	if err := s.db.First(&postAction, "id = ?", id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "post-action not found")
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
	s.db.Model(&models.PostActionRun{}).Where("post_action_id = ?", id).Count(&total)

	var runs []models.PostActionRun
	offset := (page - 1) * perPage
	if err := s.db.Where("post_action_id = ?", id).
		Order("triggered_at DESC").
		Limit(perPage).
		Offset(offset).
		Find(&runs).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to list post-action runs")
	}

	return c.JSON(fiber.Map{
		"data":     runs,
		"total":    total,
		"page":     page,
		"per_page": perPage,
	})
}

// GetWebhookPostActions returns all bindings linked to a specific webhook, with PostAction preloaded.
func (s *Server) GetWebhookPostActions(c *fiber.Ctx) error {
	webhookID := c.Params("id")

	// Verify webhook exists.
	var webhook models.Webhook
	if err := s.db.First(&webhook, "id = ?", webhookID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "webhook not found")
	}

	var bindings []models.PostActionBinding
	if err := s.db.Preload("PostAction").
		Where("trigger_type = ? AND trigger_id = ?", models.PostActionTriggerWebhook, webhookID).
		Find(&bindings).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to list webhook post-actions")
	}

	return c.JSON(bindings)
}

// GetSchedulePostActions returns all bindings linked to a specific schedule, with PostAction preloaded.
func (s *Server) GetSchedulePostActions(c *fiber.Ctx) error {
	scheduleID := c.Params("id")

	// Verify schedule exists.
	var schedule models.Schedule
	if err := s.db.First(&schedule, "id = ?", scheduleID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "schedule not found")
	}

	var bindings []models.PostActionBinding
	if err := s.db.Preload("PostAction").
		Where("trigger_type = ? AND trigger_id = ?", models.PostActionTriggerSchedule, scheduleID).
		Find(&bindings).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to list schedule post-actions")
	}

	return c.JSON(bindings)
}

// resolveTriggerName looks up a webhook or schedule by ID and returns its name.
func (s *Server) resolveTriggerName(triggerType, triggerID string) string {
	switch triggerType {
	case models.PostActionTriggerWebhook:
		var webhook models.Webhook
		if err := s.db.First(&webhook, "id = ?", triggerID).Error; err == nil {
			return webhook.Name
		}
	case models.PostActionTriggerSchedule:
		var schedule models.Schedule
		if err := s.db.First(&schedule, "id = ?", triggerID).Error; err == nil {
			return schedule.Name
		}
	}
	return ""
}

// validateTriggerExists checks that a trigger ID references an existing webhook or schedule.
func (s *Server) validateTriggerExists(triggerType, triggerID string) error {
	switch triggerType {
	case models.PostActionTriggerWebhook:
		var webhook models.Webhook
		if err := s.db.First(&webhook, "id = ?", triggerID).Error; err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "trigger_id references a non-existent webhook")
		}
	case models.PostActionTriggerSchedule:
		var schedule models.Schedule
		if err := s.db.First(&schedule, "id = ?", triggerID).Error; err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "trigger_id references a non-existent schedule")
		}
	}
	return nil
}

// encryptAuthConfig encrypts the values in an auth config map and returns the JSON representation.
func encryptAuthConfig(config map[string]string) ([]byte, error) {
	if config == nil {
		return json.Marshal(nil)
	}

	encrypted := make(map[string]string, len(config))
	for k, v := range config {
		enc, err := crypto.Encrypt(v)
		if err != nil {
			slog.Error("failed to encrypt auth_config value", "key", k, "error", err)
			return nil, err
		}
		encrypted[k] = enc
	}

	return json.Marshal(encrypted)
}
