package api

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/gofiber/contrib/websocket"

	"github.com/helmcode/agent-crew/internal/models"
)

// StreamLogs streams container logs for a team's agents via WebSocket.
func (s *Server) StreamLogs(c *websocket.Conn) {
	teamID := c.Params("id")
	defer c.Close()

	var team models.Team
	if err := s.db.Preload("Agents").First(&team, "id = ?", teamID).Error; err != nil {
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
	defer c.Close()

	var team models.Team
	if err := s.db.First(&team, "id = ?", teamID).Error; err != nil {
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"error":"team not found"}`))
		return
	}

	// Poll for new task logs and send them as activity updates.
	// Use created_at for cursor pagination (UUIDs are not sortable).
	var lastCreatedAt time.Time
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

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
		case <-ticker.C:
			var logs []models.TaskLog
			query := s.db.Where("team_id = ?", teamID).Order("created_at ASC").Limit(20)
			if !lastCreatedAt.IsZero() {
				query = query.Where("created_at > ?", lastCreatedAt)
			}
			query.Find(&logs)

			for _, log := range logs {
				data, _ := log.Payload.MarshalJSON()
				if err := c.WriteMessage(websocket.TextMessage, data); err != nil {
					return
				}
				lastCreatedAt = log.CreatedAt
			}
		}
	}
}
