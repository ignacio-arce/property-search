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

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	first := recordedMigrations(t, pool)

	// The second run must apply nothing: the count is unchanged, not merely
	// "1" (which would break every time a migration is added).
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	second := recordedMigrations(t, pool)

	if second != first {
		t.Errorf("recorded migrations went from %d to %d on a repeat run", first, second)
	}
	if first == 0 {
		t.Error("no migrations were recorded")
	}

	for _, table := range []string{
		"users", "search_urls", "listings", "listing_sources", "deliveries",
		"settings", "schema_migrations", "ratings", "bot_state",
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
}

func recordedMigrations(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM schema_migrations").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
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
	// The global defaults plus the one-time bootstrap marker.
	assertCount(t, pool, "settings", len(globalSettings)+1)

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

// The seed is a bootstrap, not a source of truth: once a user manages their
// searches from the chat, a restart must not resurrect what they deleted.
func TestSeedDoesNotResurrectDeletedSearches(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	in := SeedInput{
		ChatID: "42",
		URLs:   []SeedURL{{Label: "Casa", URL: "https://www.zonaprop.com.ar/casa.html"}},
	}
	if err := Seed(ctx, pool, in); err != nil {
		t.Fatal(err)
	}
	// The user deletes the search from the chat.
	if _, err := pool.Exec(ctx, `DELETE FROM search_urls WHERE user_id = 42`); err != nil {
		t.Fatal(err)
	}

	// A restart re-runs the seeds.
	if err := Seed(ctx, pool, in); err != nil {
		t.Fatal(err)
	}

	assertCount(t, pool, "search_urls", 0)
}

// The same for the whole account: if the operator wipes their data, a restart must
// not bring it back.
func TestSeedDoesNotResurrectADeletedUser(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	in := SeedInput{
		ChatID: "42",
		URLs:   []SeedURL{{Label: "Casa", URL: "https://www.zonaprop.com.ar/casa.html"}},
	}
	if err := Seed(ctx, pool, in); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE user_id = 42`); err != nil {
		t.Fatal(err)
	}
	if err := Seed(ctx, pool, in); err != nil {
		t.Fatal(err)
	}

	assertCount(t, pool, "users", 0)
}
