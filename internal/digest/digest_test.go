package digest_test

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"testing"

	"zonapropbot/internal/db"
	"zonapropbot/internal/dbtest"
	"zonapropbot/internal/digest"
	"zonapropbot/internal/fetch"
	"zonapropbot/internal/model"
	"zonapropbot/internal/repo"
)

const (
	searchA = "https://www.zonaprop.com.ar/departamentos-venta-a.html"
	searchB = "https://www.zonaprop.com.ar/departamentos-venta-b.html"
)

type fakeFetcher struct {
	pages map[string][]byte
	err   map[string]error
	calls int
}

func (f *fakeFetcher) Fetch(_ context.Context, u string) (*fetch.Result, error) {
	f.calls++
	if err := f.err[u]; err != nil {
		return nil, err
	}
	body, ok := f.pages[u]
	if !ok {
		return nil, fmt.Errorf("fakeFetcher: no page for %s", u)
	}
	return &fetch.Result{Body: body, Mode: "fake", Status: 200}, nil
}

type recordingNotifier struct {
	sent []model.Listing
	fail map[string]bool
}

func (n *recordingNotifier) Notify(_ context.Context, _ string, _ int64, l model.Listing) error {
	if n.fail[l.ZonapropID] {
		return fmt.Errorf("notify %s failed", l.ZonapropID)
	}
	n.sent = append(n.sent, l)
	return nil
}

func (n *recordingNotifier) ids() []string {
	var out []string
	for _, l := range n.sent {
		out = append(out, l.ZonapropID)
	}
	return out
}

// cardHTML renders a minimal card with the attributes the parser requires.
func cardHTML(id string) string {
	return fmt.Sprintf(
		`<div data-to-posting="/p/x-%s.html" data-id="%s" data-posting-type="PROPERTY">`+
			`<h2 data-qa="POSTING_CARD_PRICE">USD 100.000</h2>`+
			`<h4 data-qa="POSTING_CARD_LOCATION">Florida, Vicente López</h4></div>`, id, id)
}

func page(ids ...string) []byte {
	var b strings.Builder
	b.WriteString("<html><body>")
	for _, id := range ids {
		b.WriteString(cardHTML(id))
	}
	b.WriteString("</body></html>")
	return []byte(b.String())
}

