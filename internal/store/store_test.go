package store

import (
	"os"
	"path/filepath"
	"testing"

	"zonapropbot/internal/model"
)

func openIn(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func sampleListing(id string) model.Listing {
	return model.Listing{ZonapropID: id, CanonicalURL: "https://www.zonaprop.com.ar/p/" + id + ".html", Title: "t", Location: "x"}
}

func TestStoreStartsEmpty(t *testing.T) {
	s := openIn(t, t.TempDir())
	defer s.Close()
	if s.Contains("abc") {
		t.Error("new store should not contain anything")
	}
}

func TestAddThenContains(t *testing.T) {
	s := openIn(t, t.TempDir())
	defer s.Close()
	if err := s.Add(sampleListing("id-1")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !s.Contains("id-1") {
		t.Error("store should contain id-1 after Add")
	}
	if s.Contains("id-2") {
		t.Error("store should not contain id-2")
	}
}

func TestAddIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	s := openIn(t, dir)
	if err := s.Add(sampleListing("dup")); err != nil {
		t.Fatalf("Add 1: %v", err)
	}
	if err := s.Add(sampleListing("dup")); err != nil {
		t.Fatalf("Add 2: %v", err)
	}
	s.Close()

	raw, err := os.ReadFile(filepath.Join(dir, "seen.jsonl"))
	if err != nil {
		t.Fatalf("read seen.jsonl: %v", err)
	}
	lines := 0
	for _, b := range raw {
		if b == '\n' {
			lines++
		}
	}
	if lines != 1 {
		t.Errorf("seen.jsonl has %d lines, want 1 (duplicate must not be appended)", lines)
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s := openIn(t, dir)
	if err := s.Add(sampleListing("persist-1")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	s.Close()

	s2 := openIn(t, dir)
	defer s2.Close()
	if !s2.Contains("persist-1") {
		t.Error("id must survive a reopen")
	}
}

func TestLoadToleratesCorruptTrailingLine(t *testing.T) {
	dir := t.TempDir()
	s := openIn(t, dir)
	if err := s.Add(sampleListing("good")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	s.Close()

	f, err := os.OpenFile(filepath.Join(dir, "seen.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"id":"truncated"`)
	f.Close()

	s2 := openIn(t, dir)
	defer s2.Close()
	if !s2.Contains("good") {
		t.Error("valid ids must load despite a corrupt trailing line")
	}
	if s2.Contains("truncated") {
		t.Error("corrupt trailing line must be ignored")
	}
}

func TestOpenCreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "state")
	s := openIn(t, dir)
	defer s.Close()
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("directory not created: %v", err)
	}
}
