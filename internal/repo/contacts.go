package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"zonapropbot/internal/model"
)

// ContactTTL is how long a cached phone is trusted. A number from months ago may
// be reassigned, so it is refreshed rather than served as if fresh.
const ContactTTL = 30 * 24 * time.Hour

// GetContact returns the cached contact when it is still fresh. It returns
// ok=false when there is no row or the row is stale, which is the signal to fetch.
func (r *Repo) GetContact(ctx context.Context, listingID int64, now time.Time) (model.Contact, bool, error) {
	var (
		details   model.Contact
		fetchedAt time.Time
	)
	err := r.pool.QueryRow(ctx,
		`SELECT coalesce(phone,''), coalesce(email,''), coalesce(street_address,''),
		        coalesce(neighbourhood,''), fetched_at
		   FROM listing_contacts WHERE listing_id = $1`, listingID).
		Scan(&details.Phone, &details.Email, &details.StreetAddress, &details.Neighbourhood, &fetchedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Contact{}, false, nil
	}
	if err != nil {
		return model.Contact{}, false, fmt.Errorf("repo: get contact: %w", err)
	}
	if now.Sub(fetchedAt) > ContactTTL {
		return model.Contact{}, false, nil
	}
	return details, true, nil
}

// SaveContact caches what the detail page exposed. An empty result is stored too:
// caching "there is no phone" stops every future thumbs-up from re-fetching.
func (r *Repo) SaveContact(ctx context.Context, listingID int64, d model.Contact) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO listing_contacts (listing_id, phone, email, street_address, neighbourhood, fetched_at)
		 VALUES ($1, NULLIF($2,''), NULLIF($3,''), NULLIF($4,''), NULLIF($5,''), now())
		 ON CONFLICT (listing_id) DO UPDATE
		    SET phone = EXCLUDED.phone,
		        email = EXCLUDED.email,
		        street_address = EXCLUDED.street_address,
		        neighbourhood = EXCLUDED.neighbourhood,
		        fetched_at = now()`,
		listingID, d.Phone, d.Email, d.StreetAddress, d.Neighbourhood)
	if err != nil {
		return fmt.Errorf("repo: save contact: %w", err)
	}
	return nil
}

// ListingForContact returns the canonical URL of a listing, so the extractor can
// fetch its detail page.
func (r *Repo) ListingForContact(ctx context.Context, listingID int64) (canonicalURL string, err error) {
	err = r.pool.QueryRow(ctx,
		`SELECT canonical_url FROM listings WHERE id = $1`, listingID).Scan(&canonicalURL)
	if err != nil {
		return "", fmt.Errorf("repo: listing url for contact: %w", err)
	}
	return canonicalURL, nil
}
