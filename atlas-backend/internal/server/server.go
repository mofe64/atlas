package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/config"
)

func Run(ctx context.Context, cfg config.Config, handler http.Handler, logger *slog.Logger) error {
	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	listenErrors := make(chan error, 1)
	go func() {
		logger.Info("Atlas backend listening", "address", cfg.HTTPAddr)
		listenErrors <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-listenErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()

		logger.Info("shutting down Atlas backend")
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		return nil
	}
}
