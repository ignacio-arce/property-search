package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"zonapropbot/internal/config"
	"zonapropbot/internal/db"
	"zonapropbot/internal/digest"
	"zonapropbot/internal/fetch"
	"zonapropbot/internal/repo"
	"zonapropbot/internal/telegram"
)

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

	run := func() {
		start := time.Now()
		sent, err := runner.RunAll(ctx)
		if err != nil {
			logger.Printf("cycle aborted: %v", err)
			return
		}
		logger.Printf("cycle done in %s: %d sent", time.Since(start).Round(time.Millisecond), sent)
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
