package validate

import (
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"zonapropbot/internal/db"
	"zonapropbot/internal/dbtest"
	"zonapropbot/internal/fetch"
	"zonapropbot/internal/repo"
)

const searchURL = "https://www.zonaprop.com.ar/departamentos-venta-x.html"

type fakeFetcher struct {
	res   *fetch.Result
	err   error
	calls int
}

func (f *fakeFetcher) FetchWith(context.Context, string, fetch.Options) (*fetch.Result, error) {
	f.calls++
	return f.res, f.err
}

type fakeNotifier struct{ texts []string }

func (n *fakeNotifier) SendText(_ context.Context, _, text string) error {
	n.texts = append(n.texts, text)
	return nil
}

// fixture creates an active user with one pending search.
func fixture(t *testing.T) (*repo.Repo, *pgxpool.Pool, int64) {
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
	r := repo.New(pool)
	id, err := r.AddSearchURLChecked(ctx, 1, "test", searchURL)
	if err != nil {
		t.Fatal(err)
	}
	return r, pool, id
}

func statusOf(t *testing.T, pool *pgxpool.Pool, id int64) (string, int, *time.Time) {
	t.Helper()
	var (
		status   string
		attempts int
		next     *time.Time
	)
	if err := pool.QueryRow(context.Background(),
		`SELECT validation_status, attempts, next_check_at FROM search_urls WHERE id = $1`, id).
		Scan(&status, &attempts, &next); err != nil {
		t.Fatal(err)
	}
	return status, attempts, next
}

func newValidator(r *repo.Repo, f Fetcher) (*Validator, *fakeNotifier) {
	n := &fakeNotifier{}
	return &Validator{Repo: r, Fetcher: f, Notifier: n, Logger: log.New(io.Discard, "", 0)}, n
}

func TestValidPageBecomesWatched(t *testing.T) {
	r, pool, id := fixture(t)
	body := []byte(`<html><body><div data-to-posting="/p/a.html" data-id="1" data-posting-type="PROPERTY"></div></body></html>`)
	v, notifier := newValidator(r, &fakeFetcher{res: &fetch.Result{Body: body, Status: 200}})

	if _, err := v.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, attempts, next := statusOf(t, pool, id)
	if status != "valid" {
		t.Errorf("status = %q, want valid", status)
	}
	if attempts != 0 || next != nil {
		t.Errorf("a valid URL should have its counters reset, got attempts=%d next=%v", attempts, next)
	}
	if len(notifier.texts) != 1 {
		t.Errorf("the user should be told, got %v", notifier.texts)
	}
}

// A challenge is not a verdict: the URL stays pending and is retried later.
func TestChallengeIsRetriedNotRejected(t *testing.T) {
	r, pool, id := fixture(t)
	v, _ := newValidator(r, &fakeFetcher{err: &fetch.Error{Kind: fetch.KindBlocked, Mode: "flaresolverr",
		Err: errors.New("challenge")}})

	if _, err := v.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, attempts, next := statusOf(t, pool, id)
	if status != "retrying" {
		t.Errorf("status = %q, want retrying", status)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
	if next == nil || !next.After(time.Now()) {
		t.Errorf("next check should be scheduled in the future, got %v", next)
	}
}

// A network failure is an outage, not a verdict on the URL either.
func TestTransportFailureIsRetried(t *testing.T) {
	r, pool, id := fixture(t)
	v, _ := newValidator(r, &fakeFetcher{err: &fetch.Error{Kind: fetch.KindTransport, Mode: "tls",
		Err: errors.New("connection refused")}})

	if _, err := v.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := statusOf(t, pool, id); status != "retrying" {
		t.Errorf("status = %q, want retrying", status)
	}
}

// Zero cards on a real page is a legitimate narrow search, not a failure.
func TestEmptyPageIsValidButEmpty(t *testing.T) {
	r, pool, id := fixture(t)
	v, notifier := newValidator(r, &fakeFetcher{res: &fetch.Result{
		Body: []byte(`<html><body><div class="postingsList-module">nada</div></body></html>`), Status: 200}})

	if _, err := v.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, _, next := statusOf(t, pool, id)
	if status != "valid_empty" {
		t.Errorf("status = %q, want valid_empty", status)
	}
	if next != nil {
		t.Error("a valid-but-empty search should not be scheduled for another check")
	}
	if len(notifier.texts) != 1 {
		t.Errorf("the user should be told the search is valid, got %v", notifier.texts)
	}
}

// After the attempt budget the search is declared invalid and the user is told,
// instead of being retried forever.
func TestGivesUpAfterMaxAttempts(t *testing.T) {
	r, pool, id := fixture(t)
	v, notifier := newValidator(r, &fakeFetcher{err: &fetch.Error{Kind: fetch.KindBlocked, Mode: "flaresolverr",
		Err: errors.New("challenge")}})
	v.MaxAttempts = 2

	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := v.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		// Make it due again immediately rather than waiting out the backoff.
		if _, err := pool.Exec(ctx, `UPDATE search_urls SET next_check_at = now() - interval '1 minute' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}

	if status, _, _ := statusOf(t, pool, id); status != "invalid" {
		t.Errorf("status = %q, want invalid after the attempt budget", status)
	}
	last := notifier.texts[len(notifier.texts)-1]
	if !strings.Contains(last, "No pude validar") {
		t.Errorf("the user should be told, got %q", last)
	}
}

// A URL that is not due must not be fetched: the backoff is what protects the IP.
func TestNotDueIsNotFetched(t *testing.T) {
	r, pool, id := fixture(t)
	if _, err := pool.Exec(context.Background(),
		`UPDATE search_urls SET next_check_at = now() + interval '1 hour' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	fetcher := &fakeFetcher{res: &fetch.Result{Status: 200}}
	v, _ := newValidator(r, fetcher)

	if _, err := v.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fetcher.calls != 0 {
		t.Errorf("a search whose backoff has not elapsed was fetched %d times", fetcher.calls)
	}
}
