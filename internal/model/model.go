package model

import (
	"strconv"
	"strings"
)

// Listing is a single property posting extracted from a search page.
//
// The fields are typed, not strings. The previous version stored "128 m² tot."
// and "220 m² cub." in the same string column, so the scorer could not tell
// whether 220 was bigger than 128 or just measured differently, and an absent
// value became "0" instead of NULL — which put a priceless listing in the
// cheapest bucket. Pointers keep "absent" distinct from "zero".
type Listing struct {
	// ZonapropID is the card's data-id. It is the dedup key: the card href carries
	// n_pg/n_pos tracking parameters that change whenever the posting moves slot.
	ZonapropID string
	// CanonicalURL is origin + path, with the query stripped.
	CanonicalURL string

	Title    string
	Location string
	PhotoURL string

	PriceAmount *int64
	Currency    string // "USD" | "ARS" | ""
	Expensas    *int64 // monthly, always ARS

	M2Tot   *float64
	M2Cub   *float64
	M2Basis string // "tot" | "cub" | ""

	Rooms *int // parsed for display; measured constant on the operator's search
	Dorm  *int
	Banos *int

	// Operation is "venta" | "alquiler" | "". It is a partition key for the model,
	// not a scoring feature: an ARS rental and a USD sale are not comparable.
	Operation string
}

// PriceLabel renders the price for display, e.g. "USD 144.000". Empty when the
// price is absent.
func (l Listing) PriceLabel() string {
	if l.PriceAmount == nil {
		return ""
	}
	return strings.TrimSpace(l.Currency + " " + formatThousands(*l.PriceAmount))
}

// SizeLabel renders the size line for display, e.g. "69 m² · 3 amb.".
func (l Listing) SizeLabel() string {
	m2 := l.M2Tot
	if m2 == nil {
		m2 = l.M2Cub
	}
	var parts []string
	if m2 != nil {
		// The basis is shown because it matters: the operator's search filters on
		// covered m² while the card usually shows total m², so hiding the suffix
		// would make the number ambiguous.
		label := strconv.FormatFloat(*m2, 'f', -1, 64) + " m²"
		if l.M2Basis != "" {
			label += " " + l.M2Basis + "."
		}
		parts = append(parts, label)
	}
	if l.Rooms != nil {
		parts = append(parts, strconv.Itoa(*l.Rooms)+" amb.")
	}
	return strings.Join(parts, " · ")
}

// ExpensasLabel renders the monthly expenses, e.g. "$ 180.000". Empty when absent.
func (l Listing) ExpensasLabel() string {
	if l.Expensas == nil {
		return ""
	}
	return "$ " + formatThousands(*l.Expensas)
}

// formatThousands renders 144000 as "144.000", the es-AR convention.
func formatThousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteByte(s[i])
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
