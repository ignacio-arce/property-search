package model

// Snapshot is the persisted shape of a listing: the JSON stored in
// listings.features (latest known, for display) and in deliveries.features (the
// exact version the user was shown). Snapshotting at send time matters because a
// seller can change the price later; the model must train on what the user
// actually saw.
type Snapshot struct {
	ZonapropID   string   `json:"zonaprop_id"`
	CanonicalURL string   `json:"canonical_url"`
	Title        string   `json:"title"`
	Location     string   `json:"location"`
	PhotoURL     string   `json:"photo_url"`
	PriceAmount  *int64   `json:"price_amount,omitempty"`
	Currency     string   `json:"currency,omitempty"`
	Expensas     *int64   `json:"expensas,omitempty"`
	M2Tot        *float64 `json:"m2_tot,omitempty"`
	M2Cub        *float64 `json:"m2_cub,omitempty"`
	M2Basis      string   `json:"m2_basis,omitempty"`
	Rooms        *int     `json:"rooms,omitempty"`
	Dorm         *int     `json:"dorm,omitempty"`
	Banos        *int     `json:"banos,omitempty"`
	Operation    string   `json:"operation,omitempty"`
}

// Snapshot converts a listing into its persisted shape.
func (l Listing) Snapshot() Snapshot {
	return Snapshot{
		ZonapropID:   l.ZonapropID,
		CanonicalURL: l.CanonicalURL,
		Title:        l.Title,
		Location:     l.Location,
		PhotoURL:     l.PhotoURL,
		PriceAmount:  l.PriceAmount,
		Currency:     l.Currency,
		Expensas:     l.Expensas,
		M2Tot:        l.M2Tot,
		M2Cub:        l.M2Cub,
		M2Basis:      l.M2Basis,
		Rooms:        l.Rooms,
		Dorm:         l.Dorm,
		Banos:        l.Banos,
		Operation:    l.Operation,
	}
}

// Listing reconstructs a listing from its persisted shape.
func (s Snapshot) Listing() Listing {
	return Listing{
		ZonapropID:   s.ZonapropID,
		CanonicalURL: s.CanonicalURL,
		Title:        s.Title,
		Location:     s.Location,
		PhotoURL:     s.PhotoURL,
		PriceAmount:  s.PriceAmount,
		Currency:     s.Currency,
		Expensas:     s.Expensas,
		M2Tot:        s.M2Tot,
		M2Cub:        s.M2Cub,
		M2Basis:      s.M2Basis,
		Rooms:        s.Rooms,
		Dorm:         s.Dorm,
		Banos:        s.Banos,
		Operation:    s.Operation,
	}
}
