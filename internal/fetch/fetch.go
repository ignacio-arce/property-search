package fetch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"zonapropbot/internal/config"
	"zonapropbot/internal/searchurl"
)

// Default pacing for outbound Zonaprop requests. The probes showed Cloudflare
// escalating after ~4 requests in 10 minutes, so the gap is deliberately wide.
const (
	defaultMinGap         = 60 * time.Second
	defaultCooldown       = 5 * time.Minute
	fsClientTimeoutMargin = 30 * time.Second
)

// Result is a successfully fetched document plus the metadata needed to classify
// it. Header is empty on the FlareSolverr path: FlareSolverr returns the rendered
// body only, so a challenge that it could not solve surfaces as its own status.
type Result struct {
	Body   []byte
	Mode   string // "flaresolverr" | "tls" | "tls-proxy"
	Status int
	Header http.Header
}

// ErrorKind classifies why a fetch failed. The validation layer needs to tell a
// Cloudflare challenge apart from a genuine network failure: a challenge means
// "come back later, do not demote a valid URL", a transport failure is a plain
// outage, and an unexpected status is neither.
type ErrorKind int

const (
	// KindTransport is a dial, TLS or timeout failure: no answer at all.
	KindTransport ErrorKind = iota + 1
	// KindBlocked is the site answering but refusing: a Cloudflare challenge.
	KindBlocked
	// KindHTTPStatus is an answer with an unexpected non-2xx status.
	KindHTTPStatus
)

func (k ErrorKind) String() string {
	switch k {
	case KindTransport:
		return "transport"
	case KindBlocked:
		return "blocked"
	case KindHTTPStatus:
		return "http-status"
	default:
		return "unknown"
	}
}

// Error is a classified fetch failure.
type Error struct {
	Kind   ErrorKind
	Status int
	Header http.Header
	Mode   string
	Err    error
}

func (e *Error) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("fetch (%s, kind=%s, status=%d): %v", e.Mode, e.Kind, e.Status, e.Err)
	}
	return fmt.Sprintf("fetch (%s, kind=%s): %v", e.Mode, e.Kind, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// KindOf extracts the classification from err when it is, or wraps, a fetch Error.
func KindOf(err error) (ErrorKind, bool) {
	var fe *Error
	if errors.As(err, &fe) {
		return fe.Kind, true
	}
	return 0, false
}

// Options tunes a single fetch.
type Options struct {
	// Priority skips the pacing wait. Use it for interactive work (a user's
	// thumbs-up) and for the operator's own searches, so they are not queued
	// behind a bulk digest.
	Priority bool
	// NoRetries disables the inner retry loop. Validation uses it: five attempts
	// times four inner retries is twenty hits on a challenge page from a shared IP.
	NoRetries bool
}

// Client fetches documents from a target site, resolving Cloudflare with
// FlareSolverr when configured, falling back to a Chrome-fingerprinted TLS client
// through the optional rotating proxy, and pacing Zonaprop traffic.
type Client struct {
	cfg    *config.Config
	gate   *Gate
	logger *slog.Logger
}

// Option tunes a Client at construction.
type Option func(*Client)

// WithLogger attaches a logger for fetch diagnostics. Blocks and cooldowns are
// WARN; the mode, status, duration and URL of each fetch are DEBUG. Without it
// the client stays silent.
func WithLogger(l *slog.Logger) Option {
	return func(c *Client) { c.logger = l }
}

// New returns a Client bound to the given configuration.
func New(cfg *config.Config, opts ...Option) *Client {
	// One browser at a time when FlareSolverr is in play: FlareSolverr launches a
	// new Chromium per request, and two concurrent instances on a Pi exhaust memory.
	concurrency := 2
	if cfg.FlareSolverrURL != "" {
		concurrency = 1
	}
	minGap := cfg.FetchRateLimit
	if minGap <= 0 {
		minGap = defaultMinGap
	}
	c := &Client{cfg: cfg, gate: NewGate(minGap, concurrency, defaultCooldown)}
	for _, opt := range opts {
		opt(c)
	}
	c.gate.logger = c.logger
	return c
}

// debug and warn are nil-safe: the logger is optional.
func (c *Client) debug(msg string, args ...any) {
	if c.logger != nil {
		c.logger.Debug(msg, args...)
	}
}

func (c *Client) warn(msg string, args ...any) {
	if c.logger != nil {
		c.logger.Warn(msg, args...)
	}
}

// Fetch retrieves u with default options.
func (c *Client) Fetch(ctx context.Context, u string) (*Result, error) {
	return c.FetchWith(ctx, u, Options{})
}

// FetchWith retrieves u. Failures are retried with jittered exponential backoff,
// except a Cloudflare challenge, which stops immediately and holds off the gate:
// hammering a challenged IP is what degrades it.
func (c *Client) FetchWith(ctx context.Context, u string, opts Options) (*Result, error) {
	start := time.Now()
	// Only Zonaprop traffic is paced. Image downloads come from the CDN and must
	// not consume the scarce Zonaprop budget, and neither must test servers.
	gated := searchurl.IsZonapropURL(u)
	if gated {
		if err := c.gate.Acquire(ctx, opts.Priority); err != nil {
			return nil, err
		}
		defer c.gate.Release()
	}

	var lastErr error

	// FlareSolverr is attempted at most once per fetch. Each attempt launches a
	// browser, so retrying it multiplies the damage on an already-suspicious IP;
	// the TLS loop below carries the retries instead.
	if c.cfg.FlareSolverrURL != "" {
		res, err := c.fetchViaFlareSolverr(ctx, u)
		if err == nil {
			c.logFetched(u, res, start)
			return res, nil
		}
		lastErr = err
		if c.holdOffOnBlocked(err, gated, u) {
			return nil, err
		}
		c.debug("fetch: flaresolverr failed, falling back to tls", "url", u, "err", err)
	}

	maxAttempts := c.cfg.FetchRetries + 1
	if opts.NoRetries {
		maxAttempts = 1
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			backoff := backoff(attempt)
			c.debug("fetch: retrying", "attempt", attempt, "max_attempts", maxAttempts,
				"backoff", backoff, "url", u)
			if err := sleepWithContext(ctx, backoff); err != nil {
				return nil, err
			}
		}

		res, err := c.fetchViaTLS(ctx, u)
		if err == nil {
			c.logFetched(u, res, start)
			return res, nil
		}
		lastErr = err

		if c.holdOffOnBlocked(err, gated, u) {
			return nil, err
		}
	}

	c.warn("fetch: failed after retries", "attempts", maxAttempts, "err", lastErr)
	c.debug("fetch: failed url", "url", u)
	return nil, fmt.Errorf("fetch %s: failed after %d attempt(s): %w", u, maxAttempts, lastErr)
}

