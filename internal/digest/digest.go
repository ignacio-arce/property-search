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
	"log/slog"
	"sort"
	"time"

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
	Logger   *slog.Logger
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
		r.Logger.Info("digest: no active users")
	}

	total := 0
	for _, u := range users {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		sent, err := r.RunForUser(ctx, u.UserID, u.ChatID)
		if err != nil {
			// One user's failure must not stop the others.
			r.Logger.Warn("digest: user failed", "user", u.UserID, "err", err)
			continue
		}
		total += sent
	}
	return total, nil
}

// RunForUser fetches the user's searches, indexes new listings and sends the ones
// the user has not been shown. It returns how many were sent.
func (r *Runner) RunForUser(ctx context.Context, userID, chatID int64) (int, error) {
	start := time.Now()
	searches, err := r.Repo.ListValidSearchURLs(ctx, userID)
	if err != nil {
		return 0, err
	}
	if len(searches) == 0 {
		r.Logger.Info("digest: user has no valid searches", "user", userID)
		return 0, nil
	}

	fetched := 0
	for _, s := range searches {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if err := r.indexSearch(ctx, userID, s); err != nil {
			// A failing URL must not abort the cycle: the remaining searches still
			// get processed and the failure is logged.
			r.Logger.Warn("digest: search failed", "user", userID, "label", s.Label, "err", err)
			r.Logger.Debug("digest: search failed url", "user", userID, "url", s.URL, "err", err)
			continue
		}
		fetched++
	}

	// If not a single search could be read, the day was not a success: mark it so
	// the run is retried instead of silently counted as done. A partial failure is
	// different — the remaining listings are still undelivered and go out tomorrow.
	if fetched == 0 {
		return 0, fmt.Errorf("no search could be fetched for user %d (%d configured)", userID, len(searches))
	}

	sent, candidates, err := r.sendUndelivered(ctx, userID, chatID)
	if err != nil {
		return sent, err
	}
	capHit := r.MaxPerRun > 0 && candidates >= r.MaxPerRun
	r.Logger.Info("digest: user done",
		"user", userID,
		"searches", len(searches),
		"fetched", fetched,
		"candidates", candidates,
		"sent", sent,
		"cap_hit", capHit,
		"ms", time.Since(start).Milliseconds())
	return sent, nil
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
	r.Logger.Debug("digest: search parsed",
		"label", s.Label, "url", s.URL, "mode", res.Mode,
		"cards", stats.Cards, "skipped_type", stats.SkippedType,
		"skipped_noid", stats.SkippedNoID, "parsed", len(listings))

	if err := r.Repo.UpdateSearchURLStats(ctx, s.ID, res.Status, stats.Cards); err != nil {
		r.Logger.Warn("digest: could not record stats", "label", s.Label, "err", err)
	}
	// A search that keeps returning a full page is saturating: anything below the
	// newest page is being missed, and that is a portal limit, not a bug.
	if stats.Cards >= fullPageSize {
		r.Logger.Warn("digest: search returned a full page; listings beyond it are not covered",
			"label", s.Label, "cards", stats.Cards)
	}

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
		r.Logger.Info("digest: search baselined", "label", s.Label, "not_sent", n)
	}

	return nil
}

