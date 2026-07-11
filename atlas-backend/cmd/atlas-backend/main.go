package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/sunnyside/atlas/atlas-backend/internal/config"
	"github.com/sunnyside/atlas/atlas-backend/internal/httpapi"
	"github.com/sunnyside/atlas/atlas-backend/internal/server"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	handler, err := httpapi.NewRouter(cfg)
	if err != nil {
		logger.Error("create HTTP router", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := server.Run(ctx, cfg, handler, logger); err != nil {
		logger.Error("backend stopped", "error", err)
		os.Exit(1)
	}
}
