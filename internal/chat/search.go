package chat

import (
	"context"
	"fmt"
	"time"
)

// SearchRunner runs one user's digest on demand. *digest.Runner satisfies it.
type SearchRunner interface {
	RunForUser(ctx context.Context, userID, chatID int64) (int, error)
}

const (
	// searchCooldown is the minimum gap between manual runs of the same user. It
	// protects the shared fetch gate (1 req/min), not the user.
	searchCooldown = time.Hour
	// searchRunTimeout bounds a detached manual run so it cannot leak forever.
	searchRunTimeout = 15 * time.Minute
)

// search handles /buscar: a digest run triggered by the user instead of waiting
// for the daily schedule. It stays quiet about the search itself and reports the
// outcome when the detached run finishes.
func (p *Poller) search(ctx context.Context, userID, chatID int64, chat string) error {
	user, err := p.Repo.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if user == nil {
		return p.API.SendText(ctx, chat, "Todavía no arrancamos. Mandá /start y te guío.")
	}
	if user.State == stateStopped {
		return p.API.SendText(ctx, chat,
			"Tenés las notificaciones pausadas. Mandá /start para reanudarlas y volvé a pedirme.")
	}
	// The operator's switch is not bypassed by a user command: if the account is
	// not enabled, /buscar does not run.
	if !user.Active {
		return p.API.SendText(ctx, chat,
			"Todavía no habilité tu cuenta para notificaciones. Cuando lo haga, te llegan solas.")
	}

	searches, err := p.Repo.ListValidSearchURLs(ctx, userID)
	if err != nil {
		return err
	}
	if len(searches) == 0 {
		return p.API.SendText(ctx, chat,
			"Todavía no tengo búsquedas validadas tuyas. Sumá una con /addurl o esperá a que termine la validación.")
	}
	if p.Search == nil {
		return p.API.SendText(ctx, chat, "No puedo correr la búsqueda ahora mismo.")
	}

	if ok, wait := p.claimSearchRun(userID, p.now()); !ok {
		return p.API.SendText(ctx, chat, fmt.Sprintf(
			"Recién corrí una búsqueda tuya. Probá de nuevo en %d min.", minutesCeil(wait)))
	}

	if err := p.API.SendText(ctx, chat, "🔎 Buscando ahora…"); err != nil {
		return err
	}
	p.Logger.Info("chat: /buscar", "user", userID, "searches", len(searches))

	// Detached: a run can take minutes under the fetch pacing, and blocking the
	// poll loop would stall every other user's updates. The context keeps its
	// values but not the update's cancellation, bounded by a timeout.
	go func() {
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), searchRunTimeout)
		defer cancel()

		sent, err := p.Search.RunForUser(runCtx, userID, chatID)
		replyCtx, replyCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer replyCancel()
		if err != nil {
			p.Logger.Warn("chat: /buscar failed", "user", userID, "err", err)
			_ = p.API.SendText(replyCtx, chat, "⚠️ No pude completar la búsqueda. Probá de nuevo más tarde.")
			return
		}
		p.Logger.Info("chat: /buscar done", "user", userID, "sent", sent)
		_ = p.API.SendText(replyCtx, chat, searchResultText(sent))
	}()
	return nil
}

// claimSearchRun records the run time when the cooldown has elapsed. It is atomic
// so two /buscar messages arriving together cannot both claim the same window.
func (p *Poller) claimSearchRun(userID int64, now time.Time) (bool, time.Duration) {
	value, loaded := p.lastSearch.LoadOrStore(userID, now)
	if !loaded {
		return true, 0
	}
	last := value.(time.Time)
	if remaining := searchCooldown - now.Sub(last); remaining > 0 {
		return false, remaining
	}
	p.lastSearch.Store(userID, now)
	return true, 0
}

func minutesCeil(d time.Duration) int {
	m := int(d.Minutes())
	if d%time.Minute != 0 {
		m++
	}
	return m
}

func searchResultText(sent int) string {
	switch sent {
	case 0:
		return "No hay publicaciones nuevas por ahora."
	case 1:
		return "✅ Listo, te mandé 1 publicación nueva."
	default:
		return fmt.Sprintf("✅ Listo, te mandé %d publicaciones nuevas.", sent)
	}
}
