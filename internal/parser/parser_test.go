package parser

import (
	"os"
	"strings"
	"testing"
)

// realSearchURL is the operator's search, used as siteBase exactly like the bot
// does. It is the URL that made the old parser mangle every canonical link.
const realSearchURL = "https://www.zonaprop.com.ar/departamentos-venta-gba-norte-3-ambientes-mas-de-1-garage-mas-55-m2-cubiertos-hasta-10-anos-100000-200000-dolar-orden-publicado-descendente.html"

func loadRealPage(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile("../../fixtures/search_gba_norte.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return body
}

func TestParseRealSearchPage(t *testing.T) {
	listings, stats, err := ParseWithStats(loadRealPage(t), realSearchURL)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if stats.Cards != 30 {
		t.Errorf("Cards = %d, want 30", stats.Cards)
	}
	if stats.SkippedType != 0 || stats.SkippedNoID != 0 {
		t.Errorf("unexpected skips: %+v", stats)
	}
	if len(listings) != 30 {
		t.Fatalf("parsed %d listings, want 30", len(listings))
	}

	first := listings[0]
	if first.ZonapropID != "60170922" {
		t.Errorf("ZonapropID = %q, want 60170922", first.ZonapropID)
	}
	wantURL := "https://www.zonaprop.com.ar/propiedades/clasificado/veclapin-departamento-florida-belgrano-oeste-60170922.html"
	if first.CanonicalURL != wantURL {
		t.Errorf("CanonicalURL =\n %q\nwant %q", first.CanonicalURL, wantURL)
	}
	if first.PriceAmount == nil || *first.PriceAmount != 144000 {
		t.Errorf("PriceAmount = %v, want 144000", first.PriceAmount)
	}
	if first.Currency != "USD" {
		t.Errorf("Currency = %q, want USD", first.Currency)
	}
	if first.Expensas == nil || *first.Expensas != 180000 {
		t.Errorf("Expensas = %v, want 180000", first.Expensas)
	}
	if first.M2Tot == nil || *first.M2Tot != 69 {
		t.Errorf("M2Tot = %v, want 69", first.M2Tot)
	}
	if first.M2Cub != nil {
		t.Errorf("M2Cub = %v, want nil: the real card only shows m² tot.", first.M2Cub)
	}
	if first.M2Basis != "tot" {
		t.Errorf("M2Basis = %q, want tot", first.M2Basis)
	}
	if first.Rooms == nil || *first.Rooms != 3 {
		t.Errorf("Rooms = %v, want 3", first.Rooms)
	}
	if first.Dorm == nil || *first.Dorm != 2 {
		t.Errorf("Dorm = %v, want 2", first.Dorm)
	}
	if first.Banos == nil || *first.Banos != 1 {
		t.Errorf("Banos = %v, want 1", first.Banos)
	}
	if first.Location != "Florida, Vicente López" {
		t.Errorf("Location = %q", first.Location)
	}
	if first.Operation != "venta" {
		t.Errorf("Operation = %q, want venta", first.Operation)
	}
	if first.Title == "" || strings.Contains(first.Title, "<") {
		t.Errorf("Title = %q, want non-empty plain text", first.Title)
	}
	if !strings.Contains(first.PhotoURL, "zonapropcdn.com") {
		t.Errorf("PhotoURL = %q, want a CDN url", first.PhotoURL)
	}
}

// Regression: callers pass the full search URL as siteBase, and the old code
// concatenated it onto root-relative hrefs, producing
// "...-200000-dolar.html/propiedades/clasificado/...". The unit tests never caught
// it because they always passed the bare origin.
func TestCanonicalURLUsesOriginNotTheSearchPath(t *testing.T) {
	listings, err := Parse(loadRealPage(t), realSearchURL)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range listings {
		if strings.Contains(l.CanonicalURL, "dolar.html/propiedades") {
			t.Fatalf("canonical URL contains the search path: %q", l.CanonicalURL)
		}
		if n := strings.Count(l.CanonicalURL, "/propiedades/"); n != 1 {
			t.Errorf("canonical URL has %d '/propiedades/' segments: %q", n, l.CanonicalURL)
		}
		if strings.Contains(l.CanonicalURL, "?") {
			t.Errorf("canonical URL kept a query: %q", l.CanonicalURL)
		}
	}
}

func TestParseSkipsNonPropertyAndIDLessCards(t *testing.T) {
	html := `<html><body>
	  <div data-to-posting="/p/dev-111.html" data-id="111" data-posting-type="DEVELOPMENT"></div>
	  <div data-to-posting="/p/no-id-222.html" data-posting-type="PROPERTY"></div>
	  <div data-to-posting="/p/ok-333.html" data-id="333" data-posting-type="PROPERTY">
	    <h2 data-qa="POSTING_CARD_PRICE">USD 100.000</h2>
	  </div>
	</body></html>`

	listings, stats, err := ParseWithStats([]byte(html), realSearchURL)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Cards != 3 {
		t.Errorf("Cards = %d, want 3", stats.Cards)
	}
	if stats.SkippedType != 1 {
		t.Errorf("SkippedType = %d, want 1", stats.SkippedType)
	}
	if stats.SkippedNoID != 1 {
		t.Errorf("SkippedNoID = %d, want 1 (a DOM change must be countable)", stats.SkippedNoID)
	}
	if len(listings) != 1 || listings[0].ZonapropID != "333" {
		t.Fatalf("listings = %+v, want only the valid PROPERTY card", listings)
	}
}

// A missing value must stay absent, never become zero: a priceless listing scored
// as the cheapest one collects accidental likes and owns the ranking.
func TestMissingValuesStayNil(t *testing.T) {
	html := `<html><body>
	  <div data-to-posting="/p/x-444.html" data-id="444" data-posting-type="PROPERTY"></div>
	</body></html>`

	listings, _, err := ParseWithStats([]byte(html), realSearchURL)
	if err != nil {
		t.Fatal(err)
	}
	if len(listings) != 1 {
		t.Fatalf("parsed %d listings, want 1", len(listings))
	}
	l := listings[0]
	if l.PriceAmount != nil || l.Expensas != nil || l.M2Tot != nil || l.M2Cub != nil || l.Rooms != nil || l.Dorm != nil || l.Banos != nil {
		t.Errorf("absent values must be nil, got %+v", l)
	}
	if l.PriceLabel() != "" || l.SizeLabel() != "" || l.ExpensasLabel() != "" {
		t.Errorf("labels must be empty when values are absent: %q %q %q",
			l.PriceLabel(), l.SizeLabel(), l.ExpensasLabel())
	}
}

func TestParsePrice(t *testing.T) {
	cases := []struct {
		in       string
		want     *int64
		currency string
	}{
		{"USD 144.000", ptr(144000), "USD"},
		{"USD 83.900", ptr(83900), "USD"},
		{"$ 150.000.000", ptr(150000000), "ARS"},
		{"$ 450.000", ptr(450000), "ARS"}, // a monthly rent, ARS
		{"Desde USD 120.000", ptr(120000), "USD"},
		{"Consultar precio", nil, ""},
		{"", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, currency := parsePrice(tc.in)
			if !eq(got, tc.want) {
				t.Errorf("parsePrice(%q) amount = %v, want %v", tc.in, deref(got), deref(tc.want))
			}
			if currency != tc.currency {
				t.Errorf("parsePrice(%q) currency = %q, want %q", tc.in, currency, tc.currency)
			}
		})
	}
}

