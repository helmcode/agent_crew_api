package main

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/helmcode/agent-crew/internal/api"
	"github.com/helmcode/agent-crew/internal/models"
	"github.com/helmcode/agent-crew/internal/runtime"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	slog.Info("starting orchestrator API")

	// Database.
	dbPath := os.Getenv("DATABASE_PATH")
	if dbPath == "" {
		dbPath = "agentcrew.db"
	}
	db, err := models.InitDB(dbPath)
	if err != nil {
		slog.Error("failed to initialize database", "error", err)
		os.Exit(1)
	}

	// Runtime.
	var rt runtime.AgentRuntime
	switch os.Getenv("RUNTIME") {
	case "kubernetes":
		slog.Info("initializing kubernetes runtime")
		rt, err = runtime.NewK8sRuntime()
		if err != nil {
			slog.Error("failed to initialize kubernetes runtime", "error", err)
			os.Exit(1)
		}
	default:
		slog.Info("initializing docker runtime")
		rt, err = runtime.NewDockerRuntime()
		if err != nil {
			slog.Error("failed to initialize docker runtime", "error", err)
			os.Exit(1)
		}
	}

	// HTTP server.
	listenAddr := os.Getenv("LISTEN_ADDR")
	if listenAddr == "" {
		listenAddr = ":8080"
	}

	srv := api.NewServer(db, rt)

	// Start server in background.
	go func() {
		if err := srv.Listen(listenAddr); err != nil {
			slog.Error("server error", "error", err)
		}
	}()

	// Wait for shutdown signal.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down orchestrator API")
	if err := srv.Shutdown(); err != nil {
		slog.Error("shutdown error", "error", err)
	}
}
