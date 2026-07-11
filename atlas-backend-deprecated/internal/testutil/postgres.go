package testutil

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

var postgresTestDB = struct {
	mu        sync.Mutex
	startOnce sync.Once
	dsn       string
	err       error
	container testcontainers.Container
}{}

// DatabaseURL returns a migrated, empty Postgres database URL and holds an
// exclusive test lock until the caller's test completes.
func DatabaseURL(t *testing.T) string {
	t.Helper()

	postgresTestDB.mu.Lock()
	t.Cleanup(postgresTestDB.mu.Unlock)

	postgresTestDB.startOnce.Do(func() {
		postgresTestDB.dsn, postgresTestDB.container, postgresTestDB.err = startPostgresContainer()
	})
	if postgresTestDB.err != nil {
		t.Fatalf("start postgres test container: %v", postgresTestDB.err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, err := sql.Open("pgx", postgresTestDB.dsn)
	if err != nil {
		t.Fatalf("open postgres test db: %v", err)
	}
	defer db.Close()

	if err := applyMigrations(ctx, db); err != nil {
		t.Fatalf("apply postgres test migrations: %v", err)
	}
	if err := truncatePublicTables(ctx, db); err != nil {
		t.Fatalf("truncate postgres test db: %v", err)
	}

	return postgresTestDB.dsn
}

func startPostgresContainer() (dsn string, container testcontainers.Container, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%v", recovered)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	postgresContainer, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("atlas_test"),
		postgres.WithUsername("atlas"),
		postgres.WithPassword("atlas"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(90*time.Second),
		),
	)
	if err != nil {
		return "", nil, err
	}

	dsn, err = postgresContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = postgresContainer.Terminate(ctx)
		return "", nil, err
	}

	return dsn, postgresContainer, nil
}

func applyMigrations(ctx context.Context, db *sql.DB) error {
	migrations, err := migrationFiles()
	if err != nil {
		return err
	}
	for _, migration := range migrations {
		statement, err := os.ReadFile(migration)
		if err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, string(statement)); err != nil {
			return err
		}
	}
	return nil
}

func migrationFiles() ([]string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return nil, os.ErrInvalid
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	matches, err := filepath.Glob(filepath.Join(root, "migrations", "*.sql"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	return matches, nil
}

func truncatePublicTables(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `
		SELECT tablename
		FROM pg_tables
		WHERE schemaname = 'public'
		ORDER BY tablename
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var tableNames []string
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return err
		}
		tableNames = append(tableNames, `"`+strings.ReplaceAll(tableName, `"`, `""`)+`"`)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(tableNames) == 0 {
		return nil
	}

	_, err = db.ExecContext(ctx, "TRUNCATE TABLE "+strings.Join(tableNames, ", ")+" RESTART IDENTITY CASCADE")
	return err
}
