package repo

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"zonapropbot/internal/searchurl"
)

// ErrLimitReached is returned when a user already has the maximum number of
// searches.
var ErrLimitReached = errors.New("repo: search limit reached")

// ErrLabelTaken is returned when the label collides with another search of the
// same user.
var ErrLabelTaken = errors.New("repo: label already used")

// MaxSearchURLsPerUser caps how many searches one user may register. It bounds the
// fetch budget a single onboarding can consume.
const MaxSearchURLsPerUser = 5

// UserState is the onboarding state plus the scratch data it needs.
type UserState struct {
	UserID    int64
	ChatID    int64
	State     string
	StateData map[string]string
	Active    bool
}

// GetUser returns the user's state, or nil when they have never started.
func (r *Repo) GetUser(ctx context.Context, userID int64) (*UserState, error) {
	var (
		u    UserState
		data []byte
	)
	err := r.pool.QueryRow(ctx,
		`SELECT user_id, chat_id, state, state_data, active
		   FROM users WHERE user_id = $1`, userID).Scan(&u.UserID, &u.ChatID, &u.State, &data, &u.Active)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("repo: get user %d: %w", userID, err)
	}
	if len(data) > 0 {
		_ = jsonUnmarshal(data, &u.StateData)
	}
	return &u, nil
}

// SetUserState persists the onboarding state and its scratch data.
func (r *Repo) SetUserState(ctx context.Context, userID int64, state string, data map[string]string) error {
	encoded, err := jsonMarshal(data)
	if err != nil {
		return fmt.Errorf("repo: encode state data: %w", err)
	}
	tag, err := r.pool.Exec(ctx,
		`UPDATE users SET state = $2, state_data = $3 WHERE user_id = $1`, userID, state, encoded)
	if err != nil {
		return fmt.Errorf("repo: set state of user %d: %w", userID, err)
	}
	if tag.RowsAffected() == 0 {
		// Silently doing nothing here would make every onboarding step a no-op for a
		// user that was never created, and the bot would answer as if it had worked.
		return fmt.Errorf("repo: user %d does not exist", userID)
	}
	return nil
}

// AddSearchURLChecked registers a search, enforcing the per-user cap and label
// uniqueness inside the insert so two concurrent messages cannot both pass the
// check.
func (r *Repo) AddSearchURLChecked(ctx context.Context, userID int64, label, rawURL string) (int64, error) {
	norm, err := searchurl.Normalize(rawURL)
	if err != nil {
		return 0, err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("repo: add search begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	// Serialise per user: two messages processed at once must not both see room.
	if _, err := tx.Exec(ctx, `SELECT user_id FROM users WHERE user_id = $1 FOR UPDATE`, userID); err != nil {
		return 0, fmt.Errorf("repo: lock user: %w", err)
	}

	var count int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM search_urls WHERE user_id = $1`, userID).Scan(&count); err != nil {
		return 0, fmt.Errorf("repo: count searches: %w", err)
	}
	var existing bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM search_urls WHERE user_id = $1 AND url_norm = $2)`,
		userID, norm).Scan(&existing); err != nil {
		return 0, fmt.Errorf("repo: check existing search: %w", err)
	}
	if !existing && count >= MaxSearchURLsPerUser {
		return 0, ErrLimitReached
	}

	var id int64
	err = tx.QueryRow(ctx,
		`INSERT INTO search_urls (user_id, label, url, url_norm)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (user_id, url_norm) DO UPDATE SET label = EXCLUDED.label
		 RETURNING id`, userID, label, rawURL, norm).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, ErrLabelTaken
		}
		return 0, fmt.Errorf("repo: add search: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("repo: add search commit: %w", err)
	}
	return id, nil
}

// MarkSearchURLValid marks a search ready to be fetched and resets its validation
// counters (a re-add must make a previously failed URL retryable).
func (r *Repo) MarkSearchURLValid(ctx context.Context, searchURLID int64) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE search_urls
		    SET validation_status = 'valid', attempts = 0, next_check_at = NULL
		  WHERE id = $1`, searchURLID)
	if err != nil {
		return fmt.Errorf("repo: mark search valid: %w", err)
	}
	return nil
}

// SetSearchURLValidation records the outcome of a deep check, including when the
// next check is due.
func (r *Repo) SetSearchURLValidation(ctx context.Context, searchURLID int64, status string, nextCheckInSeconds int) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE search_urls
		    SET validation_status = $2,
		        attempts = attempts + 1,
		        next_check_at = CASE WHEN $3 > 0 THEN now() + ($3 || ' seconds')::interval ELSE NULL END,
		        last_checked_at = now()
		  WHERE id = $1`, searchURLID, status, nextCheckInSeconds)
	if err != nil {
		return fmt.Errorf("repo: set search validation: %w", err)
	}
	return nil
}

// PendingValidations returns the searches due for a deep check.
func (r *Repo) PendingValidations(ctx context.Context, limit int) ([]SearchURL, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, label, url, url_norm, validation_status, attempts
		   FROM search_urls
		  WHERE validation_status IN ('pending', 'retrying')
		    AND (next_check_at IS NULL OR next_check_at <= now())
		  ORDER BY next_check_at NULLS FIRST
		  LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("repo: pending validations: %w", err)
	}
	defer rows.Close()

	var out []SearchURL
	for rows.Next() {
		var s SearchURL
		if err := rows.Scan(&s.ID, &s.Label, &s.URL, &s.URLNorm, &s.ValidationStatus, &s.Attempts); err != nil {
			return nil, fmt.Errorf("repo: scan pending validation: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("repo: pending validations: %w", err)
	}
	return out, nil
}

// SearchURLOwner returns the user a search belongs to, so the validator can notify
// the right person.
func (r *Repo) SearchURLOwner(ctx context.Context, searchURLID int64) (userID, chatID int64, err error) {
	err = r.pool.QueryRow(ctx,
		`SELECT u.user_id, u.chat_id
		   FROM search_urls s JOIN users u ON u.user_id = s.user_id
		  WHERE s.id = $1`, searchURLID).Scan(&userID, &chatID)
	if err != nil {
		return 0, 0, fmt.Errorf("repo: owner of search %d: %w", searchURLID, err)
	}
	return userID, chatID, nil
}

// RemoveSearchURL deletes one of the user's searches by label.
func (r *Repo) RemoveSearchURL(ctx context.Context, userID int64, label string) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM search_urls WHERE user_id = $1 AND label = $2`, userID, label)
	if err != nil {
		return false, fmt.Errorf("repo: remove search: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// SetUserStopped pauses or resumes delivery. It is the user's own switch and is
// independent of the operator's active flag.
func (r *Repo) SetUserStopped(ctx context.Context, userID int64, stopped bool) error {
	state := "ready"
	if stopped {
		state = "stopped"
	}
	if _, err := r.pool.Exec(ctx, `UPDATE users SET state = $2 WHERE user_id = $1`, userID, state); err != nil {
		return fmt.Errorf("repo: set stopped: %w", err)
	}
	return nil
}

// DeleteUser removes the user and everything cascading from them: searches,
// deliveries and ratings. This is the data-deletion path behind /borrardatos.
func (r *Repo) DeleteUser(ctx context.Context, userID int64) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM users WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("repo: delete user %d: %w", userID, err)
	}
	return nil
}
