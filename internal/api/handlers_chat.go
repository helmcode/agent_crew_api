package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/helmcode/agent-crew/internal/models"
	"github.com/helmcode/agent-crew/internal/protocol"
)

const (
	// maxFileSize is the maximum allowed size per uploaded file (10 MB).
	maxFileSize = 10 * 1024 * 1024
	// maxFileCount is the maximum number of files per chat message.
	maxFileCount = 5
)

// unsafeFilenameChars matches characters that are not safe in filenames.
var unsafeFilenameChars = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

// allowedMIMEPrefixes lists the MIME type prefixes accepted for file uploads.
var allowedMIMEPrefixes = []string{"text/", "image/", "application/pdf"}

// SendChat sends a user message to the team leader via NATS.
// It supports both JSON (backward compat) and multipart/form-data with file uploads.
func (s *Server) SendChat(c *fiber.Ctx) error {
	teamID := c.Params("id")

	var team models.Team
	if err := s.db.Scopes(OrgScope(c)).First(&team, "id = ?", teamID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "team not found")
	}

	if team.Status != models.TeamStatusRunning {
		return fiber.NewError(fiber.StatusConflict, "team is not running")
	}

	var message string
	var fileRefs []protocol.FileRef

	contentType := string(c.Request().Header.ContentType())
	mediaType, _, _ := mime.ParseMediaType(contentType)

	if mediaType == "multipart/form-data" {
		// Parse multipart form.
		message = c.FormValue("message")
		if message == "" {
			return fiber.NewError(fiber.StatusBadRequest, "message is required")
		}

		// Parse uploaded files.
		form, err := c.MultipartForm()
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "failed to parse multipart form")
		}

		files := form.File["files"]
		if len(files) > maxFileCount {
			return fiber.NewError(fiber.StatusBadRequest, fmt.Sprintf("maximum %d files allowed", maxFileCount))
		}

		if len(files) > 0 {
			// Find the leader container to write files into.
			var leader models.Agent
			if err := s.db.Where("team_id = ? AND role = ? AND container_status = ?",
				teamID, models.AgentRoleLeader, models.ContainerStatusRunning).First(&leader).Error; err != nil {
				return fiber.NewError(fiber.StatusConflict, "no running leader agent found for file upload")
			}

			timestamp := time.Now().Unix()

			for _, fh := range files {
				// Validate file size.
				if fh.Size > maxFileSize {
					return fiber.NewError(fiber.StatusBadRequest,
						fmt.Sprintf("file %q exceeds maximum size of %d bytes", fh.Filename, maxFileSize))
				}

				// Validate MIME type.
				if !isAllowedMIME(fh.Header.Get("Content-Type")) {
					return fiber.NewError(fiber.StatusBadRequest,
						fmt.Sprintf("file %q has unsupported type %q; allowed: text/*, image/*, application/pdf",
							fh.Filename, fh.Header.Get("Content-Type")))
				}

				// Sanitize filename.
				safeName := sanitizeFilename(fh.Filename)
				containerPath := fmt.Sprintf("/workspace/uploads/%d_%s", timestamp, safeName)

				// Read file content.
				f, err := fh.Open()
				if err != nil {
					return fiber.NewError(fiber.StatusInternalServerError, "failed to read uploaded file")
				}
				data, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
				f.Close()
				if err != nil {
					return fiber.NewError(fiber.StatusInternalServerError, "failed to read uploaded file")
				}
				if int64(len(data)) > maxFileSize {
					return fiber.NewError(fiber.StatusBadRequest,
						fmt.Sprintf("file %q exceeds maximum size of %d bytes", fh.Filename, maxFileSize))
				}

				// Write file to leader container using CopyToContainer (tar archive)
				// to avoid shell ARG_MAX limits with large files.
				if err := s.runtime.CopyToContainer(c.Context(), leader.ContainerID, containerPath, data); err != nil {
					slog.Error("failed to write uploaded file to container",
						"file", safeName, "error", err)
					return fiber.NewError(fiber.StatusInternalServerError,
						fmt.Sprintf("failed to write file %q to container", fh.Filename))
				}

				// Fix ownership: CopyToContainer creates files as root, but the
				// agent process runs as the workspace owner (non-root via gosu).
				// Detect the workspace owner UID:GID and chown the uploads dir
				// and file to match so the agent can read/edit/delete them.
				fixPermsCmd := []string{"sh", "-c", fmt.Sprintf(
					"owner=$(stat -c '%%u:%%g' /workspace) && chown \"$owner\" /workspace/uploads && chown \"$owner\" '%s'",
					containerPath,
				)}
				if _, err := s.runtime.ExecInContainer(c.Context(), leader.ContainerID, fixPermsCmd); err != nil {
					slog.Warn("failed to fix uploaded file permissions",
						"file", safeName, "error", err)
				}

				fileRefs = append(fileRefs, protocol.FileRef{
					Name: fh.Filename,
					Path: containerPath,
					Size: fh.Size,
					Type: fh.Header.Get("Content-Type"),
				})
			}
		}
	} else {
		// Fallback: JSON body (backward compatible).
		var req ChatRequest
		if err := c.BodyParser(&req); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
		}
		if req.Message == "" {
			return fiber.NewError(fiber.StatusBadRequest, "message is required")
		}
		message = req.Message
	}

	// Log to task log for persistence and Activity panel.
	logPayload := map[string]interface{}{"content": message}
	if len(fileRefs) > 0 {
		logPayload["files"] = fileRefs
	}
	content, _ := json.Marshal(logPayload)
	taskLog := models.TaskLog{
		ID:          uuid.New().String(),
		TeamID:      teamID,
		FromAgent:   "user",
		ToAgent:     "leader",
		MessageType: "user_message",
		Payload:     models.JSON(content),
	}
	s.db.Create(&taskLog)

	// Publish to NATS leader channel so the agent actually receives the message.
	sanitizedName := SanitizeName(team.Name)
	payload := protocol.UserMessagePayload{
		Content: message,
		Files:   fileRefs,
	}
	if err := s.publishToTeamNATS(sanitizedName, payload); err != nil {
		slog.Error("failed to publish chat to NATS", "team", team.Name, "error", err)
		return c.JSON(fiber.Map{
			"status":  "queued",
			"message": "Message logged but NATS delivery failed: " + err.Error(),
		})
	}

	response := fiber.Map{
		"status":  "sent",
		"message": "Message sent to team leader",
	}
	if len(fileRefs) > 0 {
		response["files"] = fileRefs
	}
	return c.JSON(response)
}

