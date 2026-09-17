// Command probe fetches search URLs and reports what the parser sees. It is the
// risk probe: it validates the fetch path, the anti-bot behaviour and the card
// selectors against the real site before anything depends on them.
//
// URLs come from the command line, not from the environment: the bot no longer
// reads a global search list, so making the probe take arguments keeps it honest
// about what it actually fetched.
//
//	go run ./cmd/probe -out fixtures/probe_capture.html \
//	  'https://www.zonaprop.com.ar/departamentos-venta-...-orden-publicado-descendente.html'
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"zonapropbot/internal/config"
	"zonapropbot/internal/fetch"
	"zonapropbot/internal/parser"
)

func main() {
	out := flag.String("out", "data/probe_capture.html", "where to write the fetched HTML (empty to skip)")
	flag.Parse()

	if flag.NArg() == 0 {
		log.Fatal("usage: probe [-out path] <search-url> [more-urls...]")
	}

	cfg, err := config.FromEnv()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	client := fetch.New(cfg)
	ctx := context.Background()

	for _, u := range flag.Args() {
		res, err := client.Fetch(ctx, u)
		if err != nil {
			// The classification is the useful part: "blocked" means Cloudflare
			// refused, which is a different problem from the site being down.
			kind := "unknown"
			if k, ok := fetch.KindOf(err); ok {
				kind = k.String()
			}
			log.Printf("URL %s: FAILED (kind=%s): %v", u, kind, err)
			continue
		}

		listings, stats, err := parser.ParseWithStats(res.Body, u)
		if err != nil {
			log.Printf("  parse: %v", err)
			continue
		}
		fmt.Printf("URL %s\n  mode=%s status=%d bytes=%d cards=%d skipped_type=%d skipped_noid=%d parsed=%d\n",
			u, res.Mode, res.Status, len(res.Body), stats.Cards, stats.SkippedType, stats.SkippedNoID, len(listings))
		for i, l := range listings {
			if i >= 5 {
				fmt.Printf("  ... (%d more)\n", len(listings)-5)
				break
			}
			fmt.Printf("  - id=%s [%s] %s | %s | %s | %s\n",
				l.ZonapropID, l.PriceLabel(), l.Title, l.SizeLabel(), l.Location, l.Operation)
		}

		if *out == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
			log.Printf("  mkdir: %v", err)
			continue
		}
		if err := os.WriteFile(*out, res.Body, 0o644); err != nil {
			log.Printf("  write capture: %v", err)
			continue
		}
		fmt.Printf("  captured to %s\n", *out)
	}
}