// fixture builds a migrated database with one active user and a valid search.
func fixture(t *testing.T, userID, chatID int64, searches ...string) *repo.Repo {
	t.Helper()
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	r := repo.New(pool)

	// active is the operator's switch; SetActive does not exist yet (V5.4), so the
	// test flips it directly.
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (user_id, chat_id, state, active) VALUES ($1, $2, 'ready', true)`,
		userID, chatID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	for i, s := range searches {
		id, err := r.AddSearchURL(ctx, userID, fmt.Sprintf("search-%d", i), s)
		if err != nil {
			t.Fatalf("add search: %v", err)
		}
		if _, err := pool.Exec(ctx,
			`UPDATE search_urls SET validation_status = 'valid' WHERE id = $1`, id); err != nil {
			t.Fatalf("validate search: %v", err)
		}
	}
	return r
}

func newRunner(r *repo.Repo, f *fakeFetcher, n *recordingNotifier) *digest.Runner {
	return &digest.Runner{Repo: r, Fetcher: f, Notifier: n, Logger: log.New(io.Discard, "", 0)}
}

// The first index of a search is baselined, not sent: replaying the whole current
// inventory on subscribe is the noise the bot exists to remove.
func TestFirstIndexBaselinesAndLaterRunsSendOnlyNew(t *testing.T) {
	r := fixture(t, 1, 1, searchA)
	f := &fakeFetcher{pages: map[string][]byte{searchA: page("aaa", "bbb")}}
	n := &recordingNotifier{}
	runner := newRunner(r, f, n)
	ctx := context.Background()

	sent, err := runner.RunForUser(ctx, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if sent != 0 {
		t.Errorf("first run sent %d (%v), want 0: the current inventory is baselined", sent, n.ids())
	}

	// A genuinely new listing appears.
	f.pages[searchA] = page("aaa", "bbb", "ccc")
	sent, err = runner.RunForUser(ctx, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if sent != 1 || len(n.ids()) != 1 || n.ids()[0] != "ccc" {
		t.Errorf("second run sent %v, want only ccc", n.ids())
	}
}

func TestRepeatedRunIsSilent(t *testing.T) {
	r := fixture(t, 1, 1, searchA)
	f := &fakeFetcher{pages: map[string][]byte{searchA: page("aaa")}}
	n := &recordingNotifier{}
	runner := newRunner(r, f, n)
	ctx := context.Background()

	if _, err := runner.RunForUser(ctx, 1, 1); err != nil {
		t.Fatal(err)
	}
	// Re-running with the same page must not re-send the baselined listing, which
	// is what makes a process restart safe.
	for i := 0; i < 2; i++ {
		sent, err := runner.RunForUser(ctx, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		if sent != 0 {
			t.Fatalf("run %d sent %d, want 0", i+2, sent)
		}
	}
	if len(n.sent) != 0 {
		t.Errorf("nothing should have been sent, got %v", n.ids())
	}
}

// A failed search must not abort the cycle: the other searches still get indexed.
func TestFailingSearchDoesNotAbortTheCycle(t *testing.T) {
	r := fixture(t, 1, 1, searchA, searchB)
	f := &fakeFetcher{
		pages: map[string][]byte{searchB: page("bbb")},
		err:   map[string]error{searchA: fmt.Errorf("challenge")},
	}
	n := &recordingNotifier{}
	runner := newRunner(r, f, n)

	if _, err := runner.RunForUser(context.Background(), 1, 1); err != nil {
		t.Fatalf("a failing search must not fail the cycle: %v", err)
	}

	count, err := r.CountListingSources(context.Background(), searchURLIDOf(t, r, 1, searchB))
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Error("the healthy search was not indexed")
	}
}

// Candidates are scoped by listing_sources: a listing found only through another
// user's search must never reach this user.
func TestCandidatesAreScopedToTheUsersOwnSearches(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r := repo.New(pool)

	for _, u := range []struct {
		id, chat int64
		search   string
	}{
		{1, 1, searchA},
		{2, 2, searchB},
	} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO users (user_id, chat_id, state, active) VALUES ($1, $2, 'ready', true)`,
			u.id, u.chat); err != nil {
			t.Fatal(err)
		}
		sid, err := r.AddSearchURL(ctx, u.id, "s", u.search)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx,
			`UPDATE search_urls SET validation_status = 'valid' WHERE id = $1`, sid); err != nil {
			t.Fatal(err)
		}
	}

	f := &fakeFetcher{pages: map[string][]byte{
		searchA: page("aaa"),
		searchB: page("bbb"),
	}}
	n := &recordingNotifier{}
	runner := newRunner(r, f, n)

	// Baseline both.
	if _, err := runner.RunAll(ctx); err != nil {
		t.Fatal(err)
	}
	// Both get a new listing.
	f.pages[searchA] = page("aaa", "newA")
	f.pages[searchB] = page("bbb", "newB")
	n.sent = nil
	if _, err := runner.RunAll(ctx); err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(n.ids(), ","); got != "newA,newB" {
		t.Fatalf("sent %v, want newA and newB once each", n.ids())
	}

	// Each user must see only their own.
	for _, u := range []int64{1, 2} {
		cands, err := r.Candidates(ctx, u, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cands {
			other := "newA"
			if u == 1 {
				other = "newB"
			}
			if c.Listing.ZonapropID == other {
				t.Errorf("user %d sees %s, which belongs to the other user's search", u, other)
			}
		}
	}
}

// Inactive users must not be processed at all.
func TestInactiveUsersAreSkipped(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r := repo.New(pool)
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (user_id, chat_id, state, active) VALUES (1, 1, 'ready', false)`); err != nil {
		t.Fatal(err)
	}
	sid, err := r.AddSearchURL(ctx, 1, "s", searchA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE search_urls SET validation_status = 'valid' WHERE id = $1`, sid); err != nil {
		t.Fatal(err)
	}

	f := &fakeFetcher{pages: map[string][]byte{searchA: page("aaa")}}
	n := &recordingNotifier{}
	runner := newRunner(r, f, n)

	sent, err := runner.RunAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sent != 0 || f.calls != 0 {
		t.Errorf("inactive user must be skipped entirely: sent=%d fetches=%d", sent, f.calls)
	}
}

func searchURLIDOf(t *testing.T, r *repo.Repo, userID int64, url string) int64 {
	t.Helper()
	urls, err := r.ListSearchURLs(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range urls {
		if u.URL == url {
			return u.ID
		}
	}
	t.Fatalf("search %s not found for user %d", url, userID)
	return 0
}
