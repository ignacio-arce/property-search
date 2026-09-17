package db

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"zonapropbot/internal/dbtest"
)

func TestMigrateIsIdempotent(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()

	for run := 1; run <= 2; run++ {
		if err := Migrate(ctx, pool); err != nil {
			t.Fatalf("migrate run %d: %v", run, err)
		}
	}

	for _, table := range []string{
		"users", "search_urls", "listings", "listing_sources", "deliveries",
		"settings", "schema_migrations",
	} {
		var exists bool
		err := pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			 WHERE table_schema = 'public' AND table_name = $1)`, table).Scan(&exists)
		if err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}
		if !exists {
			t.Errorf("table %s does not exist", table)
		}
	}

	// Running twice must not record the migration twice.
	var recorded int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 1 {
		t.Errorf("schema_migrations has %d rows, want 1", recorded)
	}
}

func TestSeedIsIdempotent(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	in := SeedInput{
		ChatID: "42",
		URLs: []SeedURL{
			{Label: "San Isidro 3amb", URL: "https://www.zonaprop.com.ar/a.html?n_pg=1&n_pos=3"},
			{Label: "Pilar", URL: "https://www.zonaprop.com.ar/b.html"},
		},
	}
	for run := 1; run <= 2; run++ {
		if err := Seed(ctx, pool, in); err != nil {
			t.Fatalf("seed run %d: %v", run, err)
		}
	}

	assertCount(t, pool, "users", 1)
	assertCount(t, pool, "search_urls", 2)
	assertCount(t, pool, "settings", len(globalSettings))

	// The stored canonical form must have dropped the tracking parameters.
	var norm string
	if err := pool.QueryRow(ctx,
		`SELECT url_norm FROM search_urls WHERE label = 'San Isidro 3amb'`).Scan(&norm); err != nil {
		t.Fatal(err)
	}
	if want := "https://www.zonaprop.com.ar/a.html"; norm != want {
		t.Errorf("url_norm = %q, want %q", norm, want)
	}
}

func TestSeedDoesNotOverwriteOperatorSettings(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	if err := Seed(ctx, pool, SeedInput{}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE settings SET value = '07:30' WHERE key = 'daily_hour'`); err != nil {
		t.Fatal(err)
	}
	if err := Seed(ctx, pool, SeedInput{}); err != nil {
		t.Fatal(err)
	}

	var got string
	if err := pool.QueryRow(ctx,
		`SELECT value FROM settings WHERE key = 'daily_hour'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "07:30" {
		t.Errorf("daily_hour = %q, want the operator value 07:30 to survive reseeding", got)
	}
}

func TestSeedWithoutChatIDIsNoop(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	// URLs without a chat id, and a chat id without URLs, both seed nothing.
	if err := Seed(ctx, pool, SeedInput{URLs: []SeedURL{{Label: "x", URL: "https://www.zonaprop.com.ar/x.html"}}}); err != nil {
		t.Fatal(err)
	}
	if err := Seed(ctx, pool, SeedInput{ChatID: "42"}); err != nil {
		t.Fatal(err)
	}

	assertCount(t, pool, "users", 0)
	assertCount(t, pool, "search_urls", 0)
}

func TestSeedRejectsNonNumericChatID(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	err := Seed(ctx, pool, SeedInput{
		ChatID: "not-a-number",
		URLs:   []SeedURL{{Label: "x", URL: "https://www.zonaprop.com.ar/x.html"}},
	})
	if err == nil {
		t.Fatal("expected an error for a non-numeric SEED_CHAT_ID")
	}
}

func assertCount(t *testing.T, pool *pgxpool.Pool, table string, want int) {
	t.Helper()
	var got int
	// table is a test-controlled constant, never user input.
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Errorf("%s has %d rows, want %d", table, got, want)
	}
}
