package repo

import (
	"context"
	"fmt"
	"time"
)

// EnsureDigest creates the row for a user's run on a date if it is missing,
// regardless of whether the job will actually run. The row is what makes an
// interrupted day resumable: it stays not-done until the work finishes.
func (r *Repo) EnsureDigest(ctx context.Context, userID int64, date time.Time) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO digests (user_id, run_date, status)
		 VALUES ($1, $2, 'pending')
		 ON CONFLICT (user_id, run_date) DO NOTHING`, userID, date)
	if err != nil {
		return fmt.Errorf("repo: ensure digest user=%d: %w", userID, err)
	}
	return nil
}

// DigestFinished reports whether the user's run for a date already completed.
func (r *Repo) DigestFinished(ctx context.Context, userID int64, date time.Time) (bool, error) {
	var done bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM digests
		                 WHERE user_id = $1 AND run_date = $2 AND status = 'done')`,
		userID, date).Scan(&done)
	if err != nil {
		return false, fmt.Errorf("repo: digest status: %w", err)
	}
	return done, nil
}

// PendingRun is an unfinished daily run, with everything needed to resume it.
type PendingRun struct {
	UserID  int64
	ChatID  int64
	RunDate time.Time
}

// PendingDigests returns the unfinished runs on or before date, oldest first. This
// is what the boot resumes: an interrupted day stays pending until it finishes.
func (r *Repo) PendingDigests(ctx context.Context, date time.Time) ([]PendingRun, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT d.user_id, u.chat_id, d.run_date
		   FROM digests d
		   JOIN users u ON u.user_id = d.user_id
		  WHERE d.status <> 'done' AND d.run_date <= $1
		  ORDER BY d.run_date, d.user_id`, date)
	if err != nil {
		return nil, fmt.Errorf("repo: pending digests: %w", err)
	}
	defer rows.Close()

	var out []PendingRun
	for rows.Next() {
		var pr PendingRun
		if err := rows.Scan(&pr.UserID, &pr.ChatID, &pr.RunDate); err != nil {
			return nil, fmt.Errorf("repo: scan pending digest: %w", err)
		}
		out = append(out, pr)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("repo: pending digests: %w", err)
	}
	return out, nil
}

// StartDigest marks the run as running.
func (r *Repo) StartDigest(ctx context.Context, userID int64, date time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE digests SET status = 'running', started_at = now(), error = NULL
		  WHERE user_id = $1 AND run_date = $2`, userID, date)
	if err != nil {
		return fmt.Errorf("repo: start digest: %w", err)
	}
	return nil
}

// FinishDigest records the outcome. sent is the cumulative count for the day, so a
// resumed run reports the day's total rather than only what the resume sent.
func (r *Repo) FinishDigest(ctx context.Context, userID int64, date time.Time, sent int, runErr error) error {
	status := "done"
	var message *string
	if runErr != nil {
		status = "error"
		text := runErr.Error()
		message = &text
	}
	_, err := r.pool.Exec(ctx,
		`UPDATE digests
		    SET status = $3, finished_at = now(), sent = $4, error = $5
		  WHERE user_id = $1 AND run_date = $2`, userID, date, status, sent, message)
	if err != nil {
		return fmt.Errorf("repo: finish digest: %w", err)
	}
	return nil
}

// UpdateSearchURLStats records what the last fetch of a search returned. It is how
// the 30-card ceiling is monitored: a search that keeps reporting 30 cards is
// saturating, and anything below the newest 30 is being missed.
func (r *Repo) UpdateSearchURLStats(ctx context.Context, searchURLID int64, fetchStatus int, cardCount int) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE search_urls
		    SET last_fetch_status = $2, last_card_count = $3, last_checked_at = now()
		  WHERE id = $1`, searchURLID, fetchStatus, cardCount)
	if err != nil {
		return fmt.Errorf("repo: update search stats: %w", err)
	}
	return nil
}
