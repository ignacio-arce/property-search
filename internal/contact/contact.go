// Package contact extracts the advertiser's phone from a listing's detail page.
//
// The probe found the phone in a server-rendered JSON-LD block (an Apartment
// object), so no click, no internal API and no extra JavaScript is involved. The
// same block carries structured fields the card only exposes as scraped text.
//
// There is no email: the detail page has only placeholders and Zonaprop's own
// corporate addresses, so this feature sends a phone or nothing.
package contact

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"zonapropbot/internal/model"
)

type jsonLD struct {
	Type      string          `json:"@type"`
	Telephone string          `json:"telephone"`
	Email     string          `json:"email"`
	Address   json.RawMessage `json:"address"`
}

type postalAddress struct {
	StreetAddress   string `json:"streetAddress"`
	AddressRegion   string `json:"addressRegion"`
	AddressLocality string `json:"addressLocality"`
}

var emailRe = regexp.MustCompile(`^[\w.+-]+@[\w-]+\.[\w.]{2,}$`)

// FromJSONLD extracts contact details from the page's structured data. It returns
// ok=false when no listing block is present, the normal path for a page that
// failed to render or a block type we do not handle.
func FromJSONLD(html []byte) (model.Contact, bool) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(html))
	if err != nil {
		return model.Contact{}, false
	}

	var out model.Contact
	found := false
	doc.Find(`script[type="application/ld+json"]`).EachWithBreak(func(_ int, s *goquery.Selection) bool {
		var block jsonLD
		if err := json.Unmarshal([]byte(strings.TrimSpace(s.Text())), &block); err != nil {
			return true // keep looking; one bad block must not stop the scan
		}
		switch block.Type {
		case "Apartment", "House", "SingleFamilyResidence":
		default:
			return true
		}

		found = true
		out.Phone = normalizePhone(block.Telephone)
		if emailRe.MatchString(block.Email) {
			out.Email = block.Email
		}
		if len(block.Address) > 0 {
			var addr postalAddress
			if err := json.Unmarshal(block.Address, &addr); err == nil {
				out.StreetAddress = strings.TrimSpace(addr.StreetAddress)
				// addressRegion is the neighbourhood on the real page, which is more
				// precise than the card's "barrio, partido".
				out.Neighbourhood = strings.TrimSpace(addr.AddressRegion)
			}
		}
		return false
	})
	return out, found
}

// normalizePhone keeps digits and a leading plus, and rejects anything too short
// to be a phone number.
func normalizePhone(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var b strings.Builder
	for i, r := range trimmed {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && i == 0:
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(strings.TrimPrefix(out, "+")) < 8 {
		return ""
	}
	return out
}
