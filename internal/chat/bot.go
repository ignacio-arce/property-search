// Package chat turns Telegram updates into actions: ratings from inline buttons
// and command replies.
//
// The polling loop is the foundation of everything interactive, and it has three
// properties that are cheap to add now and painful to retrofit: the offset is
// persisted (a restart replays rather than drops), a poison update cannot wedge
// the loop forever, and a poller lease stops a development instance from stealing
// production's updates.
package chat

import (
	"context"

	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"zonapropbot/internal/repo"
	"zonapropbot/internal/score"
	"zonapropbot/internal/telegram"
)

// LateTapWindow is how long after delivery a rating is still accepted. Beyond it
// the listing's features have likely changed and the label would be noise.
const LateTapWindow = 14 * 24 * time.Hour

// maxAttemptsPerUpdate bounds retries of a single update. A handler that always
// fails would otherwise be re-fetched forever, blocking every other user's
// updates until Telegram's 24h retention drops them silently.
const maxAttemptsPerUpdate = 3

// API is the Telegram surface the bot needs. telegram.Notifier satisfies it.
type API interface {
	GetUpdates(ctx context.Context, offset int64) ([]telegram.Update, error)
	AnswerCallbackQuery(ctx context.Context, callbackID, text string) error
	ClearRatingKeyboard(ctx context.Context, chatID string, messageID int64) error
	SendText(ctx context.Context, chatID, text string) error
}

// ContactResolver resolves the advertiser's phone for a liked listing. It is
// optional: without it, a thumbs-up still records the rating and the link was
// already on the card.
type ContactResolver interface {
	OnLike(ctx context.Context, userID, chatID, listingID int64) error
}

// Poller consumes updates and dispatches them.
type Poller struct {
	Repo     *repo.Repo
	API      API
	Logger   *log.Logger
	Contacts ContactResolver
	// Holder identifies this process for the poll lease.
	Holder string
	// Now is overridable so tests can exercise the late-tap window.
	Now func() time.Time
}

func (p *Poller) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Run polls until the context is cancelled. It acquires the poll lease first: if
// another instance holds it, this one refuses to start rather than silently
// consuming the live updates.
func (p *Poller) Run(ctx context.Context) error {
	acquired, err := p.Repo.AcquirePollLease(ctx, p.Holder)
	if err != nil {
		return err
	}
	if !acquired {
		return fmt.Errorf("another instance holds the Telegram poll lease (holder %q): refusing to steal updates", p.Holder)
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := p.Repo.ReleasePollLease(releaseCtx, p.Holder); err != nil {
			p.logf("chat: releasing poll lease: %v", err)
		}
	}()

	offset, err := p.Repo.LastUpdateID(ctx)
	if err != nil {
		return err
	}
	p.logf("chat: polling from offset %d", offset)

	failures := 0
	var failingID int64

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}

		updates, err := p.API.GetUpdates(ctx, offset+1)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			p.logf("chat: getUpdates: %v", err)
			if err := sleep(ctx, 3*time.Second); err != nil {
				return nil
			}
			continue
		}

		needsRetry := false
		for _, u := range updates {
			if err := p.handleUpdate(ctx, u); err != nil {
				if u.UpdateID == failingID {
					failures++
				} else {
					failingID, failures = u.UpdateID, 1
				}
				if failures < maxAttemptsPerUpdate {
					// Stop the batch instead of skipping ahead: processing later
					// updates would reorder them, and this one is re-delivered by the
					// next poll.
					p.logf("chat: update %d failed (attempt %d/%d): %v", u.UpdateID, failures, maxAttemptsPerUpdate, err)
					needsRetry = true
					break
				}
				// Dead-letter: a handler that always fails must not block every other
				// user's updates until Telegram's 24h retention drops them silently.
				p.logf("chat: DEAD-LETTER update %d after %d attempts: %v", u.UpdateID, failures, err)
			}

			// Persisted after the handler, so a crash replays instead of dropping.
			if err := p.Repo.AdvanceUpdateID(ctx, u.UpdateID); err != nil {
				return err
			}
			offset = u.UpdateID
			failures, failingID = 0, 0
		}

		if needsRetry {
			if err := sleep(ctx, time.Second); err != nil {
				return nil
			}
		}
	}
}

