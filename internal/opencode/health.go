package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// HealthResponse is the response from GET /global/health.
type HealthResponse struct {
	Healthy bool   `json:"healthy"`
	Version string `json:"version"`
}

// WaitForHealth polls the OpenCode server health endpoint until it reports
// healthy or the timeout expires. It retries every retryInterval.
func WaitForHealth(baseURL, username, password string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	client := &http.Client{Timeout: 5 * time.Second}

	// Try immediately before waiting for the first tick.
	if err := checkHealthOnce(ctx, client, baseURL, username, password); err == nil {
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("opencode server not healthy after %s", timeout)
		case <-ticker.C:
			if err := checkHealthOnce(ctx, client, baseURL, username, password); err == nil {
				return nil
			} else {
				slog.Debug("opencode health check retry", "error", err)
			}
		}
	}
}

// checkHealthOnce performs a single health check request.
func checkHealthOnce(ctx context.Context, client *http.Client, baseURL, username, password string) error {
	url := fmt.Sprintf("%s/global/health", baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if password != "" {
		req.SetBasicAuth(username, password)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to opencode server: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("opencode server returned status %d", resp.StatusCode)
	}

	var health HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return fmt.Errorf("parsing health response: %w", err)
	}

	if !health.Healthy {
		return fmt.Errorf("opencode server reports unhealthy")
	}

	slog.Info("opencode server healthy", "version", health.Version)
	return nil
}
