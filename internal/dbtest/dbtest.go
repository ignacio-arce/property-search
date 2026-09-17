// Package dbtest provides a real, throwaway Postgres for integration tests.
//
// Tests that need the database call NewPool and are skipped automatically when no
// database is reachable, so `go test ./...` stays green on a machine without one.
package dbtest

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const connectTimeout = 20 * time.Second

// NewPool returns a pool connected to a freshly created database. The database is
// dropped when the test finishes, so tests are isolated from each other and from
// any data the bot has.
func NewPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	base := baseDSN()
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()

	admin, err := pgxpool.New(ctx, base)
	if err == nil {
		err = admin.Ping(ctx)
	}
	if err != nil {
		t.Skipf("no Postgres available, skipping integration test (%v)", err)
	}

	name := fmt.Sprintf("zptest_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		admin.Close()
		t.Fatalf("create test database: %v", err)
	}

	pool, err := pgxpool.New(ctx, withDatabase(base, name))
	if err != nil {
		admin.Close()
		t.Fatalf("connect to test database: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		dropCtx, dropCancel := context.WithTimeout(context.Background(), connectTimeout)
		defer dropCancel()
		if err := dropDatabase(dropCtx, admin, quoted); err != nil {
			t.Logf("dropping test database %s: %v", name, err)
		}
		admin.Close()
	})

	return pool
}

// dropDatabase retries briefly: DROP DATABASE fails while any connection is still
// closing.
func dropDatabase(ctx context.Context, admin *pgxpool.Pool, quoted string) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+quoted+" WITH (FORCE)")
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// baseDSN resolves the connection string for the maintenance database, mirroring
// how the bot itself is configured so tests and the running stack agree.
//
// Priority: TEST_POSTGRES_DSN, then POSTGRES_* from the process environment, then
// POSTGRES_* from the repository .env file (which is what docker compose uses),
// then localhost defaults.
func baseDSN() string {
	if v := os.Getenv("TEST_POSTGRES_DSN"); v != "" {
		return v
	}

	get := func(key, def string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		if v := fileEnv(key); v != "" {
			return v
		}
		return def
	}

	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(get("POSTGRES_USER", "zonaprop"), get("POSTGRES_PASSWORD", "")),
		Host:   net.JoinHostPort(get("POSTGRES_HOST", "localhost"), get("POSTGRES_PORT", "5432")),
		Path:   "/" + get("POSTGRES_DB", "zonaprop"),
	}
	return u.String()
}

func withDatabase(dsn, db string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	u.Path = "/" + db
	return u.String()
}

// fileEnv reads a KEY=VALUE line from the repository .env, if it exists. Errors
// are ignored on purpose: a missing .env just means the caller falls back to
// defaults, and dbtest must never fail a test for that.
func fileEnv(key string) string {
	path := filepath.Join(repoRoot(), ".env")
	body, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != key {
			continue
		}
		return strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return ""
}

func repoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}
