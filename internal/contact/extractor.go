// Package contact also owns the extraction flow: cache first, fetch once when
// needed, and tell the user only when there is something to tell.
package contact

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"zonapropbot/internal/fetch"
	"zonapropbot/internal/model"
	"zonapropbot/internal/repo"
)

// Fetcher retrieves a page. fetch.Client satisfies it.
type Fetcher interface {
	FetchWith(ctx context.Context, u string, opts fetch.Options) (*fetch.Result, error)
}

// Notifier sends the follow-up message.
type Notifier interface {
	SendText(ctx context.Context, chatID, text string) error
}

// Extractor resolves a listing's contact and messages it to the user.
type Extractor struct {
	Repo     *repo.Repo
	Fetcher  Fetcher
	Notifier Notifier
	Logger   *slog.Logger
	// Now is overridable for tests.
	Now func() time.Time
}

func (e *Extractor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// OnLike handles a positive rating: the link is already on the card, so this only
// adds the phone when it exists. "Not found" is the normal path, not an error.
func (e *Extractor) OnLike(ctx context.Context, userID, chatID, listingID int64) error {
	details, fresh, err := e.Repo.GetContact(ctx, listingID, e.now())
	if err != nil {
		return err
	}
	if !fresh {
		details, err = e.fetchContact(ctx, listingID)
		if err != nil {
			// A failed detail fetch must not leave the user with less than before:
			// the link is already on the card.
			e.Logger.Warn("contact: detail fetch failed", "listing", listingID, "err", err)
			return nil
		}
	}

	if details.Phone == "" {
		e.Logger.Debug("contact: listing has no phone in its detail page", "listing", listingID)
		return nil
	}

	lines := []string{"📞 Teléfono del aviso: " + details.Phone}
	if details.StreetAddress != "" {
		lines = append(lines, "Dirección: "+details.StreetAddress)
	}
	lines = append(lines, "Mencioná que lo viste en Zonaprop.")
	e.Logger.Info("contact: phone sent", "user", userID, "listing", listingID)
	return e.Notifier.SendText(ctx, fmt.Sprintf("%d", chatID), strings.Join(lines, "\n"))
}

// fetchContact performs a single no-retry attempt with priority. Priority matters:
// a thumbs-up queued behind a bulk digest would arrive minutes later, long after
// the user's spinner gave up.
func (e *Extractor) fetchContact(ctx context.Context, listingID int64) (model.Contact, error) {
	canonicalURL, err := e.Repo.ListingForContact(ctx, listingID)
	if err != nil {
		return model.Contact{}, err
	}

	res, err := e.Fetcher.FetchWith(ctx, canonicalURL, fetch.Options{Priority: true, NoRetries: true})
	if err != nil {
		return model.Contact{}, err
	}
	details, ok := FromJSONLD(res.Body)
	if !ok {
		// Cache the empty result so a future thumbs-up does not spend another request.
		_ = e.Repo.SaveContact(ctx, listingID, model.Contact{})
		return model.Contact{}, nil
	}
	if err := e.Repo.SaveContact(ctx, listingID, details); err != nil {
		return model.Contact{}, err
	}
	return details, nil
}
