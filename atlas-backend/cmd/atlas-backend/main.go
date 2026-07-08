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

	"github.com/sunnyside/atlas/atlas-backend/internal/database"
	"github.com/sunnyside/atlas/atlas-backend/internal/httpapi"
	postgresrepo "github.com/sunnyside/atlas/atlas-backend/internal/repository/postgres"
	"github.com/sunnyside/atlas/atlas-backend/internal/services"
	"github.com/sunnyside/atlas/atlas-backend/internal/transport/vehicleagentchannel"
	vehicleagentchannelpb "github.com/sunnyside/atlas/atlas-backend/internal/transport/vehicleagentchannelpb/atlas"
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

	vehicleAgentGRPCAddr := os.Getenv("ATLAS_VEHICLE_AGENT_GRPC_ADDR")
	if vehicleAgentGRPCAddr == "" {
		vehicleAgentGRPCAddr = ":9090"
	}

	deps, closeDB, err := openDependencies(context.Background(), logger)
	if err != nil {
		logger.Error("failed to open postgres db", "error", err)
		os.Exit(1)
	}
	defer closeDB()
	channelHub := vehicleagentchannel.NewHub(deps.vehicleAgentChannel, logger)

	server := &http.Server{
		Addr:              addr,
		Handler:           httpapi.NewRouterWithDispatchers(deps.httpAPI, channelHub, channelHub),
		ReadHeaderTimeout: 5 * time.Second,
	}

	grpcServer := grpc.NewServer()
	vehicleagentchannelpb.RegisterVehicleAgentChannelServiceServer(grpcServer, vehicleagentchannel.NewServer(channelHub))

	agentListener, err := net.Listen("tcp", vehicleAgentGRPCAddr)
	if err != nil {
		logger.Error("failed to listen for vehicle-agent gRPC channel", "addr", vehicleAgentGRPCAddr, "error", err)
		os.Exit(1)
	}

	errs := make(chan error, 2)
	go func() {
		logger.Info("starting atlas backend HTTP API", "addr", addr)
		errs <- server.ListenAndServe()
	}()

	go func() {
		logger.Info("starting atlas backend vehicle-agent gRPC channel", "addr", vehicleAgentGRPCAddr)
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

type backendDependencies struct {
	httpAPI             httpapi.Dependencies
	vehicleAgentChannel vehicleagentchannel.Dependencies
}

func openDependencies(ctx context.Context, logger *slog.Logger) (backendDependencies, func(), error) {
	dsn := strings.TrimSpace(os.Getenv("ATLAS_DATABASE_URL"))
	if dsn == "" {
		dsn = "postgres://atlas:atlas@127.0.0.1:5432/atlas?sslmode=disable"
	}

	db, err := database.OpenPostgres(ctx, dsn)
	if err != nil {
		return backendDependencies{}, func() {}, err
	}
	logger.Info("using postgres db repository")

	txManager := postgresrepo.NewTxManager(db)
	repos := txManager.Repositories()
	appServices := services.New(services.Dependencies{
		TxManager:    txManager,
		Repositories: repos,
	})

	deps := backendDependencies{
		httpAPI: httpapi.Dependencies{
			VehicleAgents: appServices.VehicleAgents,
			Telemetry:     appServices.Telemetry,
			Commands:      appServices.Commands,
			Missions:      appServices.Missions,
			Fleet:         appServices.Fleet,
		},
		vehicleAgentChannel: vehicleagentchannel.Dependencies{
			VehicleAgents: appServices.VehicleAgents,
			Telemetry:     appServices.Telemetry,
			Commands:      appServices.Commands,
			Missions:      appServices.Missions,
		},
	}
	closeDB := func() {
		if err := db.Close(); err != nil {
			logger.Warn("failed to close postgres db", "error", err)
		}
	}

	return deps, closeDB, nil
}
