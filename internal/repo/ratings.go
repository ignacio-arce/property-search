package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrNoDelivery means the user was never sent that listing, so a rating for it
// cannot be attached.
var ErrNoDelivery = errors.New("repo: no delivery for that listing")

// RecordRating stores a rating and marks the delivery as rated, in one
// transaction. A crash between the two writes would otherwise leave a listing
// that is rated but still pending, or delivered but never trained on.
//
// It returns ErrNoDelivery when the listing was never delivered to the user,
// which is what rejects a forged or stale callback.
func (r *Repo) RecordRating(ctx context.Context, userID, listingID int64, label int) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("repo: rating begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM deliveries WHERE user_id = $1 AND listing_id = $2)`,
		userID, listingID).Scan(&exists); err != nil {
		return fmt.Errorf("repo: check delivery: %w", err)
	}
	if !exists {
		return ErrNoDelivery
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO ratings (user_id, listing_id, label)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (user_id, listing_id)
		 DO UPDATE SET label = EXCLUDED.label, created_at = now()`,
		userID, listingID, label); err != nil {
		return fmt.Errorf("repo: insert rating: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE deliveries SET status = 'rated'
		  WHERE user_id = $1 AND listing_id = $2`, userID, listingID); err != nil {
		return fmt.Errorf("repo: mark delivery rated: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("repo: rating commit: %w", err)
	}
	return nil
}

// ListingTitle returns a listing's title, used to label command output.
func (r *Repo) ListingTitle(ctx context.Context, listingID int64) (string, error) {
	var title string
	err := r.pool.QueryRow(ctx,
		`SELECT coalesce(features->>'title', '') FROM listings WHERE id = $1`, listingID).Scan(&title)
	if err != nil {
		return "", fmt.Errorf("repo: title of listing %d: %w", listingID, err)
	}
	return title, nil
}

// RatingCounts returns how many 👍 and 👎 the user has recorded. Until the model
// exists (V3) this is the only honest thing to show about what has been learned.
func (r *Repo) RatingCounts(ctx context.Context, userID int64) (likes, dislikes int, err error) {
	err = r.pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE label = 1),
		        count(*) FILTER (WHERE label = 0)
		   FROM ratings WHERE user_id = $1`, userID).Scan(&likes, &dislikes)
	if err != nil {
		return 0, 0, fmt.Errorf("repo: rating counts for user %d: %w", userID, err)
	}
	return likes, dislikes, nil
}

// DeliveryLeaseDuration is how long a poll lease is held before another instance
// may take over.
const DeliveryLeaseDuration = 90 * time.Second

// AcquirePollLease takes the single poller lease, or reports that someone else
// holds it. Without it, running the bot locally with the production token would
// silently consume the live updates.
func (r *Repo) AcquirePollLease(ctx context.Context, holder string) (bool, error) {
	var acquired bool
	err := r.pool.QueryRow(ctx,
		`UPDATE bot_state
		    SET poll_lease_holder = $1,
		        poll_lease_expires_at = now() + $2::interval
		  WHERE id = 1
		    AND (poll_lease_holder IS NULL
		         OR poll_lease_holder = $1
		         OR poll_lease_expires_at IS NULL
		         OR poll_lease_expires_at < now())
		  RETURNING true`, holder, DeliveryLeaseDuration.String()).Scan(&acquired)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("repo: acquire poll lease: %w", err)
	}
	return acquired, nil
}

// ReleasePollLease drops the lease on a clean shutdown.
func (r *Repo) ReleasePollLease(ctx context.Context, holder string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE bot_state SET poll_lease_holder = NULL, poll_lease_expires_at = NULL
		  WHERE id = 1 AND poll_lease_holder = $1`, holder)
	if err != nil {
		return fmt.Errorf("repo: release poll lease: %w", err)
	}
	return nil
}

// LastUpdateID returns the persisted getUpdates offset.
func (r *Repo) LastUpdateID(ctx context.Context) (int64, error) {
	var id int64
	if err := r.pool.QueryRow(ctx, `SELECT last_update_id FROM bot_state WHERE id = 1`).Scan(&id); err != nil {
		return 0, fmt.Errorf("repo: read last update id: %w", err)
	}
	return id, nil
}

// AdvanceUpdateID persists the offset. It is called only after an update has been
// handled, so a crash replays it rather than dropping it.
func (r *Repo) AdvanceUpdateID(ctx context.Context, updateID int64) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE bot_state SET last_update_id = GREATEST(last_update_id, $1) WHERE id = 1`, updateID)
	if err != nil {
		return fmt.Errorf("repo: advance update id: %w", err)
	}
	return nil
}

// RatingState describes what a callback should do with a listing: whether the
// user was ever sent it, when, and whether they already rated it.
type RatingState struct {
	Delivered bool
	SentAt    time.Time
	HasRating bool
	Label     int
}

// RatingState reads the delivery and any existing rating in one query, which is
// what the callback handler needs to decide between recording, ignoring (already
// rated), and rejecting (never delivered).
func (r *Repo) RatingState(ctx context.Context, userID, listingID int64) (RatingState, error) {
	var st RatingState
	var sentAt *time.Time
	var label *int
	err := r.pool.QueryRow(ctx,
		`SELECT d.sent_at, ra.label
		   FROM deliveries d
		   LEFT JOIN ratings ra
		          ON ra.user_id = d.user_id AND ra.listing_id = d.listing_id
		  WHERE d.user_id = $1 AND d.listing_id = $2`,
		userID, listingID).Scan(&sentAt, &label)
	if errors.Is(err, pgx.ErrNoRows) {
		return RatingState{}, nil
	}
	if err != nil {
		return RatingState{}, fmt.Errorf("repo: rating state: %w", err)
	}

	st.Delivered = true
	if sentAt != nil {
		st.SentAt = *sentAt
	}
	if label != nil {
		st.HasRating = true
		st.Label = *label
	}
	return st, nil
}
