package chat

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"zonapropbot/internal/db"
	"zonapropbot/internal/dbtest"
	"zonapropbot/internal/repo"
	"zonapropbot/internal/telegram"
)

type fakeAPI struct {
	answers  []string
	cleared  []int64
	texts    []string
	clearErr error
}

func (f *fakeAPI) GetUpdates(context.Context, int64) ([]telegram.Update, error) { return nil, nil }

func (f *fakeAPI) AnswerCallbackQuery(_ context.Context, _, text string) error {
	f.answers = append(f.answers, text)
	return nil
}

func (f *fakeAPI) ClearRatingKeyboard(_ context.Context, _ string, messageID int64) error {
	if f.clearErr != nil {
		return f.clearErr
	}
	f.cleared = append(f.cleared, messageID)
	return nil
}

func (f *fakeAPI) SendText(_ context.Context, _, text string) error {
	f.texts = append(f.texts, text)
	return nil
}

func newTestPoller(t *testing.T) (*Poller, *fakeAPI, *repo.Repo, *pgxpool.Pool) {
	t.Helper()
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	r := repo.New(pool)
	api := &fakeAPI{}
	return &Poller{Repo: r, API: api, Logger: log.New(io.Discard, "", 0)}, api, r, pool
}

// delivered inserts a user, a listing and a delivery, returning the listing id.
func delivered(t *testing.T, pool *pgxpool.Pool, userID, chatID int64, zonapropID string, sentAt time.Time) int64 {
	t.Helper()
	ctx := context.Background()

	if _, err := pool.Exec(ctx,
		`INSERT INTO users (user_id, chat_id, state, active) VALUES ($1, $2, 'ready', true)
		 ON CONFLICT (user_id) DO NOTHING`, userID, chatID); err != nil {
		t.Fatal(err)
	}
	var listingID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO listings (zonaprop_id, canonical_url, features)
		 VALUES ($1, $2, '{}') RETURNING id`,
		zonapropID, "https://www.zonaprop.com.ar/p/"+zonapropID+".html").Scan(&listingID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO deliveries (user_id, listing_id, message_id, features, status, sent_at)
		 VALUES ($1, $2, 555, '{}', 'sent', $3)`, userID, listingID, sentAt); err != nil {
		t.Fatal(err)
	}
	return listingID
}

func callback(data string, userID, chatID, messageID int64) *telegram.CallbackQuery {
	return &telegram.CallbackQuery{
		ID:      "cb-1",
		From:    &telegram.User{ID: userID},
		Message: &telegram.Message{MessageID: messageID, Chat: &telegram.Chat{ID: chatID, Type: "private"}},
		Data:    data,
	}
}

func TestCallbackRecordsRating(t *testing.T) {
	p, api, _, pool := newTestPoller(t)
	listingID := delivered(t, pool, 7, 7, "aaa", time.Now())
	ctx := context.Background()

	if err := p.handleCallback(ctx, callback(fmt.Sprintf("u:%d", listingID), 7, 7, 555)); err != nil {
		t.Fatal(err)
	}

	if len(api.answers) != 1 || api.answers[0] != "Guardado 👍" {
		t.Errorf("answers = %v", api.answers)
	}
	if len(api.cleared) != 1 || api.cleared[0] != 555 {
		t.Errorf("keyboard not revoked: %v", api.cleared)
	}

	var label int
	if err := pool.QueryRow(ctx,
		`SELECT label FROM ratings WHERE user_id = 7 AND listing_id = $1`, listingID).Scan(&label); err != nil {
		t.Fatalf("rating not stored: %v", err)
	}
	if label != 1 {
		t.Errorf("label = %d, want 1", label)
	}
}

