package scheduler

import (
	"context"
	"testing"
	"time"
)

// The loop's risky property is that it never leaks: it must return promptly when
// the context is cancelled, and it must not fire the job early.
func TestRunStopsOnCancellationAndDoesNotFireEarly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fired := make(chan struct{}, 1)
	job := func(context.Context) { fired <- struct{}{} }

	done := make(chan error, 1)
	// Midnight UTC: the next run is hours away, so nothing should fire here.
	go func() { done <- Run(ctx, time.UTC, 0, 0, job, nil, time.Now) }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want a clean nil on cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}

	select {
	case <-fired:
		t.Fatal("the job fired before its scheduled time")
	default:
	}
}
