// Package db owns the single pgxpool.Pool shared by the whole process.
// pgx is used directly (no database/sql, no ORM) — this is the closest
// Go equivalent to calling Supabase's Postgres straight from a JS
// client, and it avoids the extra allocation/reflection overhead that
// database/sql's generic driver interface adds on every query.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

// NewPool creates a pgxpool sized deliberately small: on a 1 vCPU/1GB VM,
// a handful of connections is plenty of concurrency for a Gin API and
// costs far less memory (both client-side buffers and Postgres backend
// processes) than the pgxpool defaults (which scale with CPU count and
// can open far more connections than this workload needs).
func NewPool(ctx context.Context, databaseURL string, maxConns, minConns int32, log zerolog.Logger) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("db: parse config: %w", err)
	}

	cfg.MaxConns = maxConns
	cfg.MinConns = minConns
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: create pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping failed: %w", err)
	}

	log.Info().
		Int32("max_conns", maxConns).
		Int32("min_conns", minConns).
		Msg("database pool ready")

	return pool, nil
}
