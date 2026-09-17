package main

import (
	"context"
	"fmt"
	"log"
	// The image is FROM scratch with no zoneinfo, so without this import
	// time.LoadLocation falls back to UTC and 09:00 local fires at 06:00.
	_ "time/tzdata"

	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"zonapropbot/internal/chat"
	"zonapropbot/internal/config"
	"zonapropbot/internal/contact"
	"zonapropbot/internal/db"
	"zonapropbot/internal/digest"
	"zonapropbot/internal/fetch"
	"zonapropbot/internal/repo"
	"zonapropbot/internal/scheduler"
	"zonapropbot/internal/score"
	"zonapropbot/internal/telegram"
	"zonapropbot/internal/validate"
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

	// The poller runs in a goroutine and must finish before the pool closes, so its
	// lease release does not fail against a dead connection.
	var pollerWG sync.WaitGroup

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

	rp := repo.New(pool)

	fc := fetch.New(cfg)
	nt := telegram.New(cfg, imageDownloader{fc}, os.Stdout)
	runner := &digest.Runner{
		Repo:      repo.New(pool),
		Fetcher:   fc,
		Notifier:  nt,
		Logger:    logger,
		MaxPerRun: cfg.MaxDaily,
	}

	logger.Printf("bot: flaresolverr=%s proxy=%s rate=%s (dry-run=%v)",
		onOff(cfg.FlareSolverrURL), onOff(cfg.ZonapropProxy), cfg.FetchRateLimit, cfg.TelegramBotToken == "")

	// Retrain each active user's model from their ratings. V4.2 turns this into a
	// nightly job; doing it at startup keeps the model fresh until then.
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

	// The deep validator checks new searches every ten minutes. It runs alongside
	// everything else: it is the only path that can move a search from "pending" to
	// "watched", so it must not depend on the daily digest.
	validator := &validate.Validator{Repo: rp, Fetcher: fc, Notifier: nt, Logger: logger}
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := validator.RunOnce(ctx); err != nil && ctx.Err() == nil {
					logger.Printf("validate: %v", err)
				}
			}
		}
	}()

	// The poller runs alongside the digest loop: one consumes updates, the other
	// produces deliveries. They share the process but not their state.
	if cfg.TelegramBotToken != "" {
		extractor := &contact.Extractor{Repo: rp, Fetcher: fc, Notifier: nt, Logger: logger}
		poller := &chat.Poller{
			Repo:     rp,
			API:      nt,
			Logger:   logger,
			Contacts: extractor,
			Holder:   pollerHolder(),
		}
		pollerWG.Add(1)
		go func() {
			defer pollerWG.Done()
			if err := poller.Run(ctx); err != nil {
				logger.Printf("chat: poller stopped: %v", err)
			}
		}()
	} else {
		logger.Printf("chat: no TELEGRAM_BOT_TOKEN, skipping inbound polling")
	}

	loc, err := time.LoadLocation(cfg.ScheduleTZ)
	if err != nil {
		log.Fatalf("schedule tz: %v", err)
	}
	hour, minute := cfg.DailyHourParts()

	dailyRun := func(runCtx context.Context) {
		start := time.Now()
		sent, err := runner.RunDaily(runCtx, time.Now().In(loc))
		if err != nil {
			logger.Printf("digest: cycle aborted: %v", err)
			return
		}
		logger.Printf("digest: cycle done in %s: %d sent", time.Since(start).Round(time.Millisecond), sent)
	}

	if cfg.RunOnStart {
		logger.Printf("digest: RUN_ON_START is on, running one cycle at boot")
		dailyRun(ctx)
	}

	next := scheduler.NextRun(time.Now(), loc, hour, minute)
	logger.Printf("digest: daily at %s %s (next %s)", cfg.DailyHour, cfg.ScheduleTZ, next.Format(time.RFC3339))
	if err := scheduler.Run(ctx, loc, hour, minute, dailyRun, logger, time.Now); err != nil {
		logger.Printf("digest: scheduler stopped: %v", err)
	}

	// Cancel and wait for the poller before returning: its deferred release of the
	// poll lease needs a live pool, and the deferred pool.Close runs after this.
	stop()
	pollerWG.Wait()
	logger.Println("signal received, shutting down")
}

func onOff(v string) string {
	if v == "" {
		return "off"
	}
	return "on"
}
