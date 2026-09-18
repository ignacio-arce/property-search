package chat

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"zonapropbot/internal/repo"
	"zonapropbot/internal/telegram"
)

// fakeSearchRunner records the on-demand runs without touching the network.
type fakeSearchRunner struct {
	mu      sync.Mutex
	calls   int
	sent    int
	err     error
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (f *fakeSearchRunner) RunForUser(_ context.Context, _, _ int64) (int, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.started != nil {
		f.once.Do(func() { close(f.started) })
	}
	if f.release != nil {
		<-f.release
	}
	return f.sent, f.err
}

func (f *fakeSearchRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

const searchTestURL = "https://www.zonaprop.com.ar/departamentos-venta-casa.html"

func insertUser(t *testing.T, r *repo.Repo, userID, chatID int64, state string, active bool) {
	t.Helper()
	if _, err := r.Pool().Exec(context.Background(),
		`INSERT INTO users (user_id, chat_id, state, active) VALUES ($1, $2, $3, $4)
		 ON CONFLICT (user_id) DO NOTHING`, userID, chatID, state, active); err != nil {
		t.Fatal(err)
	}
}

func addValidSearch(t *testing.T, r *repo.Repo, userID int64) {
	t.Helper()
	ctx := context.Background()
	id, err := r.AddSearchURLChecked(ctx, userID, "casa", searchTestURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Pool().Exec(ctx,
		`UPDATE search_urls SET validation_status = 'valid' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
}

func textContains(texts []string, want string) bool {
	for _, t := range texts {
		if strings.Contains(t, want) {
			return true
		}
	}
	return false
}

// /buscar must acknowledge immediately, run detached, and report the outcome.
func TestBuscarAcksAndReportsResult(t *testing.T) {
	p, api, r, _ := newTestPoller(t)
	insertUser(t, r, 7, 7, "ready", true)
	addValidSearch(t, r, 7)

	p.Search = &fakeSearchRunner{sent: 3}
	if err := p.search(context.Background(), 7, 7, "7"); err != nil {
		t.Fatal(err)
	}

	if !textContains(api.texts, "Buscando ahora") {
		t.Errorf("no acknowledgement sent: %v", api.texts)
	}
	waitFor(t, 3*time.Second, func() bool {
		return textContains(api.texts, "3 publicaciones nuevas")
	}, "the result was not reported")
}

// The handler must return while the run is still in flight, so a slow digest does
// not stall the poll loop for every other user.
func TestBuscarDoesNotBlockThePoller(t *testing.T) {
	p, api, r, _ := newTestPoller(t)
	insertUser(t, r, 7, 7, "ready", true)
	addValidSearch(t, r, 7)

	started := make(chan struct{})
	release := make(chan struct{})
	p.Search = &fakeSearchRunner{sent: 1, started: started, release: release}

	if err := p.search(context.Background(), 7, 7, "7"); err != nil {
		t.Fatal(err)
	}

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("the run never started")
	}
	if textContains(api.texts, "1 publicación nueva") {
		t.Fatal("the result was reported before the run finished")
	}

	close(release)
	waitFor(t, 3*time.Second, func() bool {
		return textContains(api.texts, "1 publicación nueva")
	}, "the result was not reported after release")
}

// A paused user is told to resume with /start and no fetch is spent.
func TestBuscarRejectsPausedUser(t *testing.T) {
	p, api, r, _ := newTestPoller(t)
	insertUser(t, r, 7, 7, "stopped", true)
	addValidSearch(t, r, 7)

	runner := &fakeSearchRunner{}
	p.Search = runner
	if err := p.search(context.Background(), 7, 7, "7"); err != nil {
		t.Fatal(err)
	}
	if runner.callCount() != 0 {
		t.Errorf("a paused user triggered %d runs", runner.callCount())
	}
	if !textContains(api.texts, "pausadas") {
		t.Errorf("the user was not told they are paused: %v", api.texts)
	}
}

// The operator's switch is not bypassed by a user command.
func TestBuscarRejectsInactiveUser(t *testing.T) {
	p, api, r, _ := newTestPoller(t)
	insertUser(t, r, 7, 7, "ready", false)
	addValidSearch(t, r, 7)

	runner := &fakeSearchRunner{}
	p.Search = runner
	if err := p.search(context.Background(), 7, 7, "7"); err != nil {
		t.Fatal(err)
	}
	if runner.callCount() != 0 {
		t.Errorf("an inactive user triggered %d runs", runner.callCount())
	}
	if !textContains(api.texts, "habilité") {
		t.Errorf("the user was not told they are not enabled: %v", api.texts)
	}
}

// With no validated search there is nothing to run.
func TestBuscarRequiresAValidSearch(t *testing.T) {
	p, api, r, _ := newTestPoller(t)
	insertUser(t, r, 7, 7, "ready", true)

	runner := &fakeSearchRunner{}
	p.Search = runner
	if err := p.search(context.Background(), 7, 7, "7"); err != nil {
		t.Fatal(err)
	}
	if runner.callCount() != 0 {
		t.Errorf("a user without valid searches triggered %d runs", runner.callCount())
	}
	if !textContains(api.texts, "búsquedas validadas") {
		t.Errorf("the user was not told there is nothing to search: %v", api.texts)
	}
}

// An unknown user is guided to onboarding instead of erroring.
func TestBuscarGuidesUnknownUserToStart(t *testing.T) {
	p, api, _, _ := newTestPoller(t)
	runner := &fakeSearchRunner{}
	p.Search = runner

	if err := p.search(context.Background(), 7, 7, "7"); err != nil {
		t.Fatal(err)
	}
	if runner.callCount() != 0 {
		t.Errorf("an unknown user triggered %d runs", runner.callCount())
	}
	if !textContains(api.texts, "/start") {
		t.Errorf("the user was not guided to /start: %v", api.texts)
	}
}

// The cooldown protects the shared fetch gate: a second run inside the window is
// refused and the remaining time is reported.
func TestBuscarIsRateLimited(t *testing.T) {
	p, api, r, _ := newTestPoller(t)
	insertUser(t, r, 7, 7, "ready", true)
	addValidSearch(t, r, 7)

	started := make(chan struct{})
	release := make(chan struct{})
	runner := &fakeSearchRunner{started: started, release: release}
	p.Search = runner

	if err := p.search(context.Background(), 7, 7, "7"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("the first run never started")
	}

	if err := p.search(context.Background(), 7, 7, "7"); err != nil {
		t.Fatal(err)
	}
	if runner.callCount() != 1 {
		t.Errorf("the second /buscar triggered another run: calls=%d", runner.callCount())
	}
	if !textContains(api.texts, "Recién corrí") {
		t.Errorf("the cooldown was not reported: %v", api.texts)
	}

	close(release)
}

// A failed run must be reported as such, not as "no news".
func TestBuscarReportsFailure(t *testing.T) {
	p, api, r, _ := newTestPoller(t)
	insertUser(t, r, 7, 7, "ready", true)
	addValidSearch(t, r, 7)

	p.Search = &fakeSearchRunner{err: context.DeadlineExceeded}
	if err := p.search(context.Background(), 7, 7, "7"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool {
		return textContains(api.texts, "No pude completar")
	}, "the failure was not reported")
}

// The command is reachable from the message dispatcher, not just from the handler.
func TestBuscarIsDispatched(t *testing.T) {
	p, api, r, _ := newTestPoller(t)
	insertUser(t, r, 7, 7, "ready", true)
	addValidSearch(t, r, 7)
	p.Search = &fakeSearchRunner{sent: 0}

	msg := &telegram.Message{
		MessageID: 1,
		From:      &telegram.User{ID: 7},
		Chat:      &telegram.Chat{ID: 7, Type: "private"},
		Text:      "/buscar",
	}
	if err := p.handleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool {
		return textContains(api.texts, "No hay publicaciones nuevas")
	}, "the dispatched command did not report a result")
}

func TestHelpMentionsBuscar(t *testing.T) {
	if !strings.Contains(helpText(), "/buscar") {
		t.Error("/help does not mention /buscar")
	}
}
