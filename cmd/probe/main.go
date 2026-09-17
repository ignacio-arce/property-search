package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"zonapropbot/internal/config"
	"zonapropbot/internal/fetch"
	"zonapropbot/internal/parser"
)

func main() {
	cfg, err := config.FromEnv()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	client := fetch.New(cfg)
	ctx := context.Background()
	for _, u := range cfg.SearchURLs {
		res, err := client.Fetch(ctx, u)
		if err != nil {
			log.Printf("URL %s: FAILED: %v", u, err)
			continue
		}
		posting := bytes.Count(res.Body, []byte("data-to-posting"))
		price := bytes.Count(res.Body, []byte("POSTING_CARD_PRICE"))
		fmt.Printf("URL %s\n  mode=%s bytes=%d data-to-posting=%d POSTING_CARD_PRICE=%d\n",
			u, res.Mode, len(res.Body), posting, price)

		listings, err := parser.Parse(res.Body, u)
		if err != nil {
			log.Printf("  parse: %v", err)
			continue
		}
		fmt.Printf("  parsed=%d\n", len(listings))
		for i, l := range listings {
			if i >= 3 {
				fmt.Printf("  ... (%d more)\n", len(listings)-3)
				break
			}
			fmt.Printf("  - [%s] %s | %s | %s | %s\n",
				l.PriceLabel(), l.Title, l.SizeLabel(), l.Location, l.PhotoURL)
		}

		out := "fixtures/probe_capture.html"
		if cfg.DataDir != "" {
			out = filepath.Join(cfg.DataDir, "probe_capture.html")
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			log.Printf("mkdir: %v", err)
			continue
		}
		if err := os.WriteFile(out, res.Body, 0o644); err != nil {
			log.Printf("write fixture: %v", err)
			continue
		}
		fmt.Printf("  fixture written to %s\n", out)
	}
}
