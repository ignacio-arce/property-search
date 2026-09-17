// Package db owns the Postgres connection, the versioned migrations and the
// idempotent seeds that run at startup.
package db

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultConnectTimeout = 30 * time.Second
	pingTimeout           = 5 * time.Second
	retryDelay            = 500 * time.Millisecond
)

// Options tunes the startup connection.
type Options struct {
	// ConnectTimeout bounds the total time spent retrying the initial connection.
	// It exists to cover the compose healthcheck race: the bot container may be
	// released while Postgres is still finishing initialisation.
	ConnectTimeout time.Duration
	Logger         *log.Logger
}

// Open connects to Postgres and verifies the connection with a ping, retrying
// until ConnectTimeout expires. A database that is briefly unavailable must not
// crash the bot, and one that never comes up must fail with a clear error.
func Open(ctx context.Context, dsn string, opts Options) (*pgxpool.Pool, error) {
	if opts.ConnectTimeout <= 0 {
		opts.ConnectTimeout = defaultConnectTimeout
	}

	deadline := time.Now().Add(opts.ConnectTimeout)
	var lastErr error

	for attempt := 1; ; attempt++ {
		pool, err := pgxpool.New(ctx, dsn)
		if err == nil {
			pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
			err = pool.Ping(pingCtx)
			cancel()
			if err == nil {
				return pool, nil
			}
			pool.Close()
		}
		lastErr = err

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("postgres unreachable after %s: %w", opts.ConnectTimeout, lastErr)
		}
		if opts.Logger != nil {
			opts.Logger.Printf("postgres not ready (attempt %d): %v", attempt, err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(retryDelay):
		}
	}
}
