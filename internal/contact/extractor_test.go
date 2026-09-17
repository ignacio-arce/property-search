package contact

import (
	"context"
	"errors"
	"io"
	"log"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"zonapropbot/internal/db"
	"zonapropbot/internal/dbtest"
	"zonapropbot/internal/fetch"
	"zonapropbot/internal/repo"
)

type fakeFetcher struct {
	body  []byte
	err   error
	calls int
}

func (f *fakeFetcher) FetchWith(context.Context, string, fetch.Options) (*fetch.Result, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &fetch.Result{Body: f.body, Status: 200}, nil
}

type fakeNotifier struct{ texts []string }

func (n *fakeNotifier) SendText(_ context.Context, _, text string) error {
	n.texts = append(n.texts, text)
	return nil
}

func setup(t *testing.T) (*Extractor, *repo.Repo, *pgxpool.Pool, int64, *fakeFetcher, *fakeNotifier) {
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
	var listingID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO listings (zonaprop_id, canonical_url, features)
		 VALUES ('aaa', 'https://www.zonaprop.com.ar/p/aaa.html', '{}') RETURNING id`).Scan(&listingID); err != nil {
		t.Fatal(err)
	}

	r := repo.New(pool)
	fetcher := &fakeFetcher{}
	notifier := &fakeNotifier{}
	e := &Extractor{Repo: r, Fetcher: fetcher, Notifier: notifier,
		Logger: log.New(io.Discard, "", 0), Now: time.Now}
	return e, r, pool, listingID, fetcher, notifier
}

func TestOnLikeSendsThePhoneFromTheDetailPage(t *testing.T) {
	e, _, _, listingID, fetcher, notifier := setup(t)
	fetcher.body = realDetail(t)

	if err := e.OnLike(context.Background(), 1, 1, listingID); err != nil {
		t.Fatal(err)
	}
	if len(notifier.texts) != 1 {
		t.Fatalf("texts = %v, want the phone", notifier.texts)
	}
	if !contains(notifier.texts[0], "5491100000000") {
		t.Errorf("message = %q", notifier.texts[0])
	}
}

// "Not found" is the normal path: the link was already on the card, so nothing
// more is sent and no error is reported.
func TestOnLikeWithoutAPhoneSaysNothing(t *testing.T) {
	e, _, _, listingID, fetcher, notifier := setup(t)
	fetcher.body = []byte(`<html><script type="application/ld+json">{"@type":"Apartment"}</script></html>`)

	if err := e.OnLike(context.Background(), 1, 1, listingID); err != nil {
		t.Fatal(err)
	}
	if len(notifier.texts) != 0 {
		t.Errorf("nothing should be sent, got %v", notifier.texts)
	}
	// The empty result is cached so a future thumbs-up does not spend another request.
	var cached int
	if err := e.Repo.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM listing_contacts WHERE listing_id = $1`, listingID).Scan(&cached); err != nil {
		t.Fatal(err)
	}
	if cached != 1 {
		t.Errorf("expected the empty result to be cached, rows = %d", cached)
	}
}

func TestOnLikeUsesTheCacheInsteadOfRefetching(t *testing.T) {
	e, _, _, listingID, fetcher, _ := setup(t)
	fetcher.body = realDetail(t)

	for i := 0; i < 3; i++ {
		if err := e.OnLike(context.Background(), 1, 1, listingID); err != nil {
			t.Fatal(err)
		}
	}
	if fetcher.calls != 1 {
		t.Errorf("fetched %d times, want 1: the contact is cached", fetcher.calls)
	}
}

// A challenge on the detail page must not break the like: the rating is already
// recorded and the link is already on the card.
func TestOnLikeSurvivesADetailFetchFailure(t *testing.T) {
	e, _, _, listingID, fetcher, notifier := setup(t)
	fetcher.err = &fetch.Error{Kind: fetch.KindBlocked, Mode: "flaresolverr", Err: errors.New("challenge")}

	if err := e.OnLike(context.Background(), 1, 1, listingID); err != nil {
		t.Fatalf("a challenge must not surface as an error: %v", err)
	}
	if len(notifier.texts) != 0 {
		t.Errorf("nothing should be sent, got %v", notifier.texts)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
