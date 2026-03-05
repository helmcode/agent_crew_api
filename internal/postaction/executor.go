// Package postaction implements the post-action executor engine.
// It renders templates, executes HTTP requests with retry, and records runs.
package postaction

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/helmcode/agent-crew/internal/crypto"
	"github.com/helmcode/agent-crew/internal/models"
)

// maxResponseBody is the maximum response body size stored in a run record.
const maxResponseBody = 4096

// maxBackoff caps exponential backoff at 30 seconds.
const maxBackoff = 30 * time.Second

// maxRetryCount caps retry attempts as defense-in-depth (API validates 0-5).
const maxRetryCount = 5

// maxTimeoutSeconds caps per-request timeout as defense-in-depth (API validates 1-300).
const maxTimeoutSeconds = 300

// PostActionContext carries the data available for template rendering.
type PostActionContext struct {
	SourceType  string // "webhook" or "schedule"
	TriggerID   string
	RunID       string
	Status      string // "success" or "failed"
	Response    string
	Error       string
	TriggerName string
	TeamName    string
	Prompt      string
	StartedAt   string
	FinishedAt  string
}

// Executor orchestrates post-action execution after webhook/schedule runs.
type Executor struct {
	DB     *gorm.DB
	Client *http.Client
}

// NewExecutor creates an Executor with sensible defaults.
func NewExecutor(db *gorm.DB) *Executor {
	return &Executor{
		DB: db,
		Client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// ExecutePostActions finds all matching bindings for the given trigger and
// executes them in parallel goroutines. It is fire-and-forget: it never
// blocks the caller beyond launching goroutines.
func (e *Executor) ExecutePostActions(pctx PostActionContext) {
	var bindings []models.PostActionBinding
	query := e.DB.Preload("PostAction").
		Where("trigger_type = ? AND trigger_id = ? AND enabled = ?", pctx.SourceType, pctx.TriggerID, true)

	if err := query.Find(&bindings).Error; err != nil {
		slog.Error("postaction: failed to query bindings",
			"trigger_type", pctx.SourceType, "trigger_id", pctx.TriggerID, "error", err)
		return
	}

	for _, b := range bindings {
		if !b.PostAction.Enabled {
			continue
		}
		if !matchesTriggerOn(b.TriggerOn, pctx.Status) {
			continue
		}
		go e.executeOne(b, pctx)
	}
}

// matchesTriggerOn checks whether the binding's trigger_on condition matches the run status.
func matchesTriggerOn(triggerOn, status string) bool {
	switch triggerOn {
	case models.PostActionTriggerOnAny:
		return true
	case models.PostActionTriggerOnSuccess:
		return status == "success"
	case models.PostActionTriggerOnFailure:
		return status == "failed" || status == "timeout"
	default:
		return false
	}
}

// executeOne runs a single post-action with retry logic and records the result.
func (e *Executor) executeOne(binding models.PostActionBinding, pctx PostActionContext) {
	action := binding.PostAction
	runID := uuid.New().String()
	now := time.Now()

	run := models.PostActionRun{
		ID:           runID,
		PostActionID: action.ID,
		BindingID:    binding.ID,
		SourceType:   pctx.SourceType,
		SourceRunID:  pctx.RunID,
		TriggeredAt:  now,
		Status:       models.PostActionRunStatusRetrying,
		RequestSent:  action.Method + " " + renderTemplate(action.URL, pctx),
	}
	if err := e.DB.Create(&run).Error; err != nil {
		slog.Error("postaction: failed to create run record",
			"post_action_id", action.ID, "error", err)
		return
	}

	retryCount := action.RetryCount
	if retryCount > maxRetryCount {
		retryCount = maxRetryCount
	}
	if retryCount < 0 {
		retryCount = 0
	}
	maxAttempts := retryCount + 1
	var lastStatusCode int
	var lastRespBody string
	var lastErr error

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			backoff := backoffDuration(attempt)
			slog.Info("postaction: retrying",
				"run_id", runID, "attempt", attempt+1, "backoff", backoff)
			time.Sleep(backoff)
		}

		statusCode, respBody, err := e.doRequest(action, binding, pctx)
		lastStatusCode = statusCode
		lastRespBody = respBody
		lastErr = err

		if err == nil && statusCode < 500 {
			// Success (any non-5xx is considered successful from retry perspective).
			break
		}

		slog.Warn("postaction: attempt failed",
			"run_id", runID, "attempt", attempt+1,
			"status_code", statusCode, "error", err)
	}

	// Record final result.
	finished := time.Now()
	updates := map[string]interface{}{
		"completed_at":  finished,
		"status_code":   lastStatusCode,
		"response_body": truncate(lastRespBody, maxResponseBody),
	}

	if lastErr != nil {
		updates["status"] = models.PostActionRunStatusFailed
		updates["error"] = lastErr.Error()
	} else if lastStatusCode >= 400 {
		updates["status"] = models.PostActionRunStatusFailed
		updates["error"] = fmt.Sprintf("HTTP %d", lastStatusCode)
	} else {
		updates["status"] = models.PostActionRunStatusSuccess
	}

	if err := e.DB.Model(&models.PostActionRun{}).Where("id = ?", runID).Updates(updates).Error; err != nil {
		slog.Error("postaction: failed to update run record",
			"run_id", runID, "error", err)
	}
}

