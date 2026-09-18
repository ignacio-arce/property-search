package fetch

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Gate paces and limits outbound Zonaprop requests.
//
// This is the primary defence, not an optimisation: during the risk probes
// Cloudflare escalated after roughly four requests in ten minutes and the
// previously working URL stopped resolving. A retry loop that hammers a
// challenged IP leaves it degraded semi-permanently, so the pacing is the
// feature and the retries are the thing being restrained.
type Gate struct {
	mu       sync.Mutex
	minGap   time.Duration
	cooldown time.Duration
	next     time.Time
	sem      chan struct{}
	// logger is optional and nil-safe; it records pacing waits at DEBUG.
	logger *slog.Logger
}

// NewGate builds a gate. minGap is the minimum spacing between requests,
// concurrency caps how many may be in flight, and cooldown is how long to hold
// off after a challenge is detected.
func NewGate(minGap time.Duration, concurrency int, cooldown time.Duration) *Gate {
	if concurrency < 1 {
		concurrency = 1
	}
	return &Gate{
		minGap:   minGap,
		cooldown: cooldown,
		sem:      make(chan struct{}, concurrency),
	}
}

// Acquire waits for a slot. Priority requests skip the pacing wait (they still
// respect the concurrency limit), so an operator's search or a user's thumbs-up
// is not queued behind a bulk digest.
func (g *Gate) Acquire(ctx context.Context, priority bool) error {
	select {
	case g.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}

	g.mu.Lock()
	now := time.Now()
	start := g.next
	if priority || start.Before(now) {
		start = now
	}
	g.next = start.Add(g.minGap)
	g.mu.Unlock()

	if wait := time.Until(start); wait > 0 {
		if g.logger != nil {
			g.logger.Debug("fetch: gate wait", "wait", wait.Round(time.Millisecond), "priority", priority)
		}
		if err := sleepWithContext(ctx, wait); err != nil {
			<-g.sem
			return err
		}
	}
	return nil
}

// Release frees the slot taken by Acquire.
func (g *Gate) Release() {
	<-g.sem
}

// Cooldown pushes the next allowed request back by the configured cooldown. It is
// called when a challenge is detected so the IP gets time to recover instead of
// being hammered.
func (g *Gate) Cooldown() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if until := time.Now().Add(g.cooldown); until.After(g.next) {
		g.next = until
	}
}

// NextAllowed reports when the next non-priority request may start. Used by tests.
func (g *Gate) NextAllowed() time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.next
}
