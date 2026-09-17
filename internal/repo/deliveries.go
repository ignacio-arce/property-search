package repo

import (
	"context"
	"encoding/json"
	"fmt"

	"zonapropbot/internal/model"
)

// Delivery statuses. "baseline" marks a listing that was already on the first
// page when the user subscribed: it is recorded as delivered so it is never sent,
// but it was never actually sent either.
const (
	DeliverySent     = "sent"
	DeliveryFailed   = "failed"
	DeliveryBaseline = "baseline"
)

// MarkDelivered records that a listing was delivered to the user, snapshotting
// the features that were shown. Written after a successful send, so a crash
// between the two re-sends rather than loses the listing: a rare duplicate is
// better than a silent loss.
func (r *Repo) MarkDelivered(ctx context.Context, userID, listingID, messageID int64, position int, score *float64, status string, snap model.Snapshot) error {
	features, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("repo: marshal delivery features: %w", err)
	}

	_, err = r.pool.Exec(ctx,
		`INSERT INTO deliveries (user_id, listing_id, message_id, position, score,
		                         features, status, attempts, sent_at)
		 VALUES ($1, $2, NULLIF($3, 0), $4, $5, $6, $7, 1, now())
		 ON CONFLICT (user_id, listing_id) DO UPDATE
		    SET message_id = EXCLUDED.message_id,
		        position   = EXCLUDED.position,
		        score      = EXCLUDED.score,
		        features   = EXCLUDED.features,
		        status     = EXCLUDED.status,
		        attempts   = deliveries.attempts + 1,
		        sent_at    = now()
		  WHERE deliveries.message_id IS NULL`,
		userID, listingID, messageID, position, score, features, status)
	if err != nil {
		return fmt.Errorf("repo: mark delivered user=%d listing=%d: %w", userID, listingID, err)
	}
	return nil
}

// BaselineDeliveries marks the given listings as already seen without sending
// them, so subscribing to a search does not replay its current inventory. This is
// not a rating bootstrap: it is the standard "do not replay history on subscribe"
// rule, and it is what replaces migrating the old seen.jsonl.
func (r *Repo) BaselineDeliveries(ctx context.Context, userID int64, listingIDs []int64) (int, error) {
	if len(listingIDs) == 0 {
		return 0, nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("repo: baseline begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	inserted := 0
	for _, listingID := range listingIDs {
		tag, err := tx.Exec(ctx,
			`INSERT INTO deliveries (user_id, listing_id, features, status)
			 SELECT $1, $2, l.features, $3
			   FROM listings l
			  WHERE l.id = $2
			 ON CONFLICT (user_id, listing_id) DO NOTHING`,
			userID, listingID, DeliveryBaseline)
		if err != nil {
			return 0, fmt.Errorf("repo: baseline listing %d: %w", listingID, err)
		}
		inserted += int(tag.RowsAffected())
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("repo: baseline commit: %w", err)
	}
	return inserted, nil
}

// UndeliveredLinkedToListing counts the user's linked, not-yet-delivered
// listings. Used by the baseline to decide whether a search has already been
// baselined.
func (r *Repo) UndeliveredLinkedToListing(ctx context.Context, userID, searchURLID int64) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*)
		   FROM listing_sources s
		  WHERE s.search_url_id = $1
		    AND NOT EXISTS (
		          SELECT 1 FROM deliveries d
		           WHERE d.listing_id = s.listing_id AND d.user_id = $2)`,
		searchURLID, userID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("repo: count undelivered for search %d: %w", searchURLID, err)
	}
	return n, nil
}
