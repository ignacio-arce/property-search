package parser

import (
	"os"
	"testing"

	"zonapropbot/internal/model"
)

func loadFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../fixtures/sample.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

func TestParseExtractsListings(t *testing.T) {
	html := loadFixture(t)
	listings, err := Parse(html, "https://www.zonaprop.com.ar")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(listings) != 3 {
		t.Fatalf("got %d listings, want 3", len(listings))
	}
}

func TestParseFirstCardFields(t *testing.T) {
	html := loadFixture(t)
	listings, err := Parse(html, "https://www.zonaprop.com.ar")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	l := listings[0]
	if l.URL != "https://www.zonaprop.com.ar/propiedades/clasificado/veclapin-departamento-2-o-3-ambientes-en-bernal-con-terraza-y-59930116.html?n_src=Listado&n_pg=1&n_pos=1" {
		t.Errorf("URL = %q", l.URL)
	}
	if l.Title != "Departamento modernizado en Bernal, con terraza y patio" {
		t.Errorf("Title = %q", l.Title)
	}
	if l.Price != "USD 83.900" {
		t.Errorf("Price = %q", l.Price)
	}
	if l.M2 != "128 m² tot." {
		t.Errorf("M2 = %q", l.M2)
	}
	if l.Ambientes != "2 amb." {
		t.Errorf("Ambientes = %q", l.Ambientes)
	}
	if l.Location != "Bernal Este, Quilmes" {
		t.Errorf("Location = %q", l.Location)
	}
	if l.PhotoURL != "https://imgar.zonapropcdn.com/avisos/1/00/59/93/01/16/360x266/2073098010.jpg?isFirstImage=true" {
		t.Errorf("PhotoURL = %q", l.PhotoURL)
	}
	if l.ID == "" {
		t.Error("ID is empty")
	}
}

func TestParseIDIsStableSHA1(t *testing.T) {
	html := loadFixture(t)
	a, _ := Parse(html, "https://www.zonaprop.com.ar")
	b, _ := Parse(html, "https://www.zonaprop.com.ar")
	if a[0].ID != b[0].ID {
		t.Errorf("ID not deterministic: %s != %s", a[0].ID, b[0].ID)
	}
	if len(a[0].ID) != 40 {
		t.Errorf("ID = %q, want 40-char sha1 hex", a[0].ID)
	}
}

func TestParseCardWithoutAmbientes(t *testing.T) {
	html := loadFixture(t)
	listings, _ := Parse(html, "https://www.zonaprop.com.ar")
	l := listings[2]
	if l.Ambientes != "" {
		t.Errorf("Ambientes = %q, want empty (card has none)", l.Ambientes)
	}
	if l.M2 != "220 m² cub." {
		t.Errorf("M2 = %q", l.M2)
	}
	if l.Price != "$ 150.000.000" {
		t.Errorf("Price = %q", l.Price)
	}
}

func TestParseEmptyHTML(t *testing.T) {
	listings, err := Parse([]byte("<html><body></body></html>"), "https://www.zonaprop.com.ar")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(listings) != 0 {
		t.Errorf("got %d listings, want 0", len(listings))
	}
}

func TestParseIDMatchesBaseRepoScheme(t *testing.T) {
	// Nazarena's bot derives the id from the sha1 of the href. Verify ours does
	// the same over the canonical absolute URL so ids stay stable across runs.
	html := loadFixture(t)
	listings, _ := Parse(html, "https://www.zonaprop.com.ar")
	_ = model.Listing{ID: ""}
	want := sha1Hex("https://www.zonaprop.com.ar/propiedades/clasificado/veclapin-departamento-2-o-3-ambientes-en-bernal-con-terraza-y-59930116.html?n_src=Listado&n_pg=1&n_pos=1")
	if listings[0].ID != want {
		t.Errorf("ID = %s, want sha1 of canonical URL = %s", listings[0].ID, want)
	}
}