func (p *Poller) handleUpdate(ctx context.Context, u telegram.Update) error {
	switch {
	case u.CallbackQuery != nil:
		return p.handleCallback(ctx, u.CallbackQuery)
	case u.Message != nil:
		return p.handleMessage(ctx, u.Message)
	default:
		return nil
	}
}

// handleCallback processes a 👍/👎 press.
func (p *Poller) handleCallback(ctx context.Context, cq *telegram.CallbackQuery) error {
	if cq.From == nil || cq.Message == nil || cq.Message.Chat == nil {
		return p.API.AnswerCallbackQuery(ctx, cq.ID, "")
	}

	label, ok := parseCallbackData(cq.Data)
	if !ok {
		// Always answer: an unanswered callback shows a spinner then an error.
		return p.API.AnswerCallbackQuery(ctx, cq.ID, "No entendí ese botón")
	}

	userID := cq.From.ID
	chatID := strconv.FormatInt(cq.Message.Chat.ID, 10)
	listingID, err := strconv.ParseInt(strings.TrimPrefix(strings.TrimPrefix(cq.Data, "u:"), "d:"), 10, 64)
	if err != nil {
		return p.API.AnswerCallbackQuery(ctx, cq.ID, "No entendí ese botón")
	}

	state, err := p.Repo.RatingState(ctx, userID, listingID)
	if err != nil {
		return err
	}
	if !state.Delivered {
		return p.API.AnswerCallbackQuery(ctx, cq.ID, "Esa publicación no es tuya")
	}
	if state.HasRating {
		// First tap wins: a second tap is acknowledged but changes nothing, so it
		// cannot re-trigger side effects like re-fetching the contact.
		return p.API.AnswerCallbackQuery(ctx, cq.ID, "Ya la calificaste")
	}
	if p.now().Sub(state.SentAt) > LateTapWindow {
		if err := p.API.AnswerCallbackQuery(ctx, cq.ID, "Esa tarjeta expiró"); err != nil {
			return err
		}
		return p.API.ClearRatingKeyboard(ctx, chatID, cq.Message.MessageID)
	}

	if err := p.Repo.RecordRating(ctx, userID, listingID, label); err != nil {
		return err
	}
	// Answer before doing any network work: the detail fetch can take seconds, and
	// the user's client gives up long before that.
	if err := p.API.AnswerCallbackQuery(ctx, cq.ID, ratingAcknowledgement(label)); err != nil {
		return err
	}
	if err := p.API.ClearRatingKeyboard(ctx, chatID, cq.Message.MessageID); err != nil {
		return err
	}

	if label == 1 && p.Contacts != nil {
		// Run it detached so a slow detail page cannot stall the update loop for
		// every other user. The context is kept (not cancelled) because the fetch
		// should finish even if this update's handling returns.
		chatIDNum := cq.Message.Chat.ID
		go func() {
			detached := context.WithoutCancel(ctx)
			if err := p.Contacts.OnLike(detached, userID, chatIDNum, listingID); err != nil {
				p.logf("chat: contact for listing %d: %v", listingID, err)
			}
		}()
	}
	return nil
}

// parseCallbackData reads "u:<id>" (like) and "d:<id>" (dislike).
func parseCallbackData(data string) (int, bool) {
	switch {
	case strings.HasPrefix(data, "u:"):
		return 1, true
	case strings.HasPrefix(data, "d:"):
		return 0, true
	default:
		return 0, false
	}
}

func ratingAcknowledgement(label int) string {
	if label == 1 {
		return "Guardado 👍"
	}
	return "Guardado 👎"
}

