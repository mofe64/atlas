package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/database"
	"github.com/sunnyside/atlas/atlas-backend/internal/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repository"
	"github.com/sunnyside/atlas/atlas-backend/internal/testutil"
)

func TestTxManagerCommitsOnSuccess(t *testing.T) {
	txManager := newTestTxManager(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)

	err := txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		return insertRegisteredAgent(ctx, repos, "agent-commit", "drone-commit", "Commit Quad", now)
	})
	if err != nil {
		t.Fatalf("within tx: %v", err)
	}

	drones := txManager.Repositories().Drones.ListDrones(ctx, now)
	if len(drones) != 1 {
		t.Fatalf("expected committed drone, got %d", len(drones))
	}
}

func TestTxManagerRollsBackOnPanic(t *testing.T) {
	txManager := newTestTxManager(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	panicValue := "rollback panic"

	func() {
		defer func() {
			if recovered := recover(); recovered != panicValue {
				t.Fatalf("expected panic %q, got %v", panicValue, recovered)
			}
		}()

		_ = txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
			if err := insertRegisteredAgent(ctx, repos, "agent-panic", "drone-panic", "Panic Quad", now); err != nil {
				return err
			}
			panic(panicValue)
		})
	}()

	drones := txManager.Repositories().Drones.ListDrones(ctx, now)
	if len(drones) != 0 {
		t.Fatalf("expected panic rollback to discard drone, got %d", len(drones))
	}
}

func TestTxManagerRepositoriesShareCallbackTransaction(t *testing.T) {
	txManager := newTestTxManager(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	var command models.CommandRequest

	err := txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		if err := insertRegisteredAgent(ctx, repos, "agent-shared", "drone-shared", "Shared Tx Quad", now); err != nil {
			return err
		}

		commandID, err := repos.Commands.GenerateCommandID(ctx)
		if err != nil {
			return err
		}
		command = models.CommandRequest{
			ID:             commandID,
			DroneID:        "drone-shared",
			VehicleAgentID: "agent-shared",
			Type:           models.CommandTypeArm,
			State:          models.CommandStateAuthorized,
			RequestedBy:    "test",
			RequestedAt:    now,
			UpdatedAt:      now,
		}
		return repos.Commands.InsertCommand(ctx, command)
	})
	if err != nil {
		t.Fatalf("within tx: %v", err)
	}

	if command.ID == "" {
		t.Fatal("expected command created inside shared transaction")
	}
	if command.State != models.CommandStateAuthorized {
		t.Fatalf("expected inserted command to remain authorized, got %q", command.State)
	}
}

func TestTxManagerRollsBackOnCallbackError(t *testing.T) {
	txManager := newTestTxManager(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	rollbackErr := errors.New("rollback")

	err := txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		if err := insertRegisteredAgent(ctx, repos, "agent-error", "drone-error", "Error Quad", now); err != nil {
			return err
		}
		return rollbackErr
	})
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("expected rollback error, got %v", err)
	}

	drones := txManager.Repositories().Drones.ListDrones(ctx, now)
	if len(drones) != 0 {
		t.Fatalf("expected callback error rollback to discard drone, got %d", len(drones))
	}
}

func newTestTxManager(t *testing.T) *TxManager {
	t.Helper()

	db, err := database.OpenPostgres(context.Background(), testutil.DatabaseURL(t))
	if err != nil {
		t.Fatalf("open postgres test db: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("close postgres test db: %v", err)
		}
	})

	return NewTxManager(db)
}

func insertRegisteredAgent(ctx context.Context, repos repository.Repositories, agentID string, droneID string, droneName string, now time.Time) error {
	if err := repos.Drones.UpsertDroneRegistration(ctx, droneID, droneName, now); err != nil {
		return err
	}
	return repos.VehicleAgents.UpsertVehicleAgentRegistration(ctx, models.VehicleAgent{
		ID:                  agentID,
		DroneID:             droneID,
		Version:             "0.1.0",
		VehicleAgentVersion: "0.1.0",
		IdentityStatus:      models.DeviceIdentityActive,
		RegisteredAt:        now,
		LastSeenAt:          now,
		CommandChannelState: models.CommandChannelDisconnected,
	})
}
