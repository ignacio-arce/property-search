package model

// Listing is a single property posting extracted from a Zonaprop search page.
// ID is derived from the canonical absolute URL so it stays stable across
// runs (same scheme as the original Python bot, which hashes the href).
type Listing struct {
	ID        string
	URL       string
	Title     string
	Price     string
	M2        string
	Ambientes string
	Location  string
	PhotoURL  string
}
