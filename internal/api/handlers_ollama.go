package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/helmcode/agent-crew/internal/models"
	"github.com/helmcode/agent-crew/internal/runtime"
)

// OllamaStatusResponse is the response DTO for GET /api/ollama/status.
type OllamaStatusResponse struct {
	Running      bool     `json:"running"`
	ContainerID  string   `json:"container_id"`
	ModelsPulled []string `json:"models_pulled"`
	RefCount     int      `json:"ref_count"`
	GPUAvailable bool     `json:"gpu_available"`
}

// GetOllamaStatus returns the current status of the shared Ollama infrastructure.
func (s *Server) GetOllamaStatus(c *fiber.Ctx) error {
	resp := OllamaStatusResponse{
		ModelsPulled: []string{},
	}

	// Query SharedInfra record.
	var infra models.SharedInfra
	if err := s.db.Where("resource_type = ?", "ollama").First(&infra).Error; err != nil {
		// No record found — Ollama has never been started.
		return c.JSON(resp)
	}

	resp.ContainerID = infra.ContainerID
	resp.RefCount = infra.RefCount

	// Check if the container is actually running.
	if om, ok := s.runtime.(runtime.OllamaManager); ok {
		running, err := om.IsOllamaRunning(c.Context())
		if err != nil {
			slog.Error("failed to check ollama status", "error", err)
		}
		resp.Running = running

		// If running, query Ollama API for pulled models.
		if running {
			models, err := fetchOllamaModels(infra.ContainerID)
			if err != nil {
				slog.Error("failed to fetch ollama models", "error", err)
			} else {
				resp.ModelsPulled = models
			}
		}
	}

	// Detect GPU availability.
	resp.GPUAvailable = runtime.HasGPUAvailable()

	return c.JSON(resp)
}

// ollamaTagsResponse represents the response from the Ollama API /api/tags endpoint.
type ollamaTagsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

// fetchOllamaModels queries the Ollama API for the list of pulled models.
// It connects to the Ollama container via Docker's internal DNS or host networking.
func fetchOllamaModels(containerID string) ([]string, error) {
	// Try to reach Ollama on localhost (port-forwarded) or via container network.
	// In production the API server may be outside the Docker network,
	// so we try the internal URL first then fallback to localhost.
	urls := []string{
		runtime.OllamaInternalURL + "/api/tags",
		"http://localhost:" + runtime.OllamaInternalPort + "/api/tags",
	}

	client := &http.Client{Timeout: 5 * time.Second}

	for _, url := range urls {
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			continue
		}

		var tags ollamaTagsResponse
		if err := json.Unmarshal(body, &tags); err != nil {
			return nil, fmt.Errorf("parsing ollama tags response: %w", err)
		}

		models := make([]string, 0, len(tags.Models))
		for _, m := range tags.Models {
			models = append(models, m.Name)
		}
		return models, nil
	}

	return []string{}, nil
}