func TestExpensasIgnoresSurroundingText(t *testing.T) {
	got, ok := parseAmount("$ 180.000 Expensas")
	if !ok || got == nil || *got != 180000 {
		t.Fatalf("parseAmount = %v (ok=%v), want 180000", deref(got), ok)
	}
	if _, ok := parseAmount("Sin expensas"); ok {
		t.Error("a text with no number must not produce an amount")
	}
}

// The real page carries m²=1 and m²=12500 next to a healthy 58-141 range. Without
// the guard, a 1 m² listing yields a price-per-m² of ~150.000 and wins the ranking.
func TestSizesOutlierGuard(t *testing.T) {
	cases := []struct {
		name string
		feat []string
		want *float64
	}{
		{"normal", []string{"69 m² tot."}, fptr(69)},
		{"too small", []string{"1 m² tot."}, nil},
		{"too large", []string{"12500 m² tot."}, nil},
		{"covered goes to cub", []string{"220 m² cub."}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tot, cub := sizes(tc.feat)
			if tc.name == "covered goes to cub" {
				if cub == nil || *cub != 220 {
					t.Errorf("cub = %v, want 220", derefF(cub))
				}
				if tot != nil {
					t.Errorf("tot = %v, want nil", derefF(tot))
				}
				return
			}
			if !eqF(tot, tc.want) {
				t.Errorf("tot = %v, want %v", derefF(tot), derefF(tc.want))
			}
		})
	}
}

func TestPartido(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Florida, Vicente López", "Vicente López"},
		{"Centro, Tigre", "Tigre"},
		{"Pilar, Pilar", "Pilar"},
		// The macrozone cases: taking the last segment would collapse most of the
		// page into a single "GBA Norte" bucket.
		{"San Isidro, GBA Norte", "San Isidro"},
		{"Tigre, GBA Norte", "Tigre"},
		{"Florida", "Florida"},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := Partido(tc.in); got != tc.want {
				t.Errorf("Partido(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func ptr(v int64) *int64      { return &v }
func fptr(v float64) *float64 { return &v }

func deref(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func derefF(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func eq(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func eqF(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