// First tap wins: a second tap is acknowledged but must not re-trigger anything.
func TestSecondTapIsAcknowledgedWithoutSideEffects(t *testing.T) {
	p, api, _, pool := newTestPoller(t)
	listingID := delivered(t, pool, 7, 7, "aaa", time.Now())
	ctx := context.Background()

	if err := p.handleCallback(ctx, callback(fmt.Sprintf("u:%d", listingID), 7, 7, 555)); err != nil {
		t.Fatal(err)
	}
	if err := p.handleCallback(ctx, callback(fmt.Sprintf("d:%d", listingID), 7, 7, 555)); err != nil {
		t.Fatal(err)
	}

	if len(api.answers) != 2 || api.answers[1] != "Ya la calificaste" {
		t.Errorf("answers = %v", api.answers)
	}
	if len(api.cleared) != 1 {
		t.Errorf("keyboard cleared %d times, want only on the first tap", len(api.cleared))
	}

	var label int
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT label, (SELECT count(*) FROM ratings WHERE user_id = 7) FROM ratings WHERE user_id = 7 AND listing_id = $1`,
		listingID).Scan(&label, &count); err != nil {
		t.Fatal(err)
	}
	if label != 1 || count != 1 {
		t.Errorf("a repeat tap changed the rating: label=%d count=%d", label, count)
	}
}

func TestCallbackForUndeliveredListingIsRejected(t *testing.T) {
	p, api, _, pool := newTestPoller(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (user_id, chat_id, state, active) VALUES (7, 7, 'ready', true)`); err != nil {
		t.Fatal(err)
	}

	if err := p.handleCallback(ctx, callback("u:999", 7, 7, 555)); err != nil {
		t.Fatal(err)
	}
	if len(api.answers) != 1 || api.answers[0] != "Esa publicación no es tuya" {
		t.Errorf("answers = %v", api.answers)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ratings`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("a forged callback created %d ratings", count)
	}
}

// Beyond the window the listing's features have likely changed, so the label
// would be noise. The user is told instead of being silently ignored.
func TestLateTapExpires(t *testing.T) {
	p, api, _, pool := newTestPoller(t)
	listingID := delivered(t, pool, 7, 7, "aaa", time.Now().Add(-LateTapWindow-time.Hour))
	ctx := context.Background()

	if err := p.handleCallback(ctx, callback(fmt.Sprintf("u:%d", listingID), 7, 7, 555)); err != nil {
		t.Fatal(err)
	}
	if len(api.answers) != 1 || api.answers[0] != "Esa tarjeta expiró" {
		t.Errorf("answers = %v", api.answers)
	}
	if len(api.cleared) != 1 {
		t.Errorf("expired keyboard must be revoked: %v", api.cleared)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ratings`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Error("an expired tap must not be recorded")
	}
}

func TestUnknownCallbackDataIsAnswered(t *testing.T) {
	p, api, _, _ := newTestPoller(t)
	if err := p.handleCallback(context.Background(), callback("x:1", 7, 7, 555)); err != nil {
		t.Fatal(err)
	}
	if len(api.answers) != 1 || api.answers[0] == "" {
		t.Errorf("every callback must be answered, got %v", api.answers)
	}
}

func TestGroupChatIsRefused(t *testing.T) {
	p, api, _, _ := newTestPoller(t)
	msg := &telegram.Message{
		From: &telegram.User{ID: 7},
		Chat: &telegram.Chat{ID: -100123, Type: "group"},
		Text: "/model",
	}
	if err := p.handleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if len(api.texts) != 1 || !strings.Contains(api.texts[0], "chat privado") {
		t.Errorf("group chat must be refused, got %v", api.texts)
	}
}

func TestModelCommandRepliesWithCounts(t *testing.T) {
	p, api, _, pool := newTestPoller(t)
	listingID := delivered(t, pool, 7, 7, "aaa", time.Now())
	ctx := context.Background()
	if err := p.Repo.RecordRating(ctx, 7, listingID, 1); err != nil {
		t.Fatal(err)
	}

	msg := &telegram.Message{
		From: &telegram.User{ID: 7},
		Chat: &telegram.Chat{ID: 7, Type: "private"},
		Text: "/model",
	}
	if err := p.handleMessage(ctx, msg); err != nil {
		t.Fatal(err)
	}
	if len(api.texts) != 1 || !strings.Contains(api.texts[0], "1 👍") {
		t.Errorf("texts = %v", api.texts)
	}
	if !strings.Contains(api.texts[0], "Modelo") {
		t.Errorf("expected the model report, got %v", api.texts)
	}
}

const testSearchURL = "https://www.zonaprop.com.ar/departamentos-venta-gba-norte-3-ambientes.html"

func message(text string, userID, chatID int64) *telegram.Message {
	return &telegram.Message{
		From: &telegram.User{ID: userID},
		Chat: &telegram.Chat{ID: chatID, Type: "private"},
		Text: text,
	}
}

