package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/agentchannel"
	agentchannelpb "github.com/sunnyside/atlas/atlas-backend/internal/agentchannelpb/atlas"
	"github.com/sunnyside/atlas/atlas-backend/internal/httpapi"
	"github.com/sunnyside/atlas/atlas-backend/internal/registry"
	"google.golang.org/grpc"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	addr := os.Getenv("ATLAS_BACKEND_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	agentGRPCAddr := os.Getenv("ATLAS_AGENT_GRPC_ADDR")
	if agentGRPCAddr == "" {
		agentGRPCAddr = ":9090"
	}

	reg, closeStore, err := openRegistry(context.Background(), logger)
	if err != nil {
		logger.Error("failed to open registry store", "error", err)
		os.Exit(1)
	}
	defer closeStore()
	channelHub := agentchannel.NewHub(reg, logger)

	server := &http.Server{
		Addr:              addr,
		Handler:           httpapi.NewRouterWithRegistryAndDispatchers(reg, channelHub, channelHub),
		ReadHeaderTimeout: 5 * time.Second,
	}

	grpcServer := grpc.NewServer()
	agentchannelpb.RegisterAgentChannelServiceServer(grpcServer, agentchannel.NewServer(channelHub))

	agentListener, err := net.Listen("tcp", agentGRPCAddr)
	if err != nil {
		logger.Error("failed to listen for agent gRPC channel", "addr", agentGRPCAddr, "error", err)
		os.Exit(1)
	}

	errs := make(chan error, 2)
	go func() {
		logger.Info("starting atlas backend HTTP API", "addr", addr)
		errs <- server.ListenAndServe()
	}()

	go func() {
		logger.Info("starting atlas backend agent gRPC channel", "addr", agentGRPCAddr)
		errs <- grpcServer.Serve(agentListener)
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errs:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("backend stopped unexpectedly", "error", err)
			os.Exit(1)
		}
	case sig := <-stop:
		logger.Info("shutdown signal received", "signal", sig.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	grpcServer.GracefulStop()

	if err := server.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}

	logger.Info("atlas backend stopped")
}

func openRegistry(ctx context.Context, logger *slog.Logger) (registry.Store, func(), error) {
	storeKind := strings.ToLower(strings.TrimSpace(os.Getenv("ATLAS_STORE")))
	if storeKind == "" {
		storeKind = "postgres"
	}

	switch storeKind {
	case "postgres":
		dsn := strings.TrimSpace(os.Getenv("ATLAS_DATABASE_URL"))
		if dsn == "" {
			dsn = "postgres://atlas:atlas@127.0.0.1:5432/atlas?sslmode=disable"
		}
		store, err := registry.OpenPostgresStore(ctx, dsn)
		if err != nil {
			return nil, func() {}, err
		}
		logger.Info("using postgres registry store")
		return store, func() {
			if err := store.Close(); err != nil {
				logger.Warn("failed to close postgres store", "error", err)
			}
		}, nil
	case "sqlite":
		path := strings.TrimSpace(os.Getenv("ATLAS_SQLITE_PATH"))
		if path == "" {
			path = ".atlas-run/atlas.db"
		}
		store, err := registry.OpenSQLiteStore(ctx, path)
		if err != nil {
			return nil, func() {}, err
		}
		logger.Info("using sqlite registry store", "path", path)
		return store, func() {
			if err := store.Close(); err != nil {
				logger.Warn("failed to close sqlite store", "error", err)
			}
		}, nil
	default:
		return nil, func() {}, errors.New("ATLAS_STORE must be postgres or sqlite")
	}
}
