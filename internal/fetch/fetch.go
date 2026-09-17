package fetch

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"time"

	"zonapropbot/internal/config"
)

// noProxyTransport clones the default transport with proxying disabled. The
// rotating proxy is applied explicitly on the paths that need it (tls-client via
// ZONAPROP_PROXY); other outbound calls must not pick up HTTP_PROXY from the
// environment, which would route them through the proxy and break them.
func noProxyTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	return t
}

// Result is a successfully fetched document plus the mode that produced it.
type Result struct {
	Body []byte
	Mode string // "flaresolverr" | "tls" | "tls-proxy"
}

// Client fetches documents from a target site, resolving Cloudflare when a
// FlareSolverr instance is configured and retrying with jittered backoff.
type Client struct {
	cfg *config.Config
}

// New returns a Client bound to the given configuration.
func New(cfg *config.Config) *Client {
	return &Client{cfg: cfg}
}

// Fetch retrieves the document at u. When FLARESOLVERR_URL is configured it is
// tried first on every attempt, falling back to a direct TLS request through
// the optional HTTP proxy within the same attempt. Failures are retried up to
// cfg.FetchRetries times with jittered exponential backoff; each attempt opens
// a fresh connection so a rotating proxy gets a new egress IP per attempt.
func (c *Client) Fetch(ctx context.Context, u string) (*Result, error) {
	maxAttempts := c.cfg.FetchRetries + 1
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			if err := sleepWithContext(ctx, backoff(attempt)); err != nil {
				return nil, err
			}
		}

		if c.cfg.FlareSolverrURL != "" {
			body, err := c.fetchViaFlareSolverr(ctx, u)
			if err == nil {
				return &Result{Body: body, Mode: "flaresolverr"}, nil
			}
			lastErr = err
		}

		body, err := c.fetchViaTLS(ctx, u)
		if err == nil {
			mode := "tls"
			if c.cfg.ZonapropProxy != "" {
				mode = "tls-proxy"
			}
			return &Result{Body: body, Mode: mode}, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("fetch %s: failed after %d attempt(s): %w", u, maxAttempts, lastErr)
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

var errNoMode = errors.New("no fetch mode configured")
