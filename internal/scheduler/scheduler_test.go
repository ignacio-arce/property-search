package scheduler

import (
	"testing"
	"time"
)

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestNextRunBeforeTheHourIsToday(t *testing.T) {
	loc := mustLoad(t, "America/Argentina/Buenos_Aires")
	now := time.Date(2026, 9, 16, 7, 30, 0, 0, loc)

	got := NextRun(now, loc, 9, 0)
	want := time.Date(2026, 9, 16, 9, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("NextRun = %s, want %s", got, want)
	}
}

func TestNextRunAfterTheHourIsTomorrow(t *testing.T) {
	loc := mustLoad(t, "America/Argentina/Buenos_Aires")
	now := time.Date(2026, 9, 16, 10, 0, 0, 0, loc)

	got := NextRun(now, loc, 9, 0)
	want := time.Date(2026, 9, 17, 9, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("NextRun = %s, want %s", got, want)
	}
}

// Exactly at the hour the next run is tomorrow, not now: otherwise a restart at
// 09:00:00 would fire a second digest.
func TestNextRunExactlyAtTheHourIsTomorrow(t *testing.T) {
	loc := mustLoad(t, "America/Argentina/Buenos_Aires")
	now := time.Date(2026, 9, 16, 9, 0, 0, 0, loc)

	got := NextRun(now, loc, 9, 0)
	if got.Day() != 17 {
		t.Errorf("NextRun = %s, want tomorrow", got)
	}
}

// The whole reason time/tzdata is imported: the image has no zoneinfo, so without
// it a UTC instant would be read as 09:00 UTC and the digest would fire at 06:00
// in Buenos Aires.
func TestNextRunRespectsTheZone(t *testing.T) {
	loc := mustLoad(t, "America/Argentina/Buenos_Aires")
	utc := time.Date(2026, 9, 16, 3, 0, 0, 0, time.UTC) // 00:00 in Buenos Aires

	got := NextRun(utc, loc, 9, 0)
	if got.UTC().Hour() != 12 {
		t.Errorf("09:00 Buenos Aires is 12:00 UTC, got %s (%s)", got, got.UTC())
	}
	if got.Hour() != 9 {
		t.Errorf("NextRun hour in loc = %d, want 9", got.Hour())
	}
}

func TestNextRunHonoursMinutes(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 9, 16, 9, 30, 0, 0, loc)

	got := NextRun(now, loc, 9, 45)
	want := time.Date(2026, 9, 16, 9, 45, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("NextRun = %s, want %s", got, want)
	}
}
