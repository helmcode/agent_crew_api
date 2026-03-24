package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"time"

	"github.com/gofiber/contrib/websocket"

	"github.com/helmcode/agent-crew/internal/models"
)

// StreamLogs streams container logs for a team's agents via WebSocket.
func (s *Server) StreamLogs(c *websocket.Conn) {
	teamID := c.Params("id")
	orgID, _ := c.Locals("org_id").(string)
	defer c.Close()

	var team models.Team
	if err := s.db.Where("org_id = ?", orgID).Preload("Agents").First(&team, "id = ?", teamID).Error; err != nil {
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"error":"team not found"}`))
		return
	}

	// Find a running agent to stream logs from (prefer leader).
	var containerID string
	for _, agent := range team.Agents {
		if agent.ContainerStatus == models.ContainerStatusRunning {
			containerID = agent.ContainerID
			if agent.Role == models.AgentRoleLeader {
				break
			}
		}
	}

	if containerID == "" {
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"error":"no running agents"}`))
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reader, err := s.runtime.StreamLogs(ctx, containerID)
	if err != nil {
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"error":"failed to stream logs"}`))
		return
	}
	defer reader.Close()

	// Read from Docker logs and write to WebSocket.
	buf := make([]byte, 4096)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			if writeErr := c.WriteMessage(websocket.TextMessage, buf[:n]); writeErr != nil {
				return
			}
		}
		if err != nil {
			if err != io.EOF {
				slog.Error("log stream error", "error", err)
			}
			return
		}
	}
}

// StreamActivity streams team activity updates via WebSocket.
func (s *Server) StreamActivity(c *websocket.Conn) {
	teamID := c.Params("id")
	orgID, _ := c.Locals("org_id").(string)
	defer c.Close()

	var team models.Team
	if err := s.db.Where("org_id = ?", orgID).First(&team, "id = ?", teamID).Error; err != nil {
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"error":"team not found"}`))
		return
	}

	// Poll for new task logs and send them as activity updates.
	// Use created_at for cursor pagination (UUIDs are not sortable).
	//
	// Initialize lastCreatedAt from the most recent existing message so that
	// on (re)connection the WebSocket only streams NEW messages. The frontend
	// loads the full history via the REST messagesApi.list() call; sending it
	// again over the WebSocket on every reconnect causes the "gradual history
	// reload" jitter visible in the chat panel.
	var lastCreatedAt time.Time
	var seedMsg models.TaskLog
	if err := s.db.Where("team_id = ?", teamID).Order("created_at DESC").First(&seedMsg).Error; err == nil {
		lastCreatedAt = seedMsg.CreatedAt
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	// Send periodic pings to keep the connection alive through proxies
	// and NAT gateways during long inference times (e.g. Ollama on CPU).
	// WriteControl is safe to call concurrently with WriteMessage.
	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	// Also listen for close messages from client.
	done := make(chan struct{})
	go func() {
		for {
			_, _, err := c.ReadMessage()
			if err != nil {
				close(done)
				return
			}
		}
	}()

	for {
		select {
		case <-done:
			return
		case <-pingTicker.C:
			if err := c.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				return
			}
		case <-ticker.C:
			var logs []models.TaskLog
			// Use ">" so the cursor advances past already-sent records.
			// The seed query above initialises lastCreatedAt to the newest
			// existing message, so only truly new records are streamed.
			query := s.db.Where("team_id = ?", teamID).Order("created_at ASC").Limit(100)
			if !lastCreatedAt.IsZero() {
				query = query.Where("created_at > ?", lastCreatedAt)
			}
			query.Find(&logs)

			for _, log := range logs {
				data, _ := json.Marshal(log)
				if err := c.WriteMessage(websocket.TextMessage, data); err != nil {
					return
				}
				lastCreatedAt = log.CreatedAt
			}
		}
	}
}
