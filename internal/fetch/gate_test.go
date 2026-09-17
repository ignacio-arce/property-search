package fetch

import (
	"context"
	"testing"
	"time"
)

func TestGatePacesRequests(t *testing.T) {
	const gap = 60 * time.Millisecond
	g := NewGate(gap, 1, time.Minute)
	ctx := context.Background()

	if err := g.Acquire(ctx, false); err != nil {
		t.Fatal(err)
	}
	g.Release()

	start := time.Now()
	if err := g.Acquire(ctx, false); err != nil {
		t.Fatal(err)
	}
	g.Release()

	if elapsed := time.Since(start); elapsed < gap {
		t.Errorf("second request waited %v, want at least the %v gap", elapsed, gap)
	}
}

func TestGatePrioritySkipsPacing(t *testing.T) {
	g := NewGate(time.Hour, 1, time.Hour) // absurd gap: nothing unpaced could pass
	ctx := context.Background()

	if err := g.Acquire(ctx, false); err != nil {
		t.Fatal(err)
	}
	g.Release()

	start := time.Now()
	if err := g.Acquire(ctx, true); err != nil {
		t.Fatal(err)
	}
	g.Release()

	// A priority request (the operator's own search, or a user's thumbs-up) must
	// not be queued behind the bulk budget.
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("priority request waited %v, want to skip the pacing gap", elapsed)
	}
}

func TestGateCooldownPushesNextRequestBack(t *testing.T) {
	const cooldown = time.Hour
	g := NewGate(time.Millisecond, 1, cooldown)

	before := time.Now()
	g.Cooldown()

	if next := g.NextAllowed(); next.Before(before.Add(cooldown)) {
		t.Errorf("NextAllowed = %v, want it pushed back by the cooldown after a challenge", next)
	}
}

func TestGateLimitsConcurrency(t *testing.T) {
	g := NewGate(time.Millisecond, 1, time.Minute)

	if err := g.Acquire(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	defer g.Release()

	// The single slot is taken, so a second acquire must block until the context
	// gives up rather than running a second browser at once.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := g.Acquire(ctx, false); err == nil {
		g.Release()
		t.Fatal("second acquire succeeded while the first slot was held")
	}
}
