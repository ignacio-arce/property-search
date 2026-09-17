package parser

import (
	"bytes"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"zonapropbot/internal/model"
)

// Stats reports what a parse saw. Skipped cards are counted rather than silently
// dropped: a DOM change must be distinguishable from a genuinely empty search,
// and those two outcomes have opposite handling in the validation layer.
type Stats struct {
	Cards       int // elements carrying data-to-posting
	SkippedType int // not a PROPERTY card
	SkippedNoID int // PROPERTY card without data-id
}

// Constraints on the covered/total surface, in square metres. The real page
// contains m²=1 and m²=12500 alongside a healthy 58-141 range; without the guard
// a 1 m² listing produces a price-per-m² of ~150.000 and owns the top of the
// ranking.
const (
	minM2 = 10
	maxM2 = 1000
)

var (
	numberRe   = regexp.MustCompile(`\d[\d.,]*`)
	desdeRe    = regexp.MustCompile(`(?i)^\s*desde\s+`)
	spaceRe    = regexp.MustCompile(`\s+`)
	macrozones = map[string]bool{
		"gba norte":              true,
		"gba sur":                true,
		"gba oeste":              true,
		"capital federal":        true,
		"ciudad de buenos aires": true,
	}
)

// Parse extracts property listings. It is ParseWithStats without the counters.
func Parse(html []byte, siteBase string) ([]model.Listing, error) {
	listings, _, err := ParseWithStats(html, siteBase)
	return listings, err
}

// ParseWithStats extracts property listings from a Zonaprop search page.
//
// siteBase is used only for its origin: card hrefs are root-relative, and callers
// pass the full search URL, so concatenating it directly produced URLs like
// "...-200000-dolar.html/propiedades/clasificado/...". The operation (venta or
// alquiler) is read from siteBase, which is authoritative for the whole page.
func ParseWithStats(html []byte, siteBase string) ([]model.Listing, Stats, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(html))
	if err != nil {
		return nil, Stats{}, fmt.Errorf("parsing html: %w", err)
	}

	origin := originOf(siteBase)
	operation := operationOf(siteBase)

	var stats Stats
	var listings []model.Listing

	doc.Find("div[data-to-posting]").Each(func(_ int, card *goquery.Selection) {
		stats.Cards++

		// v1 handles PROPERTY cards only. Development projects render a different
		// price format and lack size and room features.
		if card.AttrOr("data-posting-type", "") != "PROPERTY" {
			stats.SkippedType++
			return
		}

		zonapropID := strings.TrimSpace(card.AttrOr("data-id", ""))
		if zonapropID == "" {
			stats.SkippedNoID++
			return
		}

		features := featureSpans(card)
		m2Tot, m2Cub := sizes(features)
		priceAmount, currency := parsePrice(text(card.Find("[data-qa=POSTING_CARD_PRICE]").First()))
		expensas, _ := parseAmount(text(card.Find("[data-qa=expensas]").First()))

		basis := ""
		switch {
		case m2Tot != nil:
			basis = "tot"
		case m2Cub != nil:
			basis = "cub"
		}

		listings = append(listings, model.Listing{
			ZonapropID:   zonapropID,
			CanonicalURL: canonical(origin, card.AttrOr("data-to-posting", "")),
			Title:        text(card.Find("[data-qa=POSTING_CARD_DESCRIPTION] a").First()),
			Location:     text(card.Find("[data-qa=POSTING_CARD_LOCATION]").First()),
			PhotoURL:     photoURL(card),
			PriceAmount:  priceAmount,
			Currency:     currency,
			Expensas:     expensas,
			M2Tot:        m2Tot,
			M2Cub:        m2Cub,
			M2Basis:      basis,
			Rooms:        featureInt(features, "amb."),
			Dorm:         featureInt(features, "dorm."),
			Banos:        featureInt(features, "baño"),
			Operation:    operation,
		})
	})

	return listings, stats, nil
}

// originOf reduces a URL to scheme://host. Callers pass the full search URL, so
// using it whole as a base is what mangled the card hrefs.
func originOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// canonical resolves a card href against the site origin and strips the query,
// which carries per-position tracking (n_src, n_pg, n_pos, n_search_id).
func canonical(origin, href string) string {
	base, err := url.Parse(origin)
	if err != nil {
		return href
	}
	ref, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return href
	}
	u := base.ResolveReference(ref)
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// operationOf reads venta/alquiler from the URL. The search path is authoritative
// for every card on the page.
func operationOf(raw string) string {
	path := strings.ToLower(raw)
	switch {
	case strings.Contains(path, "alquiler"):
		return "alquiler"
	case strings.Contains(path, "venta"):
		return "venta"
	default:
		return ""
	}
}

