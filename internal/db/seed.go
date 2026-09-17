package db

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"zonapropbot/internal/searchurl"
)

// SeedURL is one preseeded search URL with its short label.
type SeedURL struct {
	Label string
	URL   string
}

// SeedInput is the data the seeds need, sourced from the environment so a
// developer can exercise the pipeline before onboarding exists.
type SeedInput struct {
	ChatID string
	URLs   []SeedURL
}

// globalSettings are the defaults inserted on every startup. They are inserted
// with ON CONFLICT DO NOTHING, so a value the operator changed survives restarts.
var globalSettings = map[string]string{
	"daily_hour": "09:00",
	"max_daily":  "15",
}

// Seed inserts the global settings defaults and, when configured, the operator's
// user together with its search URLs. It is idempotent.
func Seed(ctx context.Context, pool *pgxpool.Pool, in SeedInput) error {
	if err := seedSettings(ctx, pool); err != nil {
		return err
	}
	if in.ChatID == "" || len(in.URLs) == 0 {
		return nil
	}
	return seedUser(ctx, pool, in)
}

func seedSettings(ctx context.Context, pool *pgxpool.Pool) error {
	for key, value := range globalSettings {
		_, err := pool.Exec(ctx,
			`INSERT INTO settings (key, value) VALUES ($1, $2)
			 ON CONFLICT (key) DO NOTHING`, key, value)
		if err != nil {
			return fmt.Errorf("seed: settings %q: %w", key, err)
		}
	}
	return nil
}

func seedUser(ctx context.Context, pool *pgxpool.Pool, in SeedInput) error {
	userID, err := parseChatID(in.ChatID)
	if err != nil {
		return err
	}

	// Seed URLs are trusted: the operator pasted them and they are the ones the
	// bot was built to watch. The validator (V5.3) will decide whether operator
	// seeds need re-checking.
	_, err = pool.Exec(ctx,
		`INSERT INTO users (user_id, chat_id, state, onboarded_at)
		 VALUES ($1, $1, 'ready', now())
		 ON CONFLICT (user_id) DO NOTHING`, userID)
	if err != nil {
		return fmt.Errorf("seed: user %d: %w", userID, err)
	}

	for _, u := range in.URLs {
		norm, err := searchurl.Normalize(u.URL)
		if err != nil {
			return fmt.Errorf("seed: url %q: %w", u.URL, err)
		}
		_, err = pool.Exec(ctx,
			`INSERT INTO search_urls (user_id, label, url, url_norm, validation_status)
			 VALUES ($1, $2, $3, $4, 'valid')
			 ON CONFLICT (user_id, url_norm) DO NOTHING`, userID, u.Label, u.URL, norm)
		if err != nil {
			return fmt.Errorf("seed: url %q: %w", u.URL, err)
		}
	}
	return nil
}

func parseChatID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("seed: SEED_CHAT_ID %q is not a numeric chat id: %w", raw, err)
	}
	return id, nil
}