// handleMessage answers commands. Onboarding commands arrive in V5; here the bot
// only reports what it knows.
func (p *Poller) handleMessage(ctx context.Context, msg *telegram.Message) error {
	if msg.From == nil || msg.Chat == nil {
		return nil
	}
	// Group chats share one chat id between several people, which would merge
	// their ratings, deliveries and model. Only private chats are served.
	if msg.Chat.Type != "" && msg.Chat.Type != "private" {
		return p.API.SendText(ctx, strconv.FormatInt(msg.Chat.ID, 10),
			"Solo funciono en chat privado: en un grupo se mezclarían los gustos de todos.")
	}

	chatID := strconv.FormatInt(msg.Chat.ID, 10)
	command := strings.Fields(strings.TrimSpace(msg.Text))
	if len(command) == 0 {
		return nil
	}

	switch command[0] {
	case "/start":
		return p.startOnboarding(ctx, msg.From.ID, msg.Chat.ID)
	case "/help":
		return p.API.SendText(ctx, chatID, helpText())
	case "/model":
		return p.handleModel(ctx, msg.From.ID, chatID)
	case "/list":
		return p.listSearches(ctx, msg.From.ID, chatID)
	case "/addurl":
		return p.addURL(ctx, msg.From.ID, msg.Chat.ID, chatID)
	case "/rmurl":
		label := ""
		if len(command) > 1 {
			label = strings.Join(command[1:], " ")
		}
		return p.removeURL(ctx, msg.From.ID, chatID, label)
	case "/stop":
		return p.setStopped(ctx, msg.From.ID, chatID, true)
	case "/borrardatos":
		return p.deleteEverything(ctx, msg.From.ID, chatID)
	default:
		if strings.HasPrefix(command[0], "/") {
			return p.API.SendText(ctx, chatID, "No conozco ese comando. Probá con /help.")
		}
		// A plain message is part of the onboarding conversation.
		return p.handleConversationInput(ctx, msg.From.ID, msg.Chat.ID, msg.Text)
	}
}

func (p *Poller) handleModel(ctx context.Context, userID int64, chatID string) error {
	version, err := p.Repo.ModelVersion(ctx, userID)
	if err != nil {
		return err
	}
	likes, dislikes, err := p.Repo.RatingCounts(ctx, userID)
	if err != nil {
		return err
	}
	agreement, samples, hasAgreement, err := score.Agreement(ctx, p.Repo, userID, p.now())
	if err != nil {
		return err
	}

	text := score.SummaryText(version, likes, dislikes, agreement, samples, hasAgreement)
	if version > 0 {
		weights, err := score.Load(ctx, p.Repo, userID)
		if err != nil {
			return err
		}
		if buckets := score.BucketSummary(weights, maxBucketsShown); buckets != "" {
			text += "\n\nLo que más aprendió:\n" + buckets
		}
	}
	return p.API.SendText(ctx, chatID, text)
}

// maxBucketsShown keeps /model readable; the full list lives in the database.
const maxBucketsShown = 8

func helpText() string {
	return strings.Join([]string{
		"🤖 Buscador de propiedades con calificación",
		"",
		"Te mando las publicaciones nuevas de tus búsquedas y las calificás con 👍/👎.",
		"El link va siempre en la tarjeta; el 👍 además busca el teléfono del aviso.",
		"",
		"Comandos:",
		"/start — darte de alta o reanudar las notificaciones",
		"/addurl — sumar otra búsqueda",
		"/rmurl <nombre> — borrar una búsqueda",
		"/list — ver tus búsquedas y su estado",
		"/model — qué aprendió de tus calificaciones",
		"/stop — pausar las notificaciones",
		"/borrardatos — borrar todo lo tuyo",
		"/help — este mensaje",
	}, "\n")
}

func (p *Poller) logf(format string, args ...any) {
	if p.Logger != nil {
		p.Logger.Printf(format, args...)
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
