package database

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// TxExecutor is the transaction-backed SQL surface available to Atlas
// repositories. The private marker prevents pgxpool.Pool, which has similar SQL
// methods, from satisfying this interface accidentally.
// This way our repos can only use our custom tx mgmt type.
type TxExecutor interface {
	// Exec executes SQL that does not return rows, such as UPDATE, DELETE, or
	// INSERT without RETURNING. Its command tag includes the affected row count.
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)

	// Query executes SQL that can return multiple rows. The caller must iterate
	// over and close the returned rows.
	Query(context.Context, string, ...any) (pgx.Rows, error)

	// QueryRow executes SQL expected to return at most one row. Query errors are
	// reported when the caller invokes Scan on the returned row.
	QueryRow(context.Context, string, ...any) pgx.Row

	// implementsAtlasTransaction is a compile-time marker. It has no runtime
	// behavior; it ensures clients (eg repositories) cannot receive pgxpool.Pool.
	implementsAtlasTransaction()
}

// postgresTxExecutor adapts pgx.Tx to TxExecutor. Embedding pgx.Tx promotes its
// Exec, Query, and QueryRow methods, so the adapter only needs the private marker.
// PostgresTxManager creates this adapter only after BeginTx succeeds.
type postgresTxExecutor struct {
	pgx.Tx
}

func (postgresTxExecutor) implementsAtlasTransaction() {}

// Compile-time assertion: changing either type incompatibly fails the build here.
var _ TxExecutor = postgresTxExecutor{}
