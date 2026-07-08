package postgres

import (
	"context"
	"database/sql"

	"github.com/sunnyside/atlas/atlas-backend/internal/repository"
)

// TxManager is the postgres implementation of repository.TxManager.
//
// It is the only postgres data-layer type that starts, commits, or rolls back
// transactions. Repositories created inside WithinTx are bound to the callback
// transaction and must not be stored after the callback returns.
type TxManager struct {
	db *sql.DB
}

func NewTxManager(db *sql.DB) *TxManager {
	return &TxManager{db: db}
}

// Repositories returns the root DB-backed repository set for read-only or
// caller-managed operations. Mutating service workflows should prefer WithinTx.
func (m *TxManager) Repositories() repository.Repositories {
	return newRepositories(m.db)
}

func (m *TxManager) WithinTx(ctx context.Context, fn func(ctx context.Context, repos repository.Repositories) error) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	if err := fn(ctx, newRepositories(tx)); err != nil {
		_ = tx.Rollback()
		return err
	}

	return tx.Commit()
}

func newRepositories(exec DBExecutor) repository.Repositories {
	return repository.Repositories{
		VehicleAgents:     newVehicleAgentRepository(exec),
		Drones:            newDroneRepository(exec),
		Telemetry:         newTelemetryRepository(exec),
		Commands:          newCommandRepository(exec),
		Missions:          newMissionRepository(exec),
		MissionExecutions: newMissionExecutionRepository(exec),
	}
}
