package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sunnyside/atlas/atlas-backend/internal/repositories"
)

// PostgresTxManager creates repository sets bound to one pgx transaction. The
// factory runs only after BeginTx succeeds, so it can never receive the pool.
type PostgresTxManager struct {
	pool         *pgxpool.Pool
	repositories func(TxExecutor) repositories.Repositories
}

var _ repositories.TxManager = (*PostgresTxManager)(nil)

func NewPostgresTxManager(
	pool *pgxpool.Pool,
	repositoryFactory func(TxExecutor) repositories.Repositories,
) repositories.TxManager {
	return &PostgresTxManager{pool: pool, repositories: repositoryFactory}
}

func (m *PostgresTxManager) WithinTransaction(
	ctx context.Context,
	work func(repositories.Repositories) error,
) error {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	// Rollback is the safety net for every early return and panic. Once Commit
	// succeeds pgx returns ErrTxClosed here, which is safe to ignore.
	defer func() { _ = tx.Rollback(ctx) }()

	repositories := m.repositories(postgresTxExecutor{Tx: tx})
	if err := work(repositories); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}
