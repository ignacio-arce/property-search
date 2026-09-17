package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"zonapropbot/internal/bot"
	"zonapropbot/internal/config"
	"zonapropbot/internal/fetch"
	"zonapropbot/internal/store"
	"zonapropbot/internal/telegram"
)

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

	st, err := store.Open(cfg.DataDir)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	fc := fetch.New(cfg)
	nt := telegram.New(cfg, imageDownloader{fc: fc}, os.Stdout)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := log.New(os.Stdout, "", log.LstdFlags)
	logger.Printf("zonaprop-bot: polling %d url(s) every %s (flaresolverr=%s proxy=%s)",
		len(cfg.SearchURLs), cfg.CheckInterval, onOff(cfg.FlareSolverrURL), onOff(cfg.ZonapropProxy))

	run := func() {
		start := time.Now()
		if err := bot.RunOnce(ctx, cfg, fc, nt, st, logger); err != nil {
			logger.Printf("cycle aborted: %v", err)
			return
		}
		logger.Printf("cycle done in %s", time.Since(start).Round(time.Millisecond))
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