// publishToTeamNATS connects to the team's NATS, publishes a user_message to
// the leader channel, and disconnects. The connection is short-lived on purpose
// to avoid managing per-team NATS connections in the API server.
// It retries up to 3 times to handle cases where the NATS container was just
// recreated (e.g. after port binding fix).
func (s *Server) publishToTeamNATS(teamName string, payload protocol.UserMessagePayload) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	natsURL, err := s.runtime.GetNATSConnectURL(ctx, teamName)
	if err != nil {
		return fmt.Errorf("resolving NATS URL: %w", err)
	}

	// Build NATS connection options.
	token := os.Getenv("NATS_AUTH_TOKEN")
	opts := []nats.Option{
		nats.Name("agentcrew-api"),
		nats.Timeout(5 * time.Second),
	}
	if token != "" {
		opts = append(opts, nats.Token(token))
	}

	slog.Info("connecting to team NATS",
		"team", teamName,
		"url", natsURL,
		"auth", token != "",
	)

	// Retry connection up to 3 times (NATS may have just been recreated).
	var nc *nats.Conn
	for attempt := 1; attempt <= 3; attempt++ {
		nc, err = nats.Connect(natsURL, opts...)
		if err == nil {
			break
		}
		slog.Warn("NATS connect attempt failed",
			"team", teamName,
			"url", natsURL,
			"attempt", attempt,
			"error", err,
		)
		if attempt < 3 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("context cancelled waiting for NATS: %w", ctx.Err())
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
	}
	if err != nil {
		return fmt.Errorf("connecting to NATS at %s (auth=%t): %w", natsURL, token != "", err)
	}
	defer nc.Close()

	// Build the protocol message.
	msg, err := protocol.NewMessage("user", "leader", protocol.TypeUserMessage, payload)
	if err != nil {
		return fmt.Errorf("building protocol message: %w", err)
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshaling message: %w", err)
	}

	// Publish to the leader channel.
	subject, err := protocol.TeamLeaderChannel(teamName)
	if err != nil {
		return fmt.Errorf("building leader channel: %w", err)
	}

	if err := nc.Publish(subject, data); err != nil {
		return fmt.Errorf("publishing to %s: %w", subject, err)
	}

	if err := nc.Flush(); err != nil {
		return fmt.Errorf("flushing NATS: %w", err)
	}

	slog.Info("chat message published to NATS", "team", teamName, "subject", subject)
	return nil
}

