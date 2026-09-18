package digest_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"zonapropbot/internal/config"
	"zonapropbot/internal/db"
	"zonapropbot/internal/dbtest"
	"zonapropbot/internal/digest"
	"zonapropbot/internal/fetch"
	"zonapropbot/internal/logging"
	"zonapropbot/internal/repo"
)

// TestEndToEndAgainstTheRealPage runs the whole vertical path — config, Postgres,
// migrations, indexing, parser, fetch stack, baseline and delivery bookkeeping —
// against the real captured search page served over local HTTP.
//
// It deliberately does not touch Zonaprop or Telegram: the IP is degraded from the
// risk probes and there is no bot token here. Serving the committed fixture keeps
// the test hermetic while still exercising the real fetch client, the real parser
// and the real SQL.
func TestEndToEndAgainstTheRealPage(t *testing.T) {
	fixture, err := os.ReadFile("../../fixtures/search_gba_norte.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(fixture)
	}))
	defer page.Close()

	pool := dbtest.NewPool(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	r := repo.New(pool)

	// Seed the operator's user and search, exactly like the startup seeds do.
	if err := db.Seed(ctx, pool, db.SeedInput{
		ChatID: "1",
		URLs:   []db.SeedURL{{Label: "fixture local", URL: page.URL}},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Seeds mark searches valid, which is what the digest filters on.
	urls, err := r.ListValidSearchURLs(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 1 {
		t.Fatalf("seeded %d valid searches, want 1", len(urls))
	}

	// The real fetch client, with no FlareSolverr configured: the TLS path is used.
	cfg, err := config.Load(func(key string) string {
		if key == "FETCH_RETRY_LIMIT" {
			return ""
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	fc := fetch.New(cfg)
	notifier := &recordingNotifier{}
	runner := &digest.Runner{
		Repo: r, Fetcher: fc, Notifier: notifier, Logger: logging.Discard(),
	}

	// First run: the page yields 30 listings and all of them are baselined.
	sent, err := runner.RunForUser(ctx, 1, 1)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if sent != 0 {
		t.Errorf("first run sent %d, want 0 (the inventory is baselined, not replayed)", sent)
	}

	indexed, err := r.CountListings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if indexed != 30 {
		t.Errorf("indexed %d listings, want 30 from the real page", indexed)
	}

	candidates, err := r.Candidates(ctx, 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Errorf("got %d candidates after baseline, want 0", len(candidates))
	}

	// Second run over the same unchanged page must be silent and idempotent.
	sent, err = runner.RunForUser(ctx, 1, 1)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if sent != 0 {
		t.Errorf("second run sent %d, want 0", sent)
	}
	indexed, err = r.CountListings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if indexed != 30 {
		t.Errorf("indexed %d listings after a repeat run, want 30 (no duplicates)", indexed)
	}
}
