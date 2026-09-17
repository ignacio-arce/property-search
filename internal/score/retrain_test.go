package score

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"zonapropbot/internal/db"
	"zonapropbot/internal/dbtest"
	"zonapropbot/internal/model"
	"zonapropbot/internal/repo"
)

func setup(t *testing.T) (*repo.Repo, *pgxpool.Pool) {
	t.Helper()
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (user_id, chat_id, state, active) VALUES (1, 1, 'ready', true)`); err != nil {
		t.Fatal(err)
	}
	return repo.New(pool), pool
}

// rate inserts a listing, a delivery carrying the snapshot the user saw, and a
// rating for it.
func rate(t *testing.T, pool *pgxpool.Pool, id string, l model.Listing, label int, sentAt time.Time) {
	t.Helper()
	ctx := context.Background()

	snap, err := json.Marshal(l.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var listingID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO listings (zonaprop_id, canonical_url, operation_type, currency, features)
		 VALUES ($1, $2, NULLIF($3,''), NULLIF($4,''), $5) RETURNING id`,
		id, l.CanonicalURL, l.Operation, l.Currency, snap).Scan(&listingID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO deliveries (user_id, listing_id, features, status, sent_at, score)
		 VALUES (1, $1, $2, 'sent', $3, 0)`, listingID, snap, sentAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO ratings (user_id, listing_id, label, created_at) VALUES (1, $1, $2, $3)`,
		listingID, label, sentAt); err != nil {
		t.Fatal(err)
	}
}

func TestRetrainBuildsAndActivatesANewGeneration(t *testing.T) {
	r, pool := setup(t)
	ctx := context.Background()

	liked := saleListing()
	liked.CanonicalURL = "https://www.zonaprop.com.ar/p/a.html"
	disliked := saleListing()
	disliked.CanonicalURL = "https://www.zonaprop.com.ar/p/b.html"
	disliked.Banos = intp(1)

	now := time.Now()
	rate(t, pool, "aaa", liked, 1, now.Add(-2*time.Hour))
	rate(t, pool, "bbb", liked, 1, now.Add(-time.Hour))
	rate(t, pool, "ccc", disliked, 0, now)

	version, err := Retrain(ctx, r, 1)
	if err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("version = %d, want 1", version)
	}
	if got, _ := r.ModelVersion(ctx, 1); got != 1 {
		t.Errorf("active model version = %d, want 1", got)
	}

	w, err := Load(ctx, r, 1)
	if err != nil {
		t.Fatal(err)
	}
	if w.Version != 1 {
		t.Errorf("loaded version = %d", w.Version)
	}

	// Two likes and one dislike land in the buckets they were shown under.
	partido := w.Buckets["partido"]["Vicente López"]
	if partido.Ups != 2 || partido.Downs != 1 {
		t.Errorf("partido bucket = %+v, want 2 ups / 1 down", partido)
	}
	banios := w.Buckets["banios"]
	if banios["2"].Ups != 2 {
		t.Errorf("banios=2 = %+v, want 2 ups", banios["2"])
	}
	if banios["1"].Downs != 1 {
		t.Errorf("banios=1 = %+v, want 1 down", banios["1"])
	}

	// Retraining twice advances the generation rather than mutating the old one,
	// so the scorer never reads half-written weights.
	again, err := Retrain(ctx, r, 1)
	if err != nil {
		t.Fatal(err)
	}
	if again != 2 {
		t.Errorf("second retrain version = %d, want 2", again)
	}
	if got, _ := r.ModelVersion(ctx, 1); got != 2 {
		t.Errorf("active version after second retrain = %d, want 2", got)
	}
}

func TestRetrainWithNoRatingsStillActivatesAnEmptyModel(t *testing.T) {
	r, _ := setup(t)
	ctx := context.Background()

	version, err := Retrain(ctx, r, 1)
	if err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Errorf("version = %d, want 1", version)
	}
	w, err := Load(ctx, r, 1)
	if err != nil {
		t.Fatal(err)
	}
	if score, used := w.Score(Extract(saleListing())); score != 0 || used != 0 {
		t.Errorf("an empty model must score 0 with no evidence, got (%v, %d)", score, used)
	}
}

func TestAgreementNeedsEnoughSamples(t *testing.T) {
	r, _ := setup(t)
	ctx := context.Background()

	_, _, ok, err := Agreement(ctx, r, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("agreement must not be reported without evidence")
	}
}

func TestSummaryTextIsHonestAboutWhatIsKnown(t *testing.T) {
	if got := SummaryText(0, 0, 0, 0, 0, false); !strings.Contains(got, "Todavía no tengo calificaciones") {
		t.Errorf("empty model text = %q", got)
	}
	if got := SummaryText(0, 3, 1, 0, 4, false); !strings.Contains(got, "4 de 30") {
		t.Errorf("should say how far it is from a measurable agreement: %q", got)
	}
	if got := SummaryText(4, 40, 10, 0.75, 50, true); !strings.Contains(got, "75%") {
		t.Errorf("should report the agreement: %q", got)
	}
}
