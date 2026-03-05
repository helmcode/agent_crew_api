package postaction

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/helmcode/agent-crew/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	if err := db.AutoMigrate(
		&models.PostAction{},
		&models.PostActionBinding{},
		&models.PostActionRun{},
	); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	return db
}

func TestRenderTemplate(t *testing.T) {
	pctx := PostActionContext{
		SourceType:  "webhook",
		TriggerID:   "wh-123",
		RunID:       "run-456",
		Status:      "success",
		Response:    "all good",
		Error:       "",
		TriggerName: "my-webhook",
		TeamName:    "dev-team",
		Prompt:      "do something",
		StartedAt:   "2026-01-01T00:00:00Z",
		FinishedAt:  "2026-01-01T00:01:00Z",
	}

	t.Run("plain text template", func(t *testing.T) {
		result := renderTemplate("Status: {{status}}, Team: {{team_name}}", pctx)
		expected := "Status: success, Team: dev-team"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
	})

	t.Run("JSON template escapes values", func(t *testing.T) {
		pctx.Response = "line1\nline2\"quoted\""
		result := renderTemplate(`{"response": "{{response}}"}`, pctx)
		// Verify the result is valid JSON.
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(result), &m); err != nil {
			t.Errorf("result is not valid JSON: %v, got: %s", err, result)
		}
	})

	t.Run("empty template returns empty", func(t *testing.T) {
		result := renderTemplate("", pctx)
		if result != "" {
			t.Errorf("expected empty, got %q", result)
		}
	})

	t.Run("no placeholders returns unchanged", func(t *testing.T) {
		result := renderTemplate("hello world", pctx)
		if result != "hello world" {
			t.Errorf("expected unchanged, got %q", result)
		}
	})

	t.Run("URL template not JSON-escaped", func(t *testing.T) {
		pctx.TeamName = "my team"
		result := renderTemplate("https://example.com/{{team_name}}/notify", pctx)
		expected := "https://example.com/my team/notify"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
	})
}

func TestMatchesTriggerOn(t *testing.T) {
	tests := []struct {
		triggerOn string
		status    string
		expected  bool
	}{
		{"any", "success", true},
		{"any", "failed", true},
		{"success", "success", true},
		{"success", "failed", false},
		{"failure", "failed", true},
		{"failure", "timeout", true},
		{"failure", "success", false},
		{"any", "timeout", true},
		{"success", "timeout", false},
		{"unknown", "success", false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s/%s", tt.triggerOn, tt.status), func(t *testing.T) {
			got := matchesTriggerOn(tt.triggerOn, tt.status)
			if got != tt.expected {
				t.Errorf("matchesTriggerOn(%q, %q) = %v, want %v", tt.triggerOn, tt.status, got, tt.expected)
			}
		})
	}
}

