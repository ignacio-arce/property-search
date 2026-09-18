// Package scheduler fires the daily digest at a local wall-clock time.
//
// It exists because the old bot ran on a relative ticker, which is wrong for
// "every day at 09:00": it drifts, it fires on every restart, and it silently
// depends on the process timezone. The image is FROM scratch with no zoneinfo, so
// without importing time/tzdata, 09:00 Buenos Aires would fire at 06:00 local.
package scheduler

import (
	"context"
	"log/slog"
	"time"
)

// NextRun returns the next occurrence of hour:minute in loc strictly after now.
// It is a pure function so the date arithmetic can be tested without waiting.
func NextRun(now time.Time, loc *time.Location, hour, minute int) time.Time {
	local := now.In(loc)
	next := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
	if !next.After(local) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// Job is the work the scheduler triggers.
type Job func(ctx context.Context)

// Run fires job every day at the configured local time until ctx is cancelled.
// It never runs job at boot: restart: unless-stopped would otherwise trigger an
// off-schedule digest on every restart.
func Run(ctx context.Context, loc *time.Location, hour, minute int, job Job, logger *slog.Logger, now func() time.Time) error {
	if now == nil {
		now = time.Now
	}
	for {
		next := NextRun(now(), loc, hour, minute)
		wait := time.Until(next)
		if wait < 0 {
			wait = 0
		}
		if logger != nil {
			logger.Info("scheduler: next digest", "at", next.Format(time.RFC3339), "in", wait.Round(time.Second))
		}

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
			job(ctx)
		}
	}
}
