package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	// The image is FROM scratch with no zoneinfo, so without this import
	// time.LoadLocation falls back to UTC and 09:00 local fires at 06:00.
	_ "time/tzdata"

	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"zonapropbot/internal/chat"
	"zonapropbot/internal/config"
	"zonapropbot/internal/contact"
	"zonapropbot/internal/db"
	"zonapropbot/internal/digest"
	"zonapropbot/internal/fetch"
	"zonapropbot/internal/logging"
	"zonapropbot/internal/repo"
	"zonapropbot/internal/scheduler"
	"zonapropbot/internal/score"
	"zonapropbot/internal/telegram"
	"zonapropbot/internal/validate"
)

// validatorInterval is how often the deep validator checks searches that are not
// watched yet. It is short because it is the only path that can move a search from
// "pending" to "watched", so it must not wait for the daily digest.
const validatorInterval = 10 * time.Minute

func main() {
	cfg, err := config.FromEnv()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := logging.New(os.Stdout, cfg.LogLevel)
	slog.SetDefault(logger)
	// Temporary bridge while the remaining packages still accept a *log.Logger.
	// It is removed once they are migrated to slog.
	legacy := log.New(os.Stdout, "", log.LstdFlags)

	pool, err := db.Open(ctx, cfg.DatabaseURL(), db.Options{Logger: logger})
	if err != nil {
		fatal(logger, "db", err)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool); err != nil {
		fatal(logger, "migrate", err)
	}
	if err := db.Seed(ctx, pool, seedInput(cfg)); err != nil {
		fatal(logger, "seed", err)
	}

	app := newApp(cfg, pool, logger, legacy)
	app.logStartup()
	if err := app.retrainAll(ctx); err != nil {
		fatal(logger, "retrain", err)
	}
	app.startValidator(ctx)
	app.startPoller(ctx)

	loc, err := time.LoadLocation(cfg.ScheduleTZ)
	if err != nil {
		fatal(logger, "schedule tz", err)
	}
	hour, minute := cfg.DailyHourParts()

	if cfg.RunOnStart {
		logger.Info("digest: RUN_ON_START is on, running one cycle at boot")
		app.runDigest(ctx, loc)
	}

	logger.Info("digest: daily schedule", "at", cfg.DailyHour, "tz", cfg.ScheduleTZ)
	if err := scheduler.Run(ctx, loc, hour, minute,
		func(runCtx context.Context) { app.runDigest(runCtx, loc) }, logger, time.Now); err != nil {
		logger.Warn("digest: scheduler stopped", "err", err)
	}

	// Cancel and wait for the poller before returning: its deferred release of the
	// poll lease needs a live pool, and the deferred pool.Close runs after this.
	stop()
	app.pollerWG.Wait()
	logger.Info("signal received, shutting down")
}

// fatal logs a startup error at ERROR and exits. After the logger exists it
// replaces log.Fatalf, so the whole run shares one format.
func fatal(logger *slog.Logger, msg string, err error) {
	logger.Error(msg, "err", err)
	os.Exit(1)
}

// app groups the wiring the background jobs share, so each job reads as a named
// step in main instead of an inline block.
type app struct {
	cfg     *config.Config
	repo    *repo.Repo
	fetcher *fetch.Client
	notify  *telegram.Notifier
	digest  *digest.Runner
	logger  *slog.Logger
	// legacy is the pre-migration *log.Logger still expected by packages that have
	// not moved to slog yet. It disappears when they do.
	legacy *log.Logger

	// pollerWG tracks the Telegram poller so shutdown can wait for it.
	pollerWG sync.WaitGroup
}

func newApp(cfg *config.Config, pool *pgxpool.Pool, logger *slog.Logger, legacy *log.Logger) *app {
	fetcher := fetch.New(cfg)
	notifier := telegram.New(cfg, imageDownloader{fc: fetcher}, os.Stdout)
	repository := repo.New(pool)
	return &app{
		cfg:     cfg,
		repo:    repository,
		fetcher: fetcher,
		notify:  notifier,
		digest: &digest.Runner{
			Repo:      repository,
			Fetcher:   fetcher,
			Notifier:  notifier,
			Logger:    legacy,
			MaxPerRun: cfg.MaxDaily,
		},
		logger: logger,
		legacy: legacy,
	}
}

func (a *app) logStartup() {
	a.logger.Info("bot: startup",
		"flaresolverr", onOff(a.cfg.FlareSolverrURL),
		"proxy", onOff(a.cfg.ZonapropProxy),
		"rate", a.cfg.FetchRateLimit,
		"dry_run", a.cfg.TelegramBotToken == "")
}

// retrainAll rebuilds every active user's model at boot, until the nightly job
// exists. One user's failure must not stop the others, but a failure to read the
// user list at all is fatal: the database is not usable.
func (a *app) retrainAll(ctx context.Context) error {
	users, err := a.repo.ListActiveUsers(ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		version, err := score.Retrain(ctx, a.repo, u.UserID)
		if err != nil {
			a.logger.Warn("retrain failed", "user", u.UserID, "err", err)
			continue
		}
		a.logger.Info("retrain done", "user", u.UserID, "model_version", version)
	}
	return nil
}

func (a *app) startValidator(ctx context.Context) {
	validator := &validate.Validator{
		Repo: a.repo, Fetcher: a.fetcher, Notifier: a.notify, Logger: a.legacy,
	}
	go func() {
		ticker := time.NewTicker(validatorInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := validator.RunOnce(ctx); err != nil && ctx.Err() == nil {
					a.logger.Warn("validate: run failed", "err", err)
				}
			}
		}
	}()
}

// startPoller consumes Telegram updates. It shares the process with the digest but
// not its state: one consumes updates, the other produces deliveries.
func (a *app) startPoller(ctx context.Context) {
	if a.cfg.TelegramBotToken == "" {
		a.logger.Info("chat: no TELEGRAM_BOT_TOKEN, skipping inbound polling")
		return
	}
	poller := &chat.Poller{
		Repo:   a.repo,
		API:    a.notify,
		Logger: a.legacy,
		Contacts: &contact.Extractor{
			Repo: a.repo, Fetcher: a.fetcher, Notifier: a.notify, Logger: a.legacy,
		},
		Holder: pollerHolder(),
	}
	a.pollerWG.Add(1)
	go func() {
		defer a.pollerWG.Done()
		if err := poller.Run(ctx); err != nil {
			a.logger.Warn("chat: poller stopped", "err", err)
		}
	}()
}

func (a *app) runDigest(ctx context.Context, loc *time.Location) {
	start := time.Now()
	sent, err := a.digest.RunDaily(ctx, time.Now().In(loc))
	if err != nil {
		a.logger.Error("digest: cycle aborted", "err", err)
		return
	}
	a.logger.Info("digest: cycle done", "sent", sent, "ms", time.Since(start).Milliseconds())
}

func seedInput(cfg *config.Config) db.SeedInput {
	seed := db.SeedInput{ChatID: cfg.SeedChatID}
	for _, u := range cfg.SeedURLs {
		seed.URLs = append(seed.URLs, db.SeedURL{Label: u.Label, URL: u.URL})
	}
	return seed
}

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

func onOff(v string) string {
	if v == "" {
		return "off"
	}
	return "on"
}
