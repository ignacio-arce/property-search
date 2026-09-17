package fetch

import (
	"context"
	"fmt"
	"io"
	"net/http"

	fhttp "github.com/bogdanfinn/fhttp"
	httpclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// fetchViaTLS retrieves u with a browser-like TLS fingerprint (the Go equivalent
// of cloudscraper) so Cloudflare cannot distinguish the client from a real
// Chrome. When a proxy is configured the request is tunnelled through it, giving
// the rotating egress IP a fresh shot on each attempt.
//
// Unlike the previous version it does not collapse every non-200 into a generic
// error: it returns the status and headers so the caller can tell a challenge
// from a transient status from a network failure.
func (c *Client) fetchViaTLS(ctx context.Context, u string) (*Result, error) {
	mode := "tls"
	options := []httpclient.HttpClientOption{
		httpclient.WithClientProfile(profiles.Chrome_152),
		httpclient.WithTimeoutSeconds(int(c.cfg.FetchTimeout.Seconds())),
	}
	if c.cfg.ZonapropProxy != "" {
		options = append(options, httpclient.WithProxyUrl(c.cfg.ZonapropProxy))
		mode = "tls-proxy"
	}

	client, err := httpclient.NewHttpClient(httpclient.NewNoopLogger(), options...)
	if err != nil {
		return nil, &Error{Kind: KindTransport, Mode: mode, Err: fmt.Errorf("tls client: %w", err)}
	}

	req, err := fhttp.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, &Error{Kind: KindTransport, Mode: mode, Err: err}
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "es-AR,es;q=0.9")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")

	resp, err := client.Do(req)
	if err != nil {
		return nil, &Error{Kind: KindTransport, Mode: mode, Err: fmt.Errorf("tls request: %w", err)}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, &Error{Kind: KindTransport, Mode: mode, Err: fmt.Errorf("tls read: %w", err)}
	}

	header := http.Header(resp.Header)
	if resp.StatusCode != http.StatusOK {
		return nil, &Error{
			Kind:   classifyStatus(resp.StatusCode, header, body),
			Status: resp.StatusCode,
			Header: header,
			Mode:   mode,
			Err:    fmt.Errorf("unexpected status %d", resp.StatusCode),
		}
	}

	return &Result{Body: body, Mode: mode, Status: resp.StatusCode, Header: header}, nil
}

// classifyStatus decides whether a non-200 answer is a Cloudflare challenge or
// just an unexpected status. A challenge is the one that must not be retried:
// the measured block came back as 403 with "cf-mitigated: challenge", and
// hammering it leaves the IP degraded. A bare 403 has no marker, so it stays
// retryable as a transient error.
func classifyStatus(status int, header http.Header, body []byte) ErrorKind {
	if hasChallengeMarkers(header, body) {
		return KindBlocked
	}
	return KindHTTPStatus
}
