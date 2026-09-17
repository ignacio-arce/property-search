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

func TestModelTextDoesNotPretendBelowThreshold(t *testing.T) {
	low := modelText(3, 1)
	if strings.Contains(low, "suficientes") {
		t.Errorf("below the threshold it must not claim to have learned: %s", low)
	}
	if !strings.Contains(low, "Todavía aprendiendo") {
		t.Errorf("below the threshold it must say so: %s", low)
	}

	high := modelText(40, 10)
	if !strings.Contains(high, "suficientes") {
		t.Errorf("at the threshold it should say the ranking is available: %s", high)
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
}
