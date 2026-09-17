// Package digest runs a delivery cycle for a user: fetch their searches, index
// what is new, baseline a search the first time it yields results, and send the
// rest.
//
// The baseline is the piece that makes subscribing sane. Without it, a user's
// first run would replay the entire current inventory — about 30 cards per search
// — which is both a mute-the-bot event and a pointless use of the scarce fetch
// budget.
package digest

import (
	"context"
	"fmt"
	"log"
	"sort"

	"zonapropbot/internal/fetch"
	"zonapropbot/internal/model"
	"zonapropbot/internal/parser"
	"zonapropbot/internal/repo"
	"zonapropbot/internal/score"
)

// Fetcher retrieves a search page. fetch.Client satisfies it.
type Fetcher interface {
	Fetch(ctx context.Context, u string) (*fetch.Result, error)
}

// Notifier delivers one listing to one chat. telegram.Notifier satisfies it.
type Notifier interface {
	Notify(ctx context.Context, chatID string, d model.Delivery) error
}

// Runner owns one delivery cycle.
type Runner struct {
	Repo     *repo.Repo
	Fetcher  Fetcher
	Notifier Notifier
	Logger   *log.Logger
	// MaxPerRun caps how many listings are sent in one cycle. 0 means no cap;
	// the daily cap with carry-over arrives in V4.3.
	MaxPerRun int
}

// RunAll processes every user the operator has activated and returns how many
// listings were sent in total.
func (r *Runner) RunAll(ctx context.Context) (int, error) {
	users, err := r.Repo.ListActiveUsers(ctx)
	if err != nil {
		return 0, err
	}
	if len(users) == 0 {
		r.logf("digest: no active users")
	}

	total := 0
	for _, u := range users {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		sent, err := r.RunForUser(ctx, u.UserID, u.ChatID)
		if err != nil {
			// One user's failure must not stop the others.
			r.logf("digest: user %d failed: %v", u.UserID, err)
			continue
		}
		total += sent
	}
	return total, nil
}

// RunForUser fetches the user's searches, indexes new listings and sends the ones
// the user has not been shown. It returns how many were sent.
func (r *Runner) RunForUser(ctx context.Context, userID, chatID int64) (int, error) {
	searches, err := r.Repo.ListValidSearchURLs(ctx, userID)
	if err != nil {
		return 0, err
	}
	if len(searches) == 0 {
		r.logf("digest: user %d has no valid searches", userID)
		return 0, nil
	}

	for _, s := range searches {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if err := r.indexSearch(ctx, userID, s); err != nil {
			// A failing URL must not abort the cycle: the remaining searches still
			// get processed and the failure is logged.
			r.logf("digest: search %q (%s) failed: %v", s.Label, s.URL, err)
		}
	}

	return r.sendUndelivered(ctx, userID, chatID)
}

// indexSearch fetches one search, stores what it found, and baselines it if this
// was its first successful index.
func (r *Runner) indexSearch(ctx context.Context, userID int64, s repo.SearchURL) error {
	sourcesBefore, err := r.Repo.CountListingSources(ctx, s.ID)
	if err != nil {
		return err
	}

	res, err := r.Fetcher.Fetch(ctx, s.URL)
	if err != nil {
		return err
	}

	listings, stats, err := parser.ParseWithStats(res.Body, s.URL)
	if err != nil {
		return err
	}
	r.logf("digest: search %q: mode=%s cards=%d skipped_type=%d skipped_noid=%d parsed=%d",
		s.Label, res.Mode, stats.Cards, stats.SkippedType, stats.SkippedNoID, len(listings))

	for i, l := range listings {
		listingID, err := r.Repo.UpsertListing(ctx, repo.ListingInput{
			Listing: l,
			// Position within the recency-ordered page: 1 is the newest. It is the
			// only reliable recency signal, because the Zonaprop id is not monotonic
			// with publication date.
			RecencyRank: i + 1,
		})
		if err != nil {
			return err
		}
		if err := r.Repo.LinkListingSource(ctx, listingID, s.ID); err != nil {
			return err
		}
	}

	// First time this search yielded anything: mark the whole page as already seen
	// instead of replaying it. This is not a rating bootstrap, it is "do not replay
	// history on subscribe".
	if sourcesBefore == 0 && len(listings) > 0 {
		ids, err := r.Repo.LinkedListingIDs(ctx, s.ID)
		if err != nil {
			return err
		}
		n, err := r.Repo.BaselineDeliveries(ctx, userID, ids)
		if err != nil {
			return err
		}
		r.logf("digest: search %q baselined %d listings (not sent)", s.Label, n)
	}

	return nil
}

// sendUndelivered delivers the user's pending listings, newest first, isolating
// per-listing failures.
func (r *Runner) sendUndelivered(ctx context.Context, userID, chatID int64) (int, error) {
	limit := r.MaxPerRun
	if limit <= 0 {
		limit = 1000
	}

	candidates, err := r.Repo.Candidates(ctx, userID, limit)
	if err != nil {
		return 0, err
	}

	weights, err := score.Load(ctx, r.Repo, userID)
	if err != nil {
		return 0, err
	}

	// Rank, then sort. The sort is stable on top of the query recency order, so
	// ties fall back to "newest first" instead of an arbitrary order.
	type rankedListing struct {
		candidate repo.Candidate
		score     float64
		reasons   []string
	}
	ranked := make([]rankedListing, 0, len(candidates))
	for _, c := range candidates {
		features := score.Extract(c.Listing)
		value, _ := weights.Score(features)
		var reasons []string
		for _, reason := range weights.Reasons(features, maxReasonsOnCard) {
			reasons = append(reasons, reason.Text)
		}
		ranked = append(ranked, rankedListing{candidate: c, score: value, reasons: reasons})
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })

	sent := 0
	for i, rk := range ranked {
		if err := ctx.Err(); err != nil {
			return sent, err
		}

		delivery := model.Delivery{
			ListingID: rk.candidate.ListingID,
			Listing:   rk.candidate.Listing,
			Header:    rankHeader(i+1, len(ranked)),
			Reasons:   rk.reasons,
		}
		if err := r.Notifier.Notify(ctx, formatChatID(chatID), delivery); err != nil {
			r.logf("digest: notify listing %d failed: %v", rk.candidate.ListingID, err)
			continue
		}

		// Recorded after a successful send. A crash in between re-sends the listing
		// tomorrow rather than losing it: a rare duplicate beats a silent loss.
		// message_id is not threaded yet; V2.2 needs it to revoke the keyboard.
		recorded := rk.score
		if err := r.Repo.MarkDelivered(ctx, userID, rk.candidate.ListingID, 0, i+1, &recorded,
			repo.DeliverySent, rk.candidate.Listing.Snapshot()); err != nil {
			return sent, fmt.Errorf("record delivery of listing %d: %w", rk.candidate.ListingID, err)
		}
		sent++
	}
	return sent, nil
}

// maxReasonsOnCard is how many plain-language reasons a card shows. Two is enough
// to be useful without turning the caption into a report.
const maxReasonsOnCard = 2

// rankHeader states the position honestly. A percentage would be false precision
// with this little data.
func rankHeader(position, total int) string {
	if total <= 1 {
		return "Hoy"
	}
	return fmt.Sprintf("#%d de %d hoy", position, total)
}

func (r *Runner) logf(format string, args ...any) {
	if r.Logger != nil {
		r.Logger.Printf(format, args...)
	}
}

func formatChatID(chatID int64) string {
	return fmt.Sprintf("%d", chatID)
}
