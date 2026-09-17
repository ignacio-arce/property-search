package repo

import (
	"context"
	"encoding/json"
	"fmt"

	"zonapropbot/internal/model"
)

// ListingInput is a parsed listing plus the position it occupied in the
// recency-ordered fetch. Both are needed: the position is the only reliable
// recency signal, because the Zonaprop id is not monotonic with publication date.
type ListingInput struct {
	Listing     model.Listing
	RecencyRank int
}

// UpsertListing stores a listing keyed by its Zonaprop id and returns its row id.
// The listing is global: the same posting is stored once and referenced by every
// user who receives it. Last-indexed values are refreshed, first_indexed_at is
// left alone.
func (r *Repo) UpsertListing(ctx context.Context, in ListingInput) (int64, error) {
	features, err := json.Marshal(in.Listing.Snapshot())
	if err != nil {
		return 0, fmt.Errorf("repo: marshal features: %w", err)
	}

	var id int64
	err = r.pool.QueryRow(ctx,
		`INSERT INTO listings (zonaprop_id, canonical_url, operation_type, currency,
		                       features, recency_rank)
		 VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, $6)
		 ON CONFLICT (zonaprop_id) DO UPDATE
		    SET canonical_url  = EXCLUDED.canonical_url,
		        operation_type = EXCLUDED.operation_type,
		        currency       = EXCLUDED.currency,
		        features       = EXCLUDED.features,
		        last_indexed_at = now()
		 RETURNING id`,
		in.Listing.ZonapropID, in.Listing.CanonicalURL, in.Listing.Operation,
		in.Listing.Currency, features, in.RecencyRank).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("repo: upsert listing %s: %w", in.Listing.ZonapropID, err)
	}
	return id, nil
}

// LinkListingSource records that a listing was found through a given search.
// Without this join, "listings not yet delivered" would select the whole global
// catalogue and leak every user's inventory to every other user.
func (r *Repo) LinkListingSource(ctx context.Context, listingID, searchURLID int64) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO listing_sources (listing_id, search_url_id)
		 VALUES ($1, $2)
		 ON CONFLICT (listing_id, search_url_id) DO NOTHING`, listingID, searchURLID)
	if err != nil {
		return fmt.Errorf("repo: link listing %d to search %d: %w", listingID, searchURLID, err)
	}
	return nil
}

// Candidate is a listing the user has not been shown yet.
type Candidate struct {
	ListingID int64
	Listing   model.Listing
}

// Candidates returns the user's undelivered listings, newest first, limited to
// the searches the user actually subscribes to.
//
// Ordering is by recency: first_indexed_at descending (a listing first seen today
// is newer than one first seen yesterday), then recency_rank ascending within a
// batch (position 1 in the recency-ordered page is the newest), then id as a
// stable tiebreaker. The score will reorder this in V3; until then, recency is
// the honest default.
func (r *Repo) Candidates(ctx context.Context, userID int64, limit int) ([]Candidate, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT l.id, l.features
		   FROM listings l
		  WHERE EXISTS (
		          SELECT 1 FROM listing_sources s
		            JOIN search_urls u ON u.id = s.search_url_id
		           WHERE s.listing_id = l.id AND u.user_id = $1)
		    AND NOT EXISTS (
		          SELECT 1 FROM deliveries d
		           WHERE d.listing_id = l.id AND d.user_id = $1)
		  ORDER BY l.first_indexed_at DESC,
		           l.recency_rank ASC NULLS LAST,
		           l.id ASC
		  LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("repo: candidates for user %d: %w", userID, err)
	}
	defer rows.Close()

	var out []Candidate
	for rows.Next() {
		var id int64
		var features []byte
		if err := rows.Scan(&id, &features); err != nil {
			return nil, fmt.Errorf("repo: scan candidate: %w", err)
		}
		var snap model.Snapshot
		if err := json.Unmarshal(features, &snap); err != nil {
			return nil, fmt.Errorf("repo: decode features of listing %d: %w", id, err)
		}
		out = append(out, Candidate{ListingID: id, Listing: snap.Listing()})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("repo: candidates for user %d: %w", userID, err)
	}
	return out, nil
}

// CountListings returns how many listings are indexed, used to detect the
// 30-card ceiling being saturated.
func (r *Repo) CountListings(ctx context.Context) (int, error) {
	var n int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM listings`).Scan(&n); err != nil {
		return 0, fmt.Errorf("repo: count listings: %w", err)
	}
	return n, nil
}

// CountListingSources reports how many listings have ever been indexed for a
// search. Zero means the search has never produced results, which is the signal
// that its first successful index must be baselined rather than sent.
func (r *Repo) CountListingSources(ctx context.Context, searchURLID int64) (int, error) {
	var n int
	if err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM listing_sources WHERE search_url_id = $1`, searchURLID).Scan(&n); err != nil {
		return 0, fmt.Errorf("repo: count sources of search %d: %w", searchURLID, err)
	}
	return n, nil
}

// LinkedListingIDs returns every listing indexed through a search, so the
// baseline can mark them as seen without sending them.
func (r *Repo) LinkedListingIDs(ctx context.Context, searchURLID int64) ([]int64, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT listing_id FROM listing_sources WHERE search_url_id = $1`, searchURLID)
	if err != nil {
		return nil, fmt.Errorf("repo: linked listings of search %d: %w", searchURLID, err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("repo: scan linked listing: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("repo: linked listings of search %d: %w", searchURLID, err)
	}
	return ids, nil
}
