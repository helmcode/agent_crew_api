// Package main provides a test server with mock runtime for integration testing.
// It uses an in-memory SQLite database and a mock Docker runtime that returns
// successful responses for all operations without requiring a real Docker daemon.
package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/helmcode/agent-crew/internal/api"
	"github.com/helmcode/agent-crew/internal/auth"
	"github.com/helmcode/agent-crew/internal/models"
	"github.com/helmcode/agent-crew/internal/runtime"
)

// mockRuntime implements runtime.AgentRuntime for integration testing.
type mockRuntime struct{}

func (m *mockRuntime) DeployInfra(_ context.Context, _ runtime.InfraConfig) error {
	return nil
}

func (m *mockRuntime) DeployAgent(_ context.Context, cfg runtime.AgentConfig) (*runtime.AgentInstance, error) {
	return &runtime.AgentInstance{
		ID:     "mock-container-" + cfg.Name,
		Name:   cfg.Name,
		Status: "running",
	}, nil
}

func (m *mockRuntime) StopAgent(_ context.Context, _ string) error  { return nil }
func (m *mockRuntime) RemoveAgent(_ context.Context, _ string) error { return nil }

func (m *mockRuntime) GetStatus(_ context.Context, id string) (*runtime.AgentStatus, error) {
	return &runtime.AgentStatus{ID: id, Name: "mock", Status: "running"}, nil
}

func (m *mockRuntime) StreamLogs(_ context.Context, _ string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("mock log output\n")), nil
}

func (m *mockRuntime) TeardownInfra(_ context.Context, _ string) error {
	return nil
}

func (m *mockRuntime) GetNATSURL(teamName string) string {
	return "nats://team-" + teamName + "-nats:4222"
}

func (m *mockRuntime) GetNATSConnectURL(_ context.Context, teamName string) (string, error) {
	return "nats://127.0.0.1:14222", nil
}

func (m *mockRuntime) ExecInContainer(_ context.Context, _ string, cmd []string) (string, error) {
	return "mock exec output", nil
}

func (m *mockRuntime) ReadFile(_ context.Context, _ string, path string) ([]byte, error) {
	if err := runtime.ValidateAgentFilePath(path); err != nil {
		return nil, err
	}
	return []byte("# Mock file content\n"), nil
}

func (m *mockRuntime) WriteFile(_ context.Context, _ string, path string, _ []byte) error {
	return runtime.ValidateAgentFilePath(path)
}

func (m *mockRuntime) CopyToContainer(_ context.Context, _ string, _ string, _ []byte) error {
	return nil
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	slog.Info("starting test server with mock runtime")

	db, err := models.InitDB(":memory:")
	if err != nil {
		slog.Error("failed to initialize database", "error", err)
		os.Exit(1)
	}

	listenAddr := os.Getenv("LISTEN_ADDR")
	if listenAddr == "" {
		listenAddr = ":3333"
	}

	noopAuth, err := auth.NewNoopProvider(db)
	if err != nil {
		slog.Error("failed to initialize noop auth provider", "error", err)
		os.Exit(1)
	}

	srv := api.NewServer(db, &mockRuntime{}, noopAuth)

	go func() {
		if err := srv.Listen(listenAddr); err != nil {
			slog.Error("server error", "error", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down test server")
	if err := srv.Shutdown(); err != nil {
		slog.Error("shutdown error", "error", err)
	}
}