// logFetched records a successful fetch at DEBUG: the mode says which path was
// used, the status and duration say whether it was healthy.
func (c *Client) logFetched(u string, res *Result, start time.Time) {
	c.debug("fetch: done", "mode", res.Mode, "status", res.Status,
		"ms", time.Since(start).Milliseconds(), "url", u)
}

// holdOffOnBlocked reports whether err is a challenge and, if the request was
// paced, backs the gate off so the IP can recover. A challenge is always logged:
// it is the signal that Cloudflare is escalating.
func (c *Client) holdOffOnBlocked(err error, gated bool, u string) bool {
	kind, ok := KindOf(err)
	if !ok || kind != KindBlocked {
		return false
	}
	if gated {
		c.gate.Cooldown()
		c.warn("fetch: blocked; gate cooling down", "mode", errorMode(err), "status", errorStatus(err),
			"cooldown", c.gate.cooldown)
		c.debug("fetch: blocked url", "url", u)
		return true
	}
	c.warn("fetch: blocked", "mode", errorMode(err), "status", errorStatus(err))
	c.debug("fetch: blocked url", "url", u)
	return true
}

func errorMode(err error) string {
	var fe *Error
	if errors.As(err, &fe) {
		return fe.Mode
	}
	return ""
}

func errorStatus(err error) int {
	var fe *Error
	if errors.As(err, &fe) {
		return fe.Status
	}
	return 0
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// backoff returns a jittered exponential delay for the given 1-based attempt.
func backoff(attempt int) time.Duration {
	exp := 1 << (attempt - 1)
	base := time.Duration(exp) * time.Second
	if base > 30*time.Second {
		base = 30 * time.Second
	}
	jitter := time.Duration(rand.Float64() * float64(base))
	return base + jitter
}

// hasChallengeMarkers reports whether a response looks like a Cloudflare
// challenge. The header is the reliable signal; the body markers cover the
// FlareSolverr path, where headers are not available.
func hasChallengeMarkers(header http.Header, body []byte) bool {
	if header.Get("cf-mitigated") != "" {
		return true
	}
	if header.Get("cf-chl-bypass") != "" {
		return true
	}
	// A short body is the giveaway: a real page is far larger than a challenge.
	if len(body) > 16*1024 {
		return false
	}
	page := string(body)
	for _, marker := range []string{"Just a moment", "cf_chl", "cf-please-wait", "Attention Required!"} {
		if strings.Contains(page, marker) {
			return true
		}
	}
	return false
}
