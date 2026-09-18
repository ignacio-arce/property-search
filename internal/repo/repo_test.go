package repo_test

import (
	"context"
	"testing"

	"zonapropbot/internal/db"
	"zonapropbot/internal/dbtest"
	"zonapropbot/internal/repo"
)

func newRepo(t *testing.T) *repo.Repo {
	t.Helper()
	pool := dbtest.NewPool(t)
	if err := db.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return repo.New(pool)
}

// New users must be born active so the digest reaches them without an operator
// toggling the switch by hand (testing default).
func TestEnsureUserCreatesActiveUser(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()

	const user int64 = 123
	if err := r.EnsureUser(ctx, user, user); err != nil {
		t.Fatal(err)
	}

	got, err := r.GetUser(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("user was not created")
	}
	if !got.Active {
		t.Error("new user is not active, want active by default")
	}
}

func TestSearchURLsAreScopedPerUser(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()

	const alice, bob int64 = 111, 222
	if err := r.EnsureUser(ctx, alice, alice); err != nil {
		t.Fatal(err)
	}
	if err := r.EnsureUser(ctx, bob, bob); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AddSearchURL(ctx, alice, "alice-search", "https://www.zonaprop.com.ar/alice.html"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AddSearchURL(ctx, bob, "bob-search", "https://www.zonaprop.com.ar/bob.html"); err != nil {
		t.Fatal(err)
	}

	aliceURLs, err := r.ListSearchURLs(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliceURLs) != 1 {
		t.Fatalf("alice has %d search urls, want 1: %+v", len(aliceURLs), aliceURLs)
	}
	if aliceURLs[0].Label != "alice-search" {
		t.Errorf("alice got %q, want her own search", aliceURLs[0].Label)
	}
	for _, u := range aliceURLs {
		if u.Label == "bob-search" {
			t.Fatal("cross-user leak: alice sees bob's search URL")
		}
	}

	bobURLs, err := r.ListSearchURLs(ctx, bob)
	if err != nil {
		t.Fatal(err)
	}
	if len(bobURLs) != 1 || bobURLs[0].Label != "bob-search" {
		t.Fatalf("bob got %+v, want only his own search", bobURLs)
	}
}

func TestAddSearchURLDeduplicatesOnNormalizedForm(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()

	const user int64 = 7
	if err := r.EnsureUser(ctx, user, user); err != nil {
		t.Fatal(err)
	}

	// The same search pasted from page 1 and later from page 3 carries different
	// tracking params; normalization must collapse them into one row.
	if _, err := r.AddSearchURL(ctx, user, "primera", "https://www.zonaprop.com.ar/x.html?n_pg=1&n_pos=1"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AddSearchURL(ctx, user, "corregida", "https://www.zonaprop.com.ar/x.html?n_pg=3&n_pos=9"); err != nil {
		t.Fatal(err)
	}

	urls, err := r.ListSearchURLs(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 1 {
		t.Fatalf("got %d rows, want 1 after normalization dedup: %+v", len(urls), urls)
	}
	if urls[0].Label != "corregida" {
		t.Errorf("label = %q, want the re-added label to win", urls[0].Label)
	}
}

func TestListValidSearchURLsFiltersByStatus(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()

	const user int64 = 9
	if err := r.EnsureUser(ctx, user, user); err != nil {
		t.Fatal(err)
	}
	id, err := r.AddSearchURL(ctx, user, "pendiente", "https://www.zonaprop.com.ar/p.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.AddSearchURL(ctx, user, "valida", "https://www.zonaprop.com.ar/v.html"); err != nil {
		t.Fatal(err)
	}

	// A freshly added URL starts pending, so nothing is fetchable yet.
	valid, err := r.ListValidSearchURLs(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	if len(valid) != 0 {
		t.Fatalf("got %d valid urls, want 0 before validation: %+v", len(valid), valid)
	}

	if _, err := r.Pool().Exec(ctx,
		`UPDATE search_urls SET validation_status = 'valid' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	valid, err = r.ListValidSearchURLs(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	if len(valid) != 1 || valid[0].Label != "pendiente" {
		t.Fatalf("got %+v, want only the validated url", valid)
	}
}
