package contact

import (
	"os"
	"testing"
)

// The fixture is the real listing's JSON-LD, with the phone and address replaced by
// fakes.
func realDetail(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile("../../fixtures/detail_ldjson.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return []byte(`<html><head><script type="application/ld+json">` + string(body) +
		`</script><script type="application/ld+json">{"@type":"WebSite"}</script></head></html>`)
}

func TestFromJSONLDReadsTheListingBlock(t *testing.T) {
	got, ok := FromJSONLD(realDetail(t))
	if !ok {
		t.Fatal("expected to find the listing block")
	}
	if got.Phone != "5491100000000" {
		t.Errorf("phone = %q, want the digits of the fixture's number", got.Phone)
	}
	// The real page has no advertiser email, only placeholders.
	if got.Email != "" {
		t.Errorf("email = %q, want empty", got.Email)
	}
	if got.StreetAddress == "" {
		t.Error("expected the structured street address")
	}
	if got.Neighbourhood == "" {
		t.Error("expected the neighbourhood from addressRegion")
	}
}

func TestFromJSONLDWithoutAListingBlock(t *testing.T) {
	if _, ok := FromJSONLD([]byte(`<html><script type="application/ld+json">{"@type":"WebSite"}</script></html>`)); ok {
		t.Error("a page without a listing block must report ok=false")
	}
	if _, ok := FromJSONLD([]byte(`<html>no structured data</html>`)); ok {
		t.Error("a page without structured data must report ok=false")
	}
}

func TestNormalizePhone(t *testing.T) {
	cases := []struct{ in, want string }{
		{"54 9 1168690900", "5491168690900"},
		{"+54 9 11 6869-0900", "+5491168690900"},
		{"11 6869 0900", "1168690900"},
		{"", ""},
		{"sin telefono", ""}, // too short once non-digits are dropped
		{"12345", ""},        // too short to dial
	}
	for _, tc := range cases {
		if got := normalizePhone(tc.in); got != tc.want {
			t.Errorf("normalizePhone(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