// doRequest builds and executes an HTTP request from the action config.
func (e *Executor) doRequest(action models.PostAction, binding models.PostActionBinding, pctx PostActionContext) (int, string, error) {
	url := renderTemplate(action.URL, pctx)

	// Determine body: use body_override from binding if set, otherwise body_template.
	bodyTpl := action.BodyTemplate
	if binding.BodyOverride != "" {
		bodyTpl = binding.BodyOverride
	}
	body := renderTemplate(bodyTpl, pctx)

	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}

	timeoutSec := action.TimeoutSeconds
	if timeoutSec <= 0 || timeoutSec > maxTimeoutSeconds {
		timeoutSec = 30
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, action.Method, url, bodyReader)
	if err != nil {
		return 0, "", fmt.Errorf("building request: %w", err)
	}

	// Apply headers from action config.
	if len(action.Headers) > 0 && string(action.Headers) != "null" {
		var headers map[string]string
		if err := json.Unmarshal(action.Headers, &headers); err == nil {
			for k, v := range headers {
				req.Header.Set(k, renderTemplate(v, pctx))
			}
		}
	}

	// Default Content-Type for requests with a body.
	if body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	// Apply authentication.
	if err := applyAuth(req, action); err != nil {
		return 0, "", fmt.Errorf("applying auth: %w", err)
	}

	resp, err := e.Client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body, limited to maxResponseBody.
	respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	respBody := string(respBytes)

	return resp.StatusCode, respBody, nil
}

// applyAuth adds authentication to the request based on the action's auth_type.
func applyAuth(req *http.Request, action models.PostAction) error {
	if action.AuthType == "" || action.AuthType == models.PostActionAuthNone {
		return nil
	}

	var config map[string]string
	if len(action.AuthConfig) > 0 && string(action.AuthConfig) != "null" {
		if err := json.Unmarshal(action.AuthConfig, &config); err != nil {
			return fmt.Errorf("parsing auth_config: %w", err)
		}
	}
	if config == nil {
		return fmt.Errorf("auth_type is %q but auth_config is empty", action.AuthType)
	}

	// Decrypt values that may be encrypted.
	for k, v := range config {
		decrypted, err := crypto.Decrypt(v)
		if err != nil {
			return fmt.Errorf("decrypting auth_config key %q: %w", k, err)
		}
		config[k] = decrypted
	}

	switch action.AuthType {
	case models.PostActionAuthBearer:
		token := config["token"]
		if token == "" {
			return fmt.Errorf("bearer auth requires 'token' in auth_config")
		}
		req.Header.Set("Authorization", "Bearer "+token)

	case models.PostActionAuthBasic:
		username := config["username"]
		password := config["password"]
		if username == "" {
			return fmt.Errorf("basic auth requires 'username' in auth_config")
		}
		req.SetBasicAuth(username, password)

	case models.PostActionAuthHeader:
		headerName := config["header_name"]
		headerValue := config["header_value"]
		if headerName == "" || headerValue == "" {
			return fmt.Errorf("header auth requires 'header_name' and 'header_value' in auth_config")
		}
		req.Header.Set(headerName, headerValue)

	default:
		return fmt.Errorf("unsupported auth_type: %q", action.AuthType)
	}

	return nil
}

// renderTemplate replaces {{variable}} placeholders with values from the context.
// Values are JSON-escaped when the template appears to be JSON.
func renderTemplate(tpl string, pctx PostActionContext) string {
	if tpl == "" {
		return ""
	}

	isJSON := isJSONTemplate(tpl)

	replacements := map[string]string{
		"{{status}}":       pctx.Status,
		"{{response}}":     pctx.Response,
		"{{error}}":        pctx.Error,
		"{{trigger_name}}": pctx.TriggerName,
		"{{trigger_type}}": pctx.SourceType,
		"{{team_name}}":    pctx.TeamName,
		"{{run_id}}":       pctx.RunID,
		"{{started_at}}":   pctx.StartedAt,
		"{{finished_at}}":  pctx.FinishedAt,
		"{{prompt}}":       pctx.Prompt,
	}

	result := tpl
	for placeholder, value := range replacements {
		if !strings.Contains(result, placeholder) {
			continue
		}
		replacement := value
		if isJSON {
			replacement = jsonEscape(value)
		}
		result = strings.ReplaceAll(result, placeholder, replacement)
	}
	return result
}

// isJSONTemplate checks if a template string looks like JSON.
func isJSONTemplate(tpl string) bool {
	trimmed := strings.TrimSpace(tpl)
	return (strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")) ||
		(strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]"))
}

// jsonEscape escapes a string for safe embedding inside a JSON string value.
// It marshals the string and strips the surrounding quotes.
func jsonEscape(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return s
	}
	// json.Marshal wraps in quotes: "value" → strip them.
	return string(b[1 : len(b)-1])
}

// backoffDuration calculates exponential backoff: 1s, 2s, 4s, ..., capped at maxBackoff.
func backoffDuration(attempt int) time.Duration {
	d := time.Duration(math.Pow(2, float64(attempt-1))) * time.Second
	if d > maxBackoff {
		return maxBackoff
	}
	return d
}

// truncate returns at most maxLen bytes of s.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
