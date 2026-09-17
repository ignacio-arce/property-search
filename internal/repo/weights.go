package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"zonapropbot/internal/model"
)

// WeightRow is one bucket of a model generation.
type WeightRow struct {
	Feature string
	Value   string
	Ups     int
	Downs   int
}

// TrainingSample is a rating together with the exact snapshot the user was shown.
// Training on deliveries.features rather than the latest listings.features matters:
// a seller can change the price, and the model must learn from what the user
// actually reacted to.
type TrainingSample struct {
	Label    int
	Snapshot model.Snapshot
}

// TrainingSamples returns every rated listing of the user.
func (r *Repo) TrainingSamples(ctx context.Context, userID int64) ([]TrainingSample, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT ra.label, d.features
		   FROM ratings ra
		   JOIN deliveries d
		     ON d.user_id = ra.user_id AND d.listing_id = ra.listing_id
		  WHERE ra.user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("repo: training samples for user %d: %w", userID, err)
	}
	defer rows.Close()

	var out []TrainingSample
	for rows.Next() {
		var label int
		var features []byte
		if err := rows.Scan(&label, &features); err != nil {
			return nil, fmt.Errorf("repo: scan training sample: %w", err)
		}
		var snap model.Snapshot
		if err := json.Unmarshal(features, &snap); err != nil {
			return nil, fmt.Errorf("repo: decode training snapshot: %w", err)
		}
		out = append(out, TrainingSample{Label: label, Snapshot: snap})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("repo: training samples for user %d: %w", userID, err)
	}
	return out, nil
}

// ModelVersion returns the user's active model version.
func (r *Repo) ModelVersion(ctx context.Context, userID int64) (int, error) {
	var v int
	if err := r.pool.QueryRow(ctx,
		`SELECT active_model_version FROM users WHERE user_id = $1`, userID).Scan(&v); err != nil {
		return 0, fmt.Errorf("repo: model version of user %d: %w", userID, err)
	}
	return v, nil
}

// ReplaceWeights writes a new generation and flips the user's active version in one
// transaction, so the scorer never reads half-written weights. Older generations
// that no delivery references are pruned.
func (r *Repo) ReplaceWeights(ctx context.Context, userID int64, version int, rows []WeightRow) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("repo: weights begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	for _, row := range rows {
		if _, err := tx.Exec(ctx,
			`INSERT INTO model_weights (user_id, feature, value, ups, downs, model_version)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 ON CONFLICT (user_id, feature, value, model_version)
			 DO UPDATE SET ups = EXCLUDED.ups, downs = EXCLUDED.downs, updated_at = now()`,
			userID, row.Feature, row.Value, row.Ups, row.Downs, version); err != nil {
			return fmt.Errorf("repo: insert weight %s=%s: %w", row.Feature, row.Value, err)
		}
	}

	if _, err := tx.Exec(ctx,
		`UPDATE users SET active_model_version = $2 WHERE user_id = $1`, userID, version); err != nil {
		return fmt.Errorf("repo: flip active model version: %w", err)
	}

	// Prune generations no delivery still refers to.
	if _, err := tx.Exec(ctx,
		`DELETE FROM model_weights w
		  WHERE w.user_id = $1
		    AND w.model_version <> $2
		    AND NOT EXISTS (
		          SELECT 1 FROM deliveries d
		           WHERE d.user_id = w.user_id AND d.model_version = w.model_version)`,
		userID, version); err != nil {
		return fmt.Errorf("repo: prune old weights: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("repo: weights commit: %w", err)
	}
	return nil
}

// ActiveWeights returns the user's current generation.
func (r *Repo) ActiveWeights(ctx context.Context, userID int64) (int, []WeightRow, error) {
	version, err := r.ModelVersion(ctx, userID)
	if err != nil {
		return 0, nil, err
	}
	if version == 0 {
		return 0, nil, nil
	}
	rows, err := r.pool.Query(ctx,
		`SELECT feature, value, ups, downs
		   FROM model_weights
		  WHERE user_id = $1 AND model_version = $2
		  ORDER BY feature, value`, userID, version)
	if err != nil {
		return 0, nil, fmt.Errorf("repo: active weights: %w", err)
	}
	defer rows.Close()

	var out []WeightRow
	for rows.Next() {
		var w WeightRow
		if err := rows.Scan(&w.Feature, &w.Value, &w.Ups, &w.Downs); err != nil {
			return 0, nil, fmt.Errorf("repo: scan weight: %w", err)
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return 0, nil, fmt.Errorf("repo: active weights: %w", err)
	}
	return version, out, nil
}

// ScoredSample is a rated delivery with the score it was shown at, which is what
// out-of-sample agreement needs.
type ScoredSample struct {
	RatedAt time.Time
	Score   float64
	Label   int
}

// ScoredRatedDeliveries returns scored and rated deliveries since a date.
func (r *Repo) ScoredRatedDeliveries(ctx context.Context, userID int64, since time.Time) ([]ScoredSample, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT ra.created_at, d.score, ra.label
		   FROM ratings ra
		   JOIN deliveries d
		     ON d.user_id = ra.user_id AND d.listing_id = ra.listing_id
		  WHERE ra.user_id = $1 AND ra.created_at >= $2 AND d.score IS NOT NULL
		  ORDER BY ra.created_at`, userID, since)
	if err != nil {
		return nil, fmt.Errorf("repo: scored samples: %w", err)
	}
	defer rows.Close()

	var out []ScoredSample
	for rows.Next() {
		var s ScoredSample
		if err := rows.Scan(&s.RatedAt, &s.Score, &s.Label); err != nil {
			return nil, fmt.Errorf("repo: scan scored sample: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("repo: scored samples: %w", err)
	}
	return out, nil
}
