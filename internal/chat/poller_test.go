package chat

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"zonapropbot/internal/db"
	"zonapropbot/internal/dbtest"
	"zonapropbot/internal/logging"
	"zonapropbot/internal/repo"
	"zonapropbot/internal/telegram"
)

// batchAPI models Telegram's real behaviour: an update is re-delivered on every
// getUpdates until the consumer advances the offset past it.
type batchAPI struct {
	mu        sync.Mutex
	pending   []telegram.Update
	offsets   []int64
	answerErr error
}

func (a *batchAPI) GetUpdates(ctx context.Context, offset int64) ([]telegram.Update, error) {
	a.mu.Lock()
	a.offsets = append(a.offsets, offset)
	var ready []telegram.Update
	for _, u := range a.pending {
		if u.UpdateID >= offset {
			ready = append(ready, u)
		}
	}
	a.mu.Unlock()

	if len(ready) == 0 {
		// Block like a real long poll until the test cancels.
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return ready, nil
}

func (a *batchAPI) AnswerCallbackQuery(context.Context, string, string) error {
	return a.answerErr
}

func (a *batchAPI) ClearRatingKeyboard(context.Context, string, int64) error { return nil }

func (a *batchAPI) EditMessageCaption(context.Context, string, int64, string) error { return nil }
func (a *batchAPI) SendText(context.Context, string, string) error                  { return nil }

func newPollerWithAPI(t *testing.T, api API, holder string) (*Poller, *repo.Repo) {
	t.Helper()
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	r := repo.New(pool)
	return &Poller{Repo: r, API: api, Logger: logging.Discard(), Holder: holder}, r
}

func TestPollerPersistsOffset(t *testing.T) {
	r := repo.New(dbtest.NewPool(t))
	ctx := context.Background()
	if err := db.Migrate(ctx, r.Pool()); err != nil {
		t.Fatal(err)
	}

	api := &batchAPI{pending: []telegram.Update{
		{UpdateID: 10, Message: &telegram.Message{
			From: &telegram.User{ID: 1},
			Chat: &telegram.Chat{ID: 1, Type: "private"},
			Text: "/help",
		}},
	}}
	p := &Poller{Repo: r, API: api, Logger: logging.Discard(), Holder: "test"}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = p.Run(ctx) }()

	waitFor(t, 3*time.Second, func() bool {
		id, err := r.LastUpdateID(context.Background())
		return err == nil && id == 10
	}, "offset was not persisted after handling the update")
}

// A handler that always fails must not wedge the loop forever: after a bounded
// number of attempts the update is dead-lettered and the offset advances past it.
func TestPoisonUpdateIsDeadLettered(t *testing.T) {
	api := &batchAPI{
		pending:   []telegram.Update{{UpdateID: 42, CallbackQuery: callback("u:1", 1, 1, 5)}},
		answerErr: errors.New("telegram is unhappy about this one"),
	}
	p, r := newPollerWithAPI(t, api, "poison-test")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = p.Run(ctx) }()

	waitFor(t, 10*time.Second, func() bool {
		id, err := r.LastUpdateID(context.Background())
		return err == nil && id == 42
	}, "poison update was never dead-lettered")

	api.mu.Lock()
	calls := len(api.offsets)
	api.mu.Unlock()
	if calls < maxAttemptsPerUpdate {
		t.Errorf("getUpdates called %d times, want at least %d attempts", calls, maxAttemptsPerUpdate)
	}
}

// Telegram delivers each update to exactly one caller, so a second instance must
// refuse to start rather than silently consume the live updates.
func TestPollLeaseIsExclusive(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r := repo.New(pool)

	got, err := r.AcquirePollLease(ctx, "production")
	if err != nil || !got {
		t.Fatalf("first acquisition: got=%v err=%v", got, err)
	}
	if got, err := r.AcquirePollLease(ctx, "dev-laptop"); err != nil || got {
		t.Fatalf("second acquisition must fail: got=%v err=%v", got, err)
	}
	if err := r.ReleasePollLease(ctx, "production"); err != nil {
		t.Fatal(err)
	}
	if got, err := r.AcquirePollLease(ctx, "dev-laptop"); err != nil || !got {
		t.Fatalf("after release the lease should be free: got=%v err=%v", got, err)
	}
}

func TestPollerRefusesToStealUpdates(t *testing.T) {
	api := &batchAPI{}
	p, r := newPollerWithAPI(t, api, "second-instance")
	ctx := context.Background()

	if got, err := r.AcquirePollLease(ctx, "first-instance"); err != nil || !got {
		t.Fatalf("setup lease: %v", err)
	}

	err := p.Run(ctx)
	if err == nil {
		t.Fatal("a second poller must refuse to start while another holds the lease")
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal(msg)
}
