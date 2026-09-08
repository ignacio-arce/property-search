package bot

import (
	"context"
	"log"

	"zonapropbot/internal/config"
	"zonapropbot/internal/fetch"
	"zonapropbot/internal/model"
	"zonapropbot/internal/parser"
)

// Fetcher retrieves a search page. fetch.Client satisfies it.
type Fetcher interface {
	Fetch(ctx context.Context, u string) (*fetch.Result, error)
}

// Notifier delivers a listing alert. telegram.Notifier satisfies it.
type Notifier interface {
	Notify(ctx context.Context, l model.Listing) error
}

// Store persists seen listing IDs. store.Store satisfies it.
type Store interface {
	Contains(id string) bool
	Add(l model.Listing) error
}

// RunOnce processes every configured search URL once: it fetches the page,
// parses the listings, notifies the ones not seen yet and marks them as seen.
// A URL that fails to fetch is logged and skipped so the remaining URLs still
// get processed; the loop returns only on context cancellation.
func RunOnce(ctx context.Context, cfg *config.Config, fetcher Fetcher, notifier Notifier, st Store, logger *log.Logger) error {
	for _, u := range cfg.SearchURLs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := processURL(ctx, u, fetcher, notifier, st); err != nil {
			logger.Printf("url %s: %v", u, err)
		}
	}
	return ctx.Err()
}

func processURL(ctx context.Context, u string, fetcher Fetcher, notifier Notifier, st Store) error {
	res, err := fetcher.Fetch(ctx, u)
	if err != nil {
		return err
	}
	listings, err := parser.Parse(res.Body, u)
	if err != nil {
		return err
	}
	for _, l := range listings {
		if err := ctx.Err(); err != nil {
			return err
		}
		if st.Contains(l.ID) {
			continue
		}
		if err := notifier.Notify(ctx, l); err != nil {
			return err
		}
		if err := st.Add(l); err != nil {
			return err
		}
	}
	return nil
}
