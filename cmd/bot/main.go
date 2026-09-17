package main

import (
	"context"
	"fmt"
	"log"

	"os"
	"os/signal"
	"syscall"
	"time"

	"zonapropbot/internal/chat"
	"zonapropbot/internal/config"
	"zonapropbot/internal/db"
	"zonapropbot/internal/digest"
	"zonapropbot/internal/fetch"
	"zonapropbot/internal/repo"
	"zonapropbot/internal/score"
	"zonapropbot/internal/telegram"
)

// pollerHolder identifies this process for the Telegram poll lease.
func pollerHolder() string {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	return fmt.Sprintf("%s:%d", host, os.Getpid())
}

// imageDownloader adapts the fetch client to the notifier's image fetcher, so
// photos go through the same TLS fingerprint and proxy as page fetches.
type imageDownloader struct {
	fc *fetch.Client
}

func (d imageDownloader) Fetch(ctx context.Context, u string) ([]byte, error) {
	res, err := d.fc.Fetch(ctx, u)
	if err != nil {
		return nil, err
	}
	return res.Body, nil
}

func main() {
	cfg, err := config.FromEnv()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := log.New(os.Stdout, "", log.LstdFlags)

	pool, err := db.Open(ctx, cfg.DatabaseURL(), db.Options{Logger: logger})
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	seed := db.SeedInput{ChatID: cfg.SeedChatID}
	for _, u := range cfg.SeedURLs {
		seed.URLs = append(seed.URLs, db.SeedURL{Label: u.Label, URL: u.URL})
	}
	if err := db.Seed(ctx, pool, seed); err != nil {
		log.Fatalf("seed: %v", err)
	}

	fc := fetch.New(cfg)
	nt := telegram.New(cfg, imageDownloader{fc}, os.Stdout)
	runner := &digest.Runner{
		Repo:     repo.New(pool),
		Fetcher:  fc,
		Notifier: nt,
		Logger:   logger,
	}

	logger.Printf("bot: flaresolverr=%s proxy=%s rate=%s (dry-run=%v)",
		onOff(cfg.FlareSolverrURL), onOff(cfg.ZonapropProxy), cfg.FetchRateLimit, cfg.TelegramBotToken == "")

	// Retrain each active user's model from their ratings. V4.2 turns this into a
	// nightly job; doing it at startup keeps the model fresh until then.
	rp := repo.New(pool)
	activeUsers, err := rp.ListActiveUsers(ctx)
	if err != nil {
		log.Fatalf("list active users: %v", err)
	}
	for _, u := range activeUsers {
		version, err := score.Retrain(ctx, rp, u.UserID)
		if err != nil {
			logger.Printf("retrain user %d: %v", u.UserID, err)
			continue
		}
		logger.Printf("retrain user %d: model v%d", u.UserID, version)
	}

	run := func() {
		start := time.Now()
		sent, err := runner.RunAll(ctx)
		if err != nil {
			logger.Printf("cycle aborted: %v", err)
			return
		}
		logger.Printf("cycle done in %s: %d sent", time.Since(start).Round(time.Millisecond), sent)
	}

	// The poller runs alongside the digest loop: one consumes updates, the other
	// produces deliveries. They share the process but not their state.
	if cfg.TelegramBotToken != "" {
		poller := &chat.Poller{
			Repo:   repo.New(pool),
			API:    nt,
			Logger: logger,
			Holder: pollerHolder(),
		}
		go func() {
			if err := poller.Run(ctx); err != nil {
				logger.Printf("chat: poller stopped: %v", err)
			}
		}()
	} else {
		logger.Printf("chat: no TELEGRAM_BOT_TOKEN, skipping inbound polling")
	}

	run()

	ticker := time.NewTicker(cfg.CheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Println("signal received, shutting down")
			return
		case <-ticker.C:
			run()
		}
	}
}

func onOff(v string) string {
	if v == "" {
		return "off"
	}
	return "on"
}
