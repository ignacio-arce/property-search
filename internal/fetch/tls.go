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

// fetchViaTLS retrieves u with a browser-like TLS fingerprint (the Go
// equivalent of cloudscraper) so Cloudflare cannot distinguish the client from
// a real Chrome. When an HTTP proxy is configured the request is tunnelled
// through it, giving the rotating egress IP a fresh shot on each attempt.
func (c *Client) fetchViaTLS(ctx context.Context, u string) ([]byte, error) {
	options := []httpclient.HttpClientOption{
		httpclient.WithClientProfile(profiles.Chrome_152),
		httpclient.WithTimeoutSeconds(int(c.cfg.FetchTimeout.Seconds())),
	}
	if c.cfg.ZonapropProxy != "" {
		options = append(options, httpclient.WithProxyUrl(c.cfg.ZonapropProxy))
	}

	client, err := httpclient.NewHttpClient(httpclient.NewNoopLogger(), options...)
	if err != nil {
		return nil, fmt.Errorf("tls client: %w", err)
	}

	req, err := fhttp.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "es-AR,es;q=0.9")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tls request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("tls read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tls request %s: status %d", u, resp.StatusCode)
	}
	return body, nil
}
