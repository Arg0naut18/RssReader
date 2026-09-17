package db

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens a pgxpool using DATABASE_URL from the environment.
// The pool is sized for ~1000 concurrent users behind the transaction pooler:
// MaxConns is kept low intentionally — PgBouncer multiplexes on top.
func Connect(ctx context.Context) (*pgxpool.Pool, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return nil, fmt.Errorf("DATABASE_URL environment variable not set")
	}

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parsing DATABASE_URL: %w", err)
	}

	// Keep the app-side pool small; PgBouncer (transaction pooler) handles
	// fan-out to the actual Postgres connection limit.
	cfg.MaxConns = 10
	cfg.MinConns = 2

	// PgBouncer in transaction mode does not support prepared statements —
	// each transaction may land on a different backend connection, so pgx's
	// statement cache causes "prepared statement already exists" (42P05).
	// Disable it by switching to the simple protocol, which sends plain SQL.
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("opening pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	return pool, nil
}
