// Package repo is the data access layer. Every method that touches per-user data
// takes userID as its first argument and scopes every query by it, so crossing
// users requires deliberately ignoring the parameter.
//
// Isolation is the easiest property to break silently and there is no type system
// that enforces it, so it is a structural convention: no exported query lets a
// caller omit the user.
package repo

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"zonapropbot/internal/searchurl"
)

// Repo provides user-scoped access to the database.
type Repo struct {
	pool *pgxpool.Pool
}

// New wraps a pool.
func New(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// Pool exposes the underlying pool for layers that need transactions across
// repositories. Prefer the scoped methods.
func (r *Repo) Pool() *pgxpool.Pool {
	return r.pool
}

// EnsureUser creates the user row if it does not exist. chatID is the delivery
// target; for the private chats the bot accepts, it equals userID.
func (r *Repo) EnsureUser(ctx context.Context, userID, chatID int64) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO users (user_id, chat_id, state)
		 VALUES ($1, $2, 'idle')
		 ON CONFLICT (user_id) DO NOTHING`, userID, chatID)
	if err != nil {
		return fmt.Errorf("repo: ensure user %d: %w", userID, err)
	}
	return nil
}

// SearchURL is one configured search of a user.
type SearchURL struct {
	ID               int64
	Label            string
	URL              string
	URLNorm          string
	ValidationStatus string
	Attempts         int
}

// AddSearchURL registers a search for the user. Re-adding the same normalized URL
// updates its label instead of failing, so a user correcting a typo is not an
// error. The URL is not validated here; that is the validation layer's job.
func (r *Repo) AddSearchURL(ctx context.Context, userID int64, label, rawURL string) (int64, error) {
	norm, err := searchurl.Normalize(rawURL)
	if err != nil {
		return 0, err
	}
	var id int64
	err = r.pool.QueryRow(ctx,
		`INSERT INTO search_urls (user_id, label, url, url_norm)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (user_id, url_norm) DO UPDATE SET label = EXCLUDED.label
		 RETURNING id`, userID, label, rawURL, norm).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("repo: add search url for user %d: %w", userID, err)
	}
	return id, nil
}

// ListSearchURLs returns every search of the user, ordered by label.
func (r *Repo) ListSearchURLs(ctx context.Context, userID int64) ([]SearchURL, error) {
	return r.listSearchURLs(ctx, userID, "")
}

// ListValidSearchURLs returns the user's searches that are ready to be fetched.
func (r *Repo) ListValidSearchURLs(ctx context.Context, userID int64) ([]SearchURL, error) {
	return r.listSearchURLs(ctx, userID, "AND validation_status = 'valid'")
}

func (r *Repo) listSearchURLs(ctx context.Context, userID int64, extraWhere string) ([]SearchURL, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, label, url, url_norm, validation_status, attempts
		   FROM search_urls
		  WHERE user_id = $1 `+extraWhere+`
		  ORDER BY label`, userID)
	if err != nil {
		return nil, fmt.Errorf("repo: list search urls for user %d: %w", userID, err)
	}
	defer rows.Close()

	var out []SearchURL
	for rows.Next() {
		var s SearchURL
		if err := rows.Scan(&s.ID, &s.Label, &s.URL, &s.URLNorm, &s.ValidationStatus, &s.Attempts); err != nil {
			return nil, fmt.Errorf("repo: scan search url: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("repo: list search urls for user %d: %w", userID, err)
	}
	return out, nil
}