func TestOnboardingRegistersASearchAndInjectsRecencyOrder(t *testing.T) {
	p, api, r, _ := newTestPoller(t)
	ctx := context.Background()

	if err := p.handleMessage(ctx, message("/start", 7, 7)); err != nil {
		t.Fatal(err)
	}
	if err := p.handleMessage(ctx, message(testSearchURL, 7, 7)); err != nil {
		t.Fatal(err)
	}
	if err := p.handleMessage(ctx, message("San Isidro 3amb", 7, 7)); err != nil {
		t.Fatal(err)
	}

	searches, err := r.ListSearchURLs(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(searches) != 1 {
		t.Fatalf("registered %d searches, want 1", len(searches))
	}
	if searches[0].Label != "San Isidro 3amb" {
		t.Errorf("label = %q", searches[0].Label)
	}
	// The recency order is what makes the newest listings the ones that show up.
	if !strings.Contains(searches[0].URL, "-orden-publicado-descendente") {
		t.Errorf("url = %q, want the recency order injected", searches[0].URL)
	}
	if searches[0].ValidationStatus != "pending" {
		t.Errorf("status = %q, want pending until the deep check runs", searches[0].ValidationStatus)
	}

	user, err := r.GetUser(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if user.State != stateReady {
		t.Errorf("state = %q, want ready", user.State)
	}

	// The confirmation must be honest about activation and coverage.
	last := api.texts[len(api.texts)-1]
	if !strings.Contains(last, "no empiezan hasta que te habilite") {
		t.Errorf("confirmation must mention activation: %q", last)
	}
	if !strings.Contains(last, "primera página") {
		t.Errorf("confirmation must mention the coverage limit: %q", last)
	}
}

func TestOnboardingRejectsNonZonapropURL(t *testing.T) {
	p, _, r, _ := newTestPoller(t)
	ctx := context.Background()
	if err := p.handleMessage(ctx, message("/start", 7, 7)); err != nil {
		t.Fatal(err)
	}
	// A substring host check would accept this one.
	for _, bad := range []string{
		"https://zonaprop.com.ar.attacker.test/x.html",
		"https://notzonaprop.com.ar/x.html",
		"http://www.zonaprop.com.ar/x.html",
	} {
		if err := p.handleMessage(ctx, message(bad, 7, 7)); err != nil {
			t.Fatal(err)
		}
	}
	searches, err := r.ListSearchURLs(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(searches) != 0 {
		t.Errorf("a non-Zonaprop or http URL was accepted: %+v", searches)
	}
}

func TestOnboardingDuplicateLabelReprompts(t *testing.T) {
	p, api, r, _ := newTestPoller(t)
	ctx := context.Background()

	// Two different searches cannot share a label.
	if err := p.handleMessage(ctx, message("/addurl", 7, 7)); err != nil {
		t.Fatal(err)
	}
	if err := p.handleMessage(ctx, message(testSearchURL, 7, 7)); err != nil {
		t.Fatal(err)
	}
	if err := p.handleMessage(ctx, message("repetido", 7, 7)); err != nil {
		t.Fatal(err)
	}

	if err := p.handleMessage(ctx, message("/addurl", 7, 7)); err != nil {
		t.Fatal(err)
	}
	if err := p.handleMessage(ctx, message(testSearchURL+"-otra.html", 7, 7)); err != nil {
		t.Fatal(err)
	}
	if err := p.handleMessage(ctx, message("repetido", 7, 7)); err != nil {
		t.Fatal(err)
	}

	last := api.texts[len(api.texts)-1]
	if !strings.Contains(last, "Ya usaste ese nombre") {
		t.Errorf("a duplicate label must be re-prompted, got %q", last)
	}

	// Still in the label step, so the user can simply answer with another name.
	user, err := r.GetUser(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if user.State != stateAwaitLabel {
		t.Errorf("state = %q, want await_label so the user can retry", user.State)
	}
	if err := p.handleMessage(ctx, message("otro nombre", 7, 7)); err != nil {
		t.Fatal(err)
	}

	searches, err := r.ListSearchURLs(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(searches) != 2 {
		t.Fatalf("got %d searches, want 2", len(searches))
	}
}

// The same search pasted from another page must not register twice: the tracking
// parameters differ but the normalized form is the same.
func TestOnboardingDeduplicatesOnNormalizedURL(t *testing.T) {
	p, _, r, _ := newTestPoller(t)
	ctx := context.Background()

	for _, url := range []string{testSearchURL, testSearchURL + "?n_pg=2&n_pos=9"} {
		if err := p.handleMessage(ctx, message("/addurl", 7, 7)); err != nil {
			t.Fatal(err)
		}
		if err := p.handleMessage(ctx, message(url, 7, 7)); err != nil {
			t.Fatal(err)
		}
		if err := p.handleMessage(ctx, message("misma busqueda", 7, 7)); err != nil {
			t.Fatal(err)
		}
	}

	searches, err := r.ListSearchURLs(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(searches) != 1 {
		t.Errorf("got %d searches, want 1 after normalization dedup: %+v", len(searches), searches)
	}
}

func TestOnboardingEnforcesTheSearchCap(t *testing.T) {
	p, api, r, _ := newTestPoller(t)
	ctx := context.Background()

	for i := 0; i < repo.MaxSearchURLsPerUser+1; i++ {
		if err := p.handleMessage(ctx, message("/addurl", 7, 7)); err != nil {
			t.Fatal(err)
		}
		url := fmt.Sprintf("https://www.zonaprop.com.ar/busqueda-%d.html", i)
		if err := p.handleMessage(ctx, message(url, 7, 7)); err != nil {
			t.Fatal(err)
		}
		if err := p.handleMessage(ctx, message(fmt.Sprintf("busqueda %d", i), 7, 7)); err != nil {
			t.Fatal(err)
		}
	}

	searches, err := r.ListSearchURLs(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(searches) != repo.MaxSearchURLsPerUser {
		t.Fatalf("registered %d searches, want the cap of %d", len(searches), repo.MaxSearchURLsPerUser)
	}
	joined := strings.Join(api.texts, "\n")
	if !strings.Contains(joined, "máximo") {
		t.Errorf("the user should be told about the cap: %v", api.texts)
	}
}

func TestListShowsStatusAndLabels(t *testing.T) {
	p, api, r, pool := newTestPoller(t)
	ctx := context.Background()
	if err := p.handleMessage(ctx, message("/start", 7, 7)); err != nil {
		t.Fatal(err)
	}
	if err := p.handleMessage(ctx, message(testSearchURL, 7, 7)); err != nil {
		t.Fatal(err)
	}
	if err := p.handleMessage(ctx, message("mi busqueda", 7, 7)); err != nil {
		t.Fatal(err)
	}
	searches, _ := r.ListSearchURLs(ctx, 7)
	if _, err := pool.Exec(ctx, `UPDATE search_urls SET validation_status = 'valid' WHERE id = $1`, searches[0].ID); err != nil {
		t.Fatal(err)
	}

	if err := p.handleMessage(ctx, message("/list", 7, 7)); err != nil {
		t.Fatal(err)
	}
	last := api.texts[len(api.texts)-1]
	if !strings.Contains(last, "mi busqueda") || !strings.Contains(last, "vigilando") {
		t.Errorf("/list output = %q", last)
	}
}

func TestStopAndBorrardatos(t *testing.T) {
	p, _, r, _ := newTestPoller(t)
	ctx := context.Background()
	if err := p.handleMessage(ctx, message("/start", 7, 7)); err != nil {
		t.Fatal(err)
	}
	if err := p.handleMessage(ctx, message(testSearchURL, 7, 7)); err != nil {
		t.Fatal(err)
	}
	if err := p.handleMessage(ctx, message("x", 7, 7)); err != nil {
		t.Fatal(err)
	}

	if err := p.handleMessage(ctx, message("/stop", 7, 7)); err != nil {
		t.Fatal(err)
	}
	user, _ := r.GetUser(ctx, 7)
	if user.State != stateStopped {
		t.Errorf("state = %q, want stopped", user.State)
	}

	if err := p.handleMessage(ctx, message("/borrardatos", 7, 7)); err != nil {
		t.Fatal(err)
	}
	after, _ := r.GetUser(ctx, 7)
	if after != nil {
		t.Error("the user should be gone after /borrardatos")
	}
}

func TestRemoveURLNeedsAKnownLabel(t *testing.T) {
	p, api, _, _ := newTestPoller(t)
	ctx := context.Background()
	if err := p.handleMessage(ctx, message("/rmurl noexiste", 7, 7)); err != nil {
		t.Fatal(err)
	}
	last := api.texts[len(api.texts)-1]
	if !strings.Contains(last, "No encontré") {
		t.Errorf("removing an unknown label should say so, got %q", last)
	}
}