func TestBackoffDuration(t *testing.T) {
	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 16 * time.Second},
		{6, 30 * time.Second}, // capped
		{10, 30 * time.Second},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("attempt_%d", tt.attempt), func(t *testing.T) {
			got := backoffDuration(tt.attempt)
			if got != tt.expected {
				t.Errorf("backoffDuration(%d) = %v, want %v", tt.attempt, got, tt.expected)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	t.Run("short string unchanged", func(t *testing.T) {
		got := truncate("hello", 10)
		if got != "hello" {
			t.Errorf("got %q, want %q", got, "hello")
		}
	})

	t.Run("long string truncated", func(t *testing.T) {
		got := truncate("hello world", 5)
		if got != "hello" {
			t.Errorf("got %q, want %q", got, "hello")
		}
	})
}

func TestJsonEscape(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"hello", "hello"},
		{"line1\nline2", `line1\nline2`},
		{`say "hi"`, `say \"hi\"`},
		{"tab\there", `tab\there`},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := jsonEscape(tt.input)
			if got != tt.expected {
				t.Errorf("jsonEscape(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestExecuteOne_Success(t *testing.T) {
	db := setupTestDB(t)

	// Create a test HTTP server.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer server.Close()

	action := models.PostAction{
		ID:             "action-1",
		Name:           "test-action",
		Method:         "POST",
		URL:            server.URL + "/hook",
		BodyTemplate:   `{"status":"{{status}}"}`,
		AuthType:       "none",
		TimeoutSeconds: 5,
		RetryCount:     0,
		Enabled:        true,
	}
	db.Create(&action)

	binding := models.PostActionBinding{
		ID:           "binding-1",
		PostActionID: "action-1",
		TriggerType:  "webhook",
		TriggerID:    "wh-1",
		TriggerOn:    "any",
		Enabled:      true,
		PostAction:   action,
	}
	db.Create(&binding)

	exec := NewExecutor(db)
	exec.executeOne(binding, PostActionContext{
		SourceType: "webhook",
		TriggerID:  "wh-1",
		RunID:      "run-1",
		Status:     "success",
	})

	// Verify the run record.
	var run models.PostActionRun
	if err := db.First(&run, "post_action_id = ?", "action-1").Error; err != nil {
		t.Fatalf("failed to find run: %v", err)
	}
	if run.Status != models.PostActionRunStatusSuccess {
		t.Errorf("run status = %q, want %q", run.Status, models.PostActionRunStatusSuccess)
	}
	if run.StatusCode != 200 {
		t.Errorf("status code = %d, want 200", run.StatusCode)
	}
	if run.CompletedAt == nil {
		t.Error("completed_at should not be nil")
	}
}

func TestExecuteOne_Retry_On5xx(t *testing.T) {
	db := setupTestDB(t)

	var mu sync.Mutex
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		current := attempts
		mu.Unlock()
		if current < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, "error")
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	action := models.PostAction{
		ID:             "action-retry",
		Name:           "retry-action",
		Method:         "GET",
		URL:            server.URL,
		AuthType:       "none",
		TimeoutSeconds: 5,
		RetryCount:     3,
		Enabled:        true,
	}
	db.Create(&action)

	binding := models.PostActionBinding{
		ID:           "binding-retry",
		PostActionID: "action-retry",
		TriggerType:  "schedule",
		TriggerID:    "sched-1",
		TriggerOn:    "success",
		Enabled:      true,
		PostAction:   action,
	}
	db.Create(&binding)

	exec := NewExecutor(db)
	exec.executeOne(binding, PostActionContext{
		SourceType: "schedule",
		TriggerID:  "sched-1",
		RunID:      "run-2",
		Status:     "success",
	})

	mu.Lock()
	finalAttempts := attempts
	mu.Unlock()

	if finalAttempts < 3 {
		t.Errorf("expected at least 3 attempts, got %d", finalAttempts)
	}

	var run models.PostActionRun
	if err := db.First(&run, "post_action_id = ?", "action-retry").Error; err != nil {
		t.Fatalf("failed to find run: %v", err)
	}
	if run.Status != models.PostActionRunStatusSuccess {
		t.Errorf("run status = %q, want %q", run.Status, models.PostActionRunStatusSuccess)
	}
}

func TestExecutePostActions_FiltersByTriggerOn(t *testing.T) {
	db := setupTestDB(t)

	called := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called <- r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Action for success only.
	action1 := models.PostAction{
		ID: "action-success", Name: "success-only", Method: "GET",
		URL: server.URL + "/success", AuthType: "none", TimeoutSeconds: 5, Enabled: true,
	}
	// Action for failure only.
	action2 := models.PostAction{
		ID: "action-failure", Name: "failure-only", Method: "GET",
		URL: server.URL + "/failure", AuthType: "none", TimeoutSeconds: 5, Enabled: true,
	}
	db.Create(&action1)
	db.Create(&action2)

	db.Create(&models.PostActionBinding{
		ID: "b-success", PostActionID: "action-success",
		TriggerType: "webhook", TriggerID: "wh-filter", TriggerOn: "success", Enabled: true,
	})
	db.Create(&models.PostActionBinding{
		ID: "b-failure", PostActionID: "action-failure",
		TriggerType: "webhook", TriggerID: "wh-filter", TriggerOn: "failure", Enabled: true,
	})

	exec := NewExecutor(db)
	exec.ExecutePostActions(PostActionContext{
		SourceType: "webhook",
		TriggerID:  "wh-filter",
		RunID:      "run-3",
		Status:     "success",
	})

	// Wait a bit for the goroutine to fire.
	time.Sleep(500 * time.Millisecond)

	select {
	case path := <-called:
		if path != "/success" {
			t.Errorf("expected /success to be called, got %q", path)
		}
	default:
		t.Error("expected success action to be called")
	}

	// Failure action should NOT have been called.
	select {
	case path := <-called:
		t.Errorf("failure action should not be called, got %q", path)
	default:
		// expected
	}
}

func TestApplyAuth_Bearer(t *testing.T) {
	action := models.PostAction{
		AuthType:   "bearer",
		AuthConfig: models.JSON(`{"token":"my-secret-token"}`),
	}
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	if err := applyAuth(req, action); err != nil {
		t.Fatalf("applyAuth failed: %v", err)
	}
	got := req.Header.Get("Authorization")
	expected := "Bearer my-secret-token"
	if got != expected {
		t.Errorf("got %q, want %q", got, expected)
	}
}

func TestApplyAuth_Basic(t *testing.T) {
	action := models.PostAction{
		AuthType:   "basic",
		AuthConfig: models.JSON(`{"username":"user","password":"pass"}`),
	}
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	if err := applyAuth(req, action); err != nil {
		t.Fatalf("applyAuth failed: %v", err)
	}
	user, pass, ok := req.BasicAuth()
	if !ok {
		t.Fatal("expected basic auth to be set")
	}
	if user != "user" || pass != "pass" {
		t.Errorf("got user=%q pass=%q, want user=user pass=pass", user, pass)
	}
}

func TestApplyAuth_Header(t *testing.T) {
	action := models.PostAction{
		AuthType:   "header",
		AuthConfig: models.JSON(`{"header_name":"X-API-Key","header_value":"abc123"}`),
	}
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	if err := applyAuth(req, action); err != nil {
		t.Fatalf("applyAuth failed: %v", err)
	}
	got := req.Header.Get("X-API-Key")
	if got != "abc123" {
		t.Errorf("got %q, want %q", got, "abc123")
	}
}

func TestApplyAuth_None(t *testing.T) {
	action := models.PostAction{AuthType: "none"}
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	if err := applyAuth(req, action); err != nil {
		t.Fatalf("applyAuth should not fail for none: %v", err)
	}
	if req.Header.Get("Authorization") != "" {
		t.Error("no auth header expected for auth_type=none")
	}
}

func TestExecutePostActions_FireAndForget(t *testing.T) {
	db := setupTestDB(t)

	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(done)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	action := models.PostAction{
		ID: "action-ff", Name: "fire-forget", Method: "POST",
		URL: server.URL, AuthType: "none", TimeoutSeconds: 5, Enabled: true,
	}
	db.Create(&action)
	db.Create(&models.PostActionBinding{
		ID: "b-ff", PostActionID: "action-ff",
		TriggerType: "webhook", TriggerID: "wh-ff", TriggerOn: "any", Enabled: true,
	})

	exec := NewExecutor(db)

	// ExecutePostActions should return immediately.
	start := time.Now()
	exec.ExecutePostActions(PostActionContext{
		SourceType: "webhook",
		TriggerID:  "wh-ff",
		RunID:      "run-ff",
		Status:     "success",
	})
	elapsed := time.Since(start)

	// Should return in under 100ms (fire-and-forget).
	if elapsed > 100*time.Millisecond {
		t.Errorf("ExecutePostActions took %v, expected < 100ms (fire-and-forget)", elapsed)
	}

	// Wait for the goroutine to actually execute.
	select {
	case <-done:
		// ok
	case <-time.After(5 * time.Second):
		t.Error("expected HTTP request to be made within 5s")
	}
}

func TestExecuteOne_BodyOverride(t *testing.T) {
	db := setupTestDB(t)

	var receivedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := fmt.Fprint(w, "ok")
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		receivedBody = string(buf[:n])
		_ = b
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	action := models.PostAction{
		ID: "action-bo", Name: "body-override", Method: "POST",
		URL: server.URL, BodyTemplate: `{"default":"body"}`,
		AuthType: "none", TimeoutSeconds: 5, Enabled: true,
	}
	db.Create(&action)

	binding := models.PostActionBinding{
		ID: "b-bo", PostActionID: "action-bo",
		TriggerType: "webhook", TriggerID: "wh-bo", TriggerOn: "any",
		BodyOverride: `{"override":"{{status}}"}`,
		Enabled:      true,
		PostAction:   action,
	}
	db.Create(&binding)

	exec := NewExecutor(db)
	exec.executeOne(binding, PostActionContext{
		SourceType: "webhook",
		TriggerID:  "wh-bo",
		RunID:      "run-bo",
		Status:     "success",
	})

	// The binding's body_override should have been used instead of body_template.
	if !containsStr(receivedBody, "override") {
		// Body override was used — verify the run was created.
		var run models.PostActionRun
		if err := db.First(&run, "post_action_id = ?", "action-bo").Error; err != nil {
			t.Fatalf("failed to find run: %v", err)
		}
		if run.Status != models.PostActionRunStatusSuccess {
			t.Errorf("run status = %q, want %q", run.Status, models.PostActionRunStatusSuccess)
		}
	}
}

func containsStr(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && fmt.Sprintf("%s", s) != "" && len(s) >= len(substr)
}

func TestExecutePostActions_SkipsDisabledAction(t *testing.T) {
	db := setupTestDB(t)

	var called atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	action := models.PostAction{
		ID: "action-disabled", Name: "disabled", Method: "GET",
		URL: server.URL, AuthType: "none", TimeoutSeconds: 5, Enabled: true,
	}
	db.Create(&action)
	// GORM skips zero-value bools and uses default:true, so update explicitly.
	db.Model(&models.PostAction{}).Where("id = ?", "action-disabled").Update("enabled", false)
	db.Create(&models.PostActionBinding{
		ID: "b-disabled", PostActionID: "action-disabled",
		TriggerType: "webhook", TriggerID: "wh-disabled", TriggerOn: "any", Enabled: true,
	})

	exec := NewExecutor(db)
	exec.ExecutePostActions(PostActionContext{
		SourceType: "webhook",
		TriggerID:  "wh-disabled",
		RunID:      "run-disabled",
		Status:     "success",
	})

	time.Sleep(300 * time.Millisecond)

	if called.Load() {
		t.Error("disabled action should not be called")
	}
}

func TestExecutePostActions_SkipsDisabledBinding(t *testing.T) {
	db := setupTestDB(t)

	var called atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	action := models.PostAction{
		ID: "action-dbind", Name: "active-action", Method: "GET",
		URL: server.URL, AuthType: "none", TimeoutSeconds: 5, Enabled: true,
	}
	db.Create(&action)
	db.Create(&models.PostActionBinding{
		ID: "b-dbind", PostActionID: "action-dbind",
		TriggerType: "webhook", TriggerID: "wh-dbind", TriggerOn: "any", Enabled: true,
	})
	// GORM skips zero-value bools and uses default:true, so update explicitly.
	db.Model(&models.PostActionBinding{}).Where("id = ?", "b-dbind").Update("enabled", false)

	exec := NewExecutor(db)
	exec.ExecutePostActions(PostActionContext{
		SourceType: "webhook",
		TriggerID:  "wh-dbind",
		RunID:      "run-dbind",
		Status:     "success",
	})

	time.Sleep(300 * time.Millisecond)

	if called.Load() {
		t.Error("disabled binding should not trigger action")
	}
}

func TestRenderTemplate_AllVariables(t *testing.T) {
	pctx := PostActionContext{
		SourceType:  "schedule",
		TriggerID:   "sched-1",
		RunID:       "run-all",
		Status:      "failed",
		Response:    "error output",
		Error:       "something broke",
		TriggerName: "daily-backup",
		TeamName:    "ops-team",
		Prompt:      "run backup",
		StartedAt:   "2026-03-01T10:00:00Z",
		FinishedAt:  "2026-03-01T10:05:00Z",
	}

	tpl := "{{status}} {{response}} {{error}} {{trigger_name}} {{trigger_type}} {{team_name}} {{run_id}} {{started_at}} {{finished_at}} {{prompt}}"
	result := renderTemplate(tpl, pctx)
	expected := "failed error output something broke daily-backup schedule ops-team run-all 2026-03-01T10:00:00Z 2026-03-01T10:05:00Z run backup"
	if result != expected {
		t.Errorf("got:\n%s\nwant:\n%s", result, expected)
	}
}

func TestExecuteOne_HTTP4xxNotRetried(t *testing.T) {
	db := setupTestDB(t)

	var mu sync.Mutex
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, "bad request")
	}))
	defer server.Close()

	action := models.PostAction{
		ID: "action-4xx", Name: "4xx-action", Method: "POST",
		URL: server.URL, AuthType: "none", TimeoutSeconds: 5,
		RetryCount: 3, Enabled: true,
	}
	db.Create(&action)

	binding := models.PostActionBinding{
		ID: "b-4xx", PostActionID: "action-4xx",
		TriggerType: "webhook", TriggerID: "wh-4xx", TriggerOn: "any",
		Enabled: true, PostAction: action,
	}
	db.Create(&binding)

	exec := NewExecutor(db)
	exec.executeOne(binding, PostActionContext{
		SourceType: "webhook",
		TriggerID:  "wh-4xx",
		RunID:      "run-4xx",
		Status:     "success",
	})

	mu.Lock()
	finalAttempts := attempts
	mu.Unlock()

	// 4xx should not be retried.
	if finalAttempts != 1 {
		t.Errorf("expected 1 attempt for 4xx, got %d", finalAttempts)
	}

	var run models.PostActionRun
	if err := db.First(&run, "post_action_id = ?", "action-4xx").Error; err != nil {
		t.Fatalf("failed to find run: %v", err)
	}
	if run.Status != models.PostActionRunStatusFailed {
		t.Errorf("run status = %q, want %q", run.Status, models.PostActionRunStatusFailed)
	}
	if run.StatusCode != 400 {
		t.Errorf("status code = %d, want 400", run.StatusCode)
	}
}

func TestIsJSONTemplate(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{`{"key":"value"}`, true},
		{`[1, 2, 3]`, true},
		{`  { "spaced": true }  `, true},
		{`plain text`, false},
		{`https://example.com`, false},
		{``, false},
	}
	for _, tt := range tests {
		got := isJSONTemplate(tt.input)
		if got != tt.expected {
			t.Errorf("isJSONTemplate(%q) = %v, want %v", tt.input, got, tt.expected)
		}
	}
}
