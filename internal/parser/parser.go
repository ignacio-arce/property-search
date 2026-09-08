package parser

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"zonapropbot/internal/model"
)

// Parse extracts property listings from a Zonaprop search page. Each card is
// identified by the div[data-to-posting] element (the same selector used by the
// original Python bot). siteBase is prepended to relative hrefs to build the
// canonical absolute URL from which the stable ID is derived.
func Parse(html []byte, siteBase string) ([]model.Listing, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(html))
	if err != nil {
		return nil, fmt.Errorf("parsing html: %w", err)
	}

	var listings []model.Listing
	doc.Find("div[data-to-posting]").Each(func(_ int, card *goquery.Selection) {
		href, ok := card.Attr("data-to-posting")
		if !ok || href == "" {
			return
		}
		url := href
		if !strings.HasPrefix(href, "http://") && !strings.HasPrefix(href, "https://") {
			url = strings.TrimSuffix(siteBase, "/") + href
		}
		listings = append(listings, model.Listing{
			ID:        sha1Hex(url),
			URL:       url,
			Title:     text(card.Find("[data-qa=POSTING_CARD_DESCRIPTION] a").First()),
			Price:     text(card.Find("[data-qa=POSTING_CARD_PRICE]").First()),
			M2:        feature(card, "m²", "m2"),
			Ambientes: feature(card, "amb."),
			Location:  text(card.Find("[data-qa=POSTING_CARD_LOCATION]").First()),
			PhotoURL:  attr(card.Find("[data-qa=POSTING_CARD_GALLERY] img").First(), "src"),
		})
	})
	return listings, nil
}

func feature(card *goquery.Selection, needles ...string) string {
	var found string
	card.Find("[data-qa=POSTING_CARD_FEATURES] span").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		txt := strings.TrimSpace(s.Text())
		for _, n := range needles {
			if strings.Contains(strings.ToLower(txt), strings.ToLower(n)) {
				found = txt
				return false
			}
		}
		return true
	})
	return found
}

func text(sel *goquery.Selection) string {
	return strings.Join(strings.Fields(sel.Text()), " ")
}

func attr(sel *goquery.Selection, name string) string {
	v, _ := sel.Attr(name)
	return v
}

func sha1Hex(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}