// parsePrice handles "USD 144.000", "$ 150.000.000", "Desde USD 120.000" and
// "Consultar precio". The dot is a thousands separator (es-AR), so stripping
// non-digits blindly would turn "USD 83.900" into 83900 but "$ 150.000.000" into
// an ARS amount scored against a USD band — a 1000x outlier.
func parsePrice(raw string) (*int64, string) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, ""
	}
	if strings.Contains(strings.ToLower(s), "consultar") {
		return nil, ""
	}
	s = strings.TrimSpace(desdeRe.ReplaceAllString(s, ""))

	currency := ""
	switch {
	case strings.HasPrefix(strings.ToUpper(s), "USD"):
		currency = "USD"
	case strings.HasPrefix(s, "$"):
		currency = "ARS"
	}

	amount, ok := parseAmount(s)
	if !ok {
		return nil, ""
	}
	return amount, currency
}

// parseAmount extracts the first number in s, ignoring separators and any
// surrounding text ("$ 180.000 Expensas" -> 180000).
func parseAmount(s string) (*int64, bool) {
	match := numberRe.FindString(s)
	if match == "" {
		return nil, false
	}
	digits := strings.NewReplacer(".", "", ",", "").Replace(match)
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return nil, false
	}
	return &n, true
}

func featureSpans(card *goquery.Selection) []string {
	var out []string
	card.Find("[data-qa=POSTING_CARD_FEATURES] span").Each(func(_ int, s *goquery.Selection) {
		if t := text(s); t != "" {
			out = append(out, t)
		}
	})
	return out
}

// featureInt finds the first feature span containing needle and returns its
// number. The needle for bathrooms is "baño": writing "banio" does not match the
// real DOM and would leave the feature permanently absent.
func featureInt(features []string, needle string) *int {
	for _, f := range features {
		if !strings.Contains(strings.ToLower(f), needle) {
			continue
		}
		if n, ok := parseAmount(f); ok {
			v := int(*n)
			return &v
		}
	}
	return nil
}

// sizes extracts total and covered surface separately. The real card only ever
// shows "m² tot.", but other searches expose both, and blending them makes the
// values incomparable.
func sizes(features []string) (tot, cub *float64) {
	for _, f := range features {
		lower := strings.ToLower(f)
		if !strings.Contains(lower, "m²") && !strings.Contains(lower, "m2") {
			continue
		}
		match := numberRe.FindString(f)
		if match == "" {
			continue
		}
		v, err := strconv.ParseFloat(strings.NewReplacer(".", "", ",", ".").Replace(match), 64)
		if err != nil || v < minM2 || v > maxM2 {
			continue
		}
		if strings.Contains(lower, "cub") {
			if cub == nil {
				value := v
				cub = &value
			}
			continue
		}
		if tot == nil {
			value := v
			tot = &value
		}
	}
	return tot, cub
}

// Partido extracts the locality that matters for scoring. The real Location is
// "barrio, partido" ("Florida, Vicente López"), but some values end in a
// macrozone ("San Isidro, GBA Norte"), where the partido is the first segment.
func Partido(location string) string {
	parts := strings.Split(location, ",")
	if len(parts) == 0 {
		return ""
	}
	last := strings.TrimSpace(parts[len(parts)-1])
	if len(parts) > 1 && macrozones[strings.ToLower(last)] {
		return strings.TrimSpace(parts[0])
	}
	return last
}

func photoURL(card *goquery.Selection) string {
	img := card.Find("[data-qa=POSTING_CARD_GALLERY] img").First()
	if v := attr(img, "src"); v != "" {
		return v
	}
	// Lazy-loaded images put the real URL in data-src.
	return attr(img, "data-src")
}

func text(sel *goquery.Selection) string {
	return strings.TrimSpace(spaceRe.ReplaceAllString(sel.Text(), " "))
}

func attr(sel *goquery.Selection, name string) string {
	v, _ := sel.Attr(name)
	return strings.TrimSpace(v)
}