// sendUndelivered delivers the user's pending listings, newest first, isolating
// per-listing failures. It returns how many were sent and how many candidates the
// query considered, so the caller can report the funnel.
func (r *Runner) sendUndelivered(ctx context.Context, userID, chatID int64) (sent, candidates int, err error) {
	limit := r.MaxPerRun
	if limit <= 0 {
		limit = 1000
	}

	pending, err := r.Repo.Candidates(ctx, userID, limit)
	if err != nil {
		return 0, 0, err
	}

	weights, err := score.Load(ctx, r.Repo, userID)
	if err != nil {
		return 0, len(pending), err
	}

	// Rank, then sort. The sort is stable on top of the query recency order, so
	// ties fall back to "newest first" instead of an arbitrary order.
	type rankedListing struct {
		candidate repo.Candidate
		score     float64
		reasons   []string
	}
	ranked := make([]rankedListing, 0, len(pending))
	for _, c := range pending {
		features := score.Extract(c.Listing)
		value, _ := weights.Score(features)
		var reasons []string
		for _, reason := range weights.Reasons(features, maxReasonsOnCard) {
			reasons = append(reasons, reason.Text)
		}
		ranked = append(ranked, rankedListing{candidate: c, score: value, reasons: reasons})
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })

	for i, rk := range ranked {
		if err := ctx.Err(); err != nil {
			return sent, len(pending), err
		}

		delivery := model.Delivery{
			ListingID: rk.candidate.ListingID,
			Listing:   rk.candidate.Listing,
			Header:    rankHeader(i+1, len(ranked)),
			Reasons:   rk.reasons,
		}
		if err := r.Notifier.Notify(ctx, formatChatID(chatID), delivery); err != nil {
			r.Logger.Warn("digest: notify failed", "listing", rk.candidate.ListingID, "err", err)
			continue
		}

		// Recorded after a successful send. A crash in between re-sends the listing
		// tomorrow rather than losing it: a rare duplicate beats a silent loss.
		// message_id is not threaded yet; V2.2 needs it to revoke the keyboard.
		recorded := rk.score
		if err := r.Repo.MarkDelivered(ctx, userID, rk.candidate.ListingID, 0, i+1, &recorded,
			repo.DeliverySent, rk.candidate.Listing.Snapshot()); err != nil {
			return sent, len(pending), fmt.Errorf("record delivery of listing %d: %w", rk.candidate.ListingID, err)
		}
		sent++
	}
	return sent, len(pending), nil
}

// maxReasonsOnCard is how many plain-language reasons a card shows. Two is enough
// to be useful without turning the caption into a report.
const maxReasonsOnCard = 2

// fullPageSize is Zonaprop's page size. The portal serves only the first page, so
// hitting it means the coverage ceiling was reached.
const fullPageSize = 30

// rankHeader states the position honestly. A percentage would be false precision
// with this little data.
func rankHeader(position, total int) string {
	if total <= 1 {
		return "Hoy"
	}
	return fmt.Sprintf("#%d de %d hoy", position, total)
}

func formatChatID(chatID int64) string {
	return fmt.Sprintf("%d", chatID)
}

// RunDaily runs the digest for every active user whose run for date is not
// finished yet, and resumes any earlier unfinished run.
//
// The row is created for every active user before deciding whether to run, so a
// day that is skipped by a lock is still recorded and can be resumed. The input is
// "listings not yet delivered", not "today's listings", which is what makes a
// resume correct rather than a duplicate.
func (r *Runner) RunDaily(ctx context.Context, date time.Time) (int, error) {
	users, err := r.Repo.ListActiveUsers(ctx)
	if err != nil {
		return 0, err
	}
	for _, u := range users {
		if err := r.Repo.EnsureDigest(ctx, u.UserID, date); err != nil {
			r.Logger.Warn("digest: could not record run", "user", u.UserID, "err", err)
		}
	}

	pending, err := r.Repo.PendingDigests(ctx, date)
	if err != nil {
		return 0, err
	}
	if len(pending) == 0 {
		r.Logger.Info("digest: nothing pending", "date", date.Format("2006-01-02"))
		return 0, nil
	}

	total := 0
	for _, run := range pending {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		if err := r.Repo.StartDigest(ctx, run.UserID, run.RunDate); err != nil {
			r.Logger.Warn("digest: start run failed", "user", run.UserID, "err", err)
			continue
		}

		sent, runErr := r.RunForUser(ctx, run.UserID, run.ChatID)
		if runErr != nil {
			r.Logger.Warn("digest: user failed", "user", run.UserID, "err", runErr)
		}
		if err := r.Repo.FinishDigest(ctx, run.UserID, run.RunDate, sent, runErr); err != nil {
			r.Logger.Warn("digest: finish run failed", "user", run.UserID, "err", err)
		}
		total += sent
	}
	return total, nil
}