// chatMessageTypes are the message types that represent actual conversation
// content (user input and agent responses). Status updates, task assignments,
// and other operational messages are excluded from the chat history endpoint
// to prevent them from pushing conversation messages out of the result window.
var chatMessageTypes = []string{
	string(protocol.TypeUserMessage),
	string(protocol.TypeLeaderResponse),
	"task_result", // backward compat: records stored before relay fix
}

// GetMessages returns chat messages for a team, filtered to conversation-relevant
// types by default. Use the "types" query parameter to override (comma-separated).
// Supports cursor-based pagination via the "before" query parameter (RFC3339 timestamp).
func (s *Server) GetMessages(c *fiber.Ctx) error {
	teamID := c.Params("id")

	var team models.Team
	if err := s.db.Scopes(OrgScope(c)).First(&team, "id = ?", teamID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "team not found")
	}

	limit := c.QueryInt("limit", 100)
	if limit > 500 {
		limit = 500
	}

	query := s.db.Where("team_id = ?", teamID)

	// Filter by message type. Default to chat-relevant types only.
	if typesParam := c.Query("types"); typesParam != "" {
		types := splitCSV(typesParam)
		query = query.Where("message_type IN ?", types)
	} else {
		query = query.Where("message_type IN ?", chatMessageTypes)
	}

	// Cursor-based pagination: load messages older than the given timestamp.
	if before := c.Query("before"); before != "" {
		t, err := time.Parse(time.RFC3339Nano, before)
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid 'before' timestamp, use RFC3339 format")
		}
		query = query.Where("created_at < ?", t)
	}

	var logs []models.TaskLog
	if err := query.Order("created_at DESC").
		Limit(limit).
		Find(&logs).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to list messages")
	}

	return c.JSON(logs)
}

// GetActivity returns all task log entries for a team (including status updates,
// task assignments, etc.). This is the unfiltered counterpart to GetMessages,
// intended for the Activity panel.
func (s *Server) GetActivity(c *fiber.Ctx) error {
	teamID := c.Params("id")

	var team models.Team
	if err := s.db.Scopes(OrgScope(c)).First(&team, "id = ?", teamID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "team not found")
	}

	limit := c.QueryInt("limit", 50)
	if limit > 200 {
		limit = 200
	}

	query := s.db.Where("team_id = ?", teamID)

	if before := c.Query("before"); before != "" {
		t, err := time.Parse(time.RFC3339Nano, before)
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid 'before' timestamp, use RFC3339 format")
		}
		query = query.Where("created_at < ?", t)
	}

	var logs []models.TaskLog
	if err := query.Order("created_at DESC").
		Limit(limit).
		Find(&logs).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to list activity")
	}

	return c.JSON(logs)
}

// splitCSV splits a comma-separated string into trimmed, non-empty parts.
func splitCSV(s string) []string {
	var result []string
	for _, part := range strings.Split(s, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// sanitizeFilename strips path separators, null bytes, and special characters
// from a filename to prevent path traversal and injection attacks.
func sanitizeFilename(name string) string {
	// Extract base name to strip any directory components.
	name = filepath.Base(name)

	// Remove null bytes.
	name = strings.ReplaceAll(name, "\x00", "")

	// Replace unsafe characters with underscores.
	name = unsafeFilenameChars.ReplaceAllString(name, "_")

	// Collapse consecutive underscores.
	for strings.Contains(name, "__") {
		name = strings.ReplaceAll(name, "__", "_")
	}

	name = strings.Trim(name, "_.")

	if name == "" || name == "." || name == ".." {
		name = "upload"
	}

	// Truncate to 255 characters.
	if len(name) > 255 {
		name = name[:255]
	}

	return name
}

// isAllowedMIME checks whether a MIME type is in the allowed list.
func isAllowedMIME(mimeType string) bool {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	for _, prefix := range allowedMIMEPrefixes {
		if strings.HasPrefix(mimeType, prefix) {
			return true
		}
	}
	return false
}
