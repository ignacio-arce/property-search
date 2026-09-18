// Package validate performs the deep check on a user's search URL: it fetches the
// page once and decides whether the search is usable.
//
// The point is the classification. "Zero cards" means three different things — a
// Cloudflare block, a genuinely empty search, or a DOM change — and the handling is
// opposite in each case: a block must be retried later and must never demote a
// valid URL, while a DOM change needs a human. Conflating them either rejects
// healthy searches or leaves them pending forever.
package validate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"zonapropbot/internal/fetch"
	"zonapropbot/internal/parser"
	"zonapropbot/internal/repo"
)

// Fetcher retrieves a page. fetch.Client satisfies it.
type Fetcher interface {
	FetchWith(ctx context.Context, u string, opts fetch.Options) (*fetch.Result, error)
}

// Notifier tells the user what happened to their URL.
type Notifier interface {
	SendText(ctx context.Context, chatID, text string) error
}

// Validator runs deep checks.
type Validator struct {
	Repo     *repo.Repo
	Fetcher  Fetcher
	Notifier Notifier
	Logger   *slog.Logger
	// MaxAttempts is how many deep checks a URL gets before it is declared invalid.
	MaxAttempts int
	// BatchSize bounds how many URLs one pass checks.
	BatchSize int
}

const (
	defaultMaxAttempts = 5
	defaultBatchSize   = 20
	maxBackoff         = 24 * time.Hour
)

// Outcome is what a deep check concluded.
type Outcome string

const (
	// OutcomeValid: answered with listings.
	OutcomeValid Outcome = "valid"
	// OutcomeValidEmpty: answered, no listings. A real, narrow search, not a failure.
	OutcomeValidEmpty Outcome = "valid_empty"
	// OutcomeBlocked: Cloudflare refused. Retry later; never demote a valid URL.
	OutcomeBlocked Outcome = "retrying"
	// OutcomeTransport: the network failed. Also retry later.
	OutcomeTransport Outcome = "retrying"
	// OutcomeInvalid: gave up after MaxAttempts.
	OutcomeInvalid Outcome = "invalid"
)

// RunOnce checks the searches that are due and returns how many became usable.
func (v *Validator) RunOnce(ctx context.Context) (int, error) {
	if v.MaxAttempts <= 0 {
		v.MaxAttempts = defaultMaxAttempts
	}
	if v.BatchSize <= 0 {
		v.BatchSize = defaultBatchSize
	}

	start := time.Now()
	pending, err := v.Repo.PendingValidations(ctx, v.BatchSize)
	if err != nil {
		return 0, err
	}
	if len(pending) == 0 {
		return 0, nil
	}

	becameValid := 0
	sawCards := false
	sawEmpty := false
	checked, valid, empty, invalid, retrying, failed := 0, 0, 0, 0, 0, 0

	for _, s := range pending {
		if err := ctx.Err(); err != nil {
			return becameValid, err
		}
		checked++
		outcome, cards, err := v.check(ctx, s)
		if err != nil {
			v.Logger.Warn("validate: check failed", "label", s.Label, "url", s.URL, "err", err)
			failed++
			continue
		}
		if cards > 0 {
			sawCards = true
		}
		if outcome == OutcomeValidEmpty {
			sawEmpty = true
		}

		attempts := s.Attempts + 1
		switch {
		case outcome == OutcomeValid:
			if err := v.Repo.MarkSearchURLValid(ctx, s.ID); err != nil {
				return becameValid, err
			}
			becameValid++
			valid++
			v.notify(ctx, s.ID, fmt.Sprintf("✅ `%s` ya está validada: empiezo a vigilarla.", s.Label))
			v.Logger.Debug("validate: search valid", "label", s.Label, "url", s.URL, "cards", cards)

		case outcome == OutcomeValidEmpty:
			if err := v.Repo.SetSearchURLValidation(ctx, s.ID, string(OutcomeValidEmpty), 0); err != nil {
				return becameValid, err
			}
			becameValid++
			empty++
			v.notify(ctx, s.ID, fmt.Sprintf("✅ `%s` está validada, aunque ahora mismo no tenga resultados.", s.Label))
			v.Logger.Debug("validate: search valid but empty", "label", s.Label, "url", s.URL)

		case attempts >= v.MaxAttempts:
			if err := v.Repo.SetSearchURLValidation(ctx, s.ID, string(OutcomeInvalid), 0); err != nil {
				return becameValid, err
			}
			invalid++
			v.notify(ctx, s.ID, fmt.Sprintf(
				"⚠️ No pude validar `%s` después de %d intentos. Fijate que la URL siga andando en el navegador, o borrala con /rmurl.",
				s.Label, attempts))
			v.Logger.Warn("validate: search invalid after attempts", "label", s.Label, "attempts", attempts)

		default:
			next := backoffSeconds(attempts)
			if err := v.Repo.SetSearchURLValidation(ctx, s.ID, string(outcome), next); err != nil {
				return becameValid, err
			}
			retrying++
			v.Logger.Debug("validate: search retrying", "label", s.Label, "status", string(outcome),
				"attempt", attempts, "next_check_s", next)
		}
	}

	// Canary: an empty page is normal for a narrow search, but if every search in
	// this pass came back empty, the DOM probably changed rather than the market.
	if sawEmpty && !sawCards {
		v.Logger.Warn("validate: every checked search returned zero cards; looks like a DOM change, not empty searches")
	}

	v.Logger.Info("validate: run done",
		"checked", checked, "valid", valid, "empty", empty,
		"invalid", invalid, "retrying", retrying, "failed", failed,
		"ms", time.Since(start).Milliseconds())
	return becameValid, nil
}

// check fetches and classifies one search.
func (v *Validator) check(ctx context.Context, s repo.SearchURL) (Outcome, int, error) {
	// No inner retries: five attempts times four retries is twenty hits on a
	// challenge page from an IP that is already suspect.
	res, err := v.Fetcher.FetchWith(ctx, s.URL, fetch.Options{NoRetries: true})
	if err != nil {
		if kind, ok := fetch.KindOf(err); ok && kind == fetch.KindBlocked {
			return OutcomeBlocked, 0, nil
		}
		// A transport failure is an outage, not a verdict on the URL.
		return OutcomeTransport, 0, nil
	}
	if res.Status != 200 {
		return OutcomeBlocked, 0, nil
	}

	listings, _, err := parser.ParseWithStats(res.Body, s.URL)
	if err != nil {
		return OutcomeTransport, 0, err
	}
	if len(listings) == 0 {
		return OutcomeValidEmpty, 0, nil
	}
	return OutcomeValid, len(listings), nil
}

func (v *Validator) notify(ctx context.Context, searchURLID int64, text string) {
	_, chatID, err := v.Repo.SearchURLOwner(ctx, searchURLID)
	if err != nil {
		v.Logger.Warn("validate: search owner lookup failed", "search", searchURLID, "err", err)
		return
	}
	if err := v.Notifier.SendText(ctx, fmt.Sprintf("%d", chatID), text); err != nil && !errors.Is(err, context.Canceled) {
		v.Logger.Warn("validate: notify failed", "search", searchURLID, "err", err)
	}
}

// backoffSeconds grows exponentially and caps at a day, so a URL that keeps failing
// stops consuming the scarce fetch budget every ten minutes.
func backoffSeconds(attempt int) int {
	seconds := math.Pow(2, float64(attempt)) * 60
	if seconds > maxBackoff.Seconds() {
		seconds = maxBackoff.Seconds()
	}
	return int(seconds)
}
