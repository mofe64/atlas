package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/config"
	"github.com/sunnyside/atlas/atlas-backend/internal/database"
	"github.com/sunnyside/atlas/atlas-backend/internal/httpapi"
	postgresrepository "github.com/sunnyside/atlas/atlas-backend/internal/repositories/postgres"
	"github.com/sunnyside/atlas/atlas-backend/internal/server"
	authservice "github.com/sunnyside/atlas/atlas-backend/internal/services/auth"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Fail startup when PostgreSQL is unavailable instead of accepting HTTP
	// requests that every meaningful endpoint would later fail to serve.
	startupCtx, cancelStartup := context.WithTimeout(ctx, 15*time.Second)
	pool, err := database.Open(startupCtx, cfg.DatabaseURL)
	cancelStartup()
	if err != nil {
		logger.Error("connect to PostgreSQL", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Every auth repository is created inside TxManager and bound to its pgx.Tx.
	// Services never receive a pool-backed repository.
	authTxManager := database.NewPostgresTxManager(pool, postgresrepository.NewRepositories)
	authService, err := authservice.NewService(authTxManager, authservice.ServiceConfig{
		IdleTimeout:           cfg.SessionIdleTimeout,
		AbsoluteSessionExpiry: cfg.SessionAbsoluteExpiry,
		SessionRetention:      cfg.SessionRetention,
	})
	if err != nil {
		logger.Error("create authentication service", "error", err)
		os.Exit(1)
	}
	go cleanInactiveSessions(ctx, authService, logger)

	handler, err := httpapi.NewRouter(cfg, authService, pool)
	if err != nil {
		logger.Error("create HTTP router", "error", err)
		os.Exit(1)
	}

	if err := server.Run(ctx, cfg, handler, logger); err != nil {
		logger.Error("backend stopped", "error", err)
		os.Exit(1)
	}
}

func cleanInactiveSessions(ctx context.Context, service *authservice.Service, logger *slog.Logger) {
	cleanup := func() {
		deleted, err := service.CleanupInactiveSessions(ctx)
		if err != nil && ctx.Err() == nil {
			logger.Error("clean inactive sessions", "error", err)
			return
		}
		if deleted > 0 {
			logger.Info("cleaned inactive sessions", "deleted", deleted)
		}
	}

	cleanup()
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanup()
		}
	}
}
