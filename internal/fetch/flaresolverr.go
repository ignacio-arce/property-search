package fetch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type fsRequest struct {
	Cmd        string `json:"cmd"`
	URL        string `json:"url"`
	MaxTimeout int    `json:"maxTimeout"`
}

type fsResponse struct {
	Status   string `json:"status"`
	Message  string `json:"message"`
	Solution struct {
		Status   int    `json:"status"`
		Response string `json:"response"`
	} `json:"solution"`
}

// fetchViaFlareSolverr asks the FlareSolverr instance to resolve the Cloudflare
// challenge for u and returns the rendered HTML.
//
// FlareSolverr returns only the body, so Result.Header stays empty on this path
// and a challenge it could not solve is reported through its own status/message
// instead.
func (c *Client) fetchViaFlareSolverr(ctx context.Context, u string) (*Result, error) {
	const mode = "flaresolverr"

	payload, err := json.Marshal(fsRequest{
		Cmd:        "request.get",
		URL:        u,
		MaxTimeout: int(c.cfg.MaxBrowserTimeout.Milliseconds()),
	})
	if err != nil {
		return nil, &Error{Kind: KindTransport, Mode: mode, Err: err}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.cfg.FlareSolverrURL+"/v1", bytes.NewReader(payload))
	if err != nil {
		return nil, &Error{Kind: KindTransport, Mode: mode, Err: err}
	}
	req.Header.Set("Content-Type", "application/json")

	// The client timeout must exceed the browser's maxTimeout, otherwise the bot
	// aborts resolutions that were about to succeed and launches another browser.
	//
	// Proxy disabled on purpose: FlareSolverr is a compose service reached by name,
	// and the proxy is for Zonaprop traffic only. HTTP_PROXY from the environment
	// must not leak here.
	hc := &http.Client{
		Timeout:   c.cfg.MaxBrowserTimeout + fsClientTimeoutMargin,
		Transport: noProxyTransport(),
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, &Error{Kind: KindTransport, Mode: mode, Err: fmt.Errorf("flaresolverr request: %w", err)}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, &Error{Kind: KindTransport, Mode: mode, Err: fmt.Errorf("flaresolverr read: %w", err)}
	}

	var fs fsResponse
	if err := json.Unmarshal(raw, &fs); err != nil {
		return nil, &Error{
			Kind:   KindHTTPStatus,
			Status: resp.StatusCode,
			Mode:   mode,
			Err:    fmt.Errorf("flaresolverr bad response: %w", err),
		}
	}

	if fs.Status != "ok" {
		// "Error solving the challenge. Timeout after Ns" is the measured failure
		// when Cloudflare escalates; it must be classified as blocked, not as a
		// transient error to retry.
		kind := KindHTTPStatus
		if strings.Contains(strings.ToLower(fs.Message), "challenge") {
			kind = KindBlocked
		}
		return nil, &Error{
			Kind:   kind,
			Status: resp.StatusCode,
			Mode:   mode,
			Err:    fmt.Errorf("flaresolverr status %q: %s", fs.Status, fs.Message),
		}
	}

	if fs.Solution.Status != http.StatusOK {
		return nil, &Error{
			Kind:   classifyStatus(fs.Solution.Status, nil, []byte(fs.Solution.Response)),
			Status: fs.Solution.Status,
			Mode:   mode,
			Err:    fmt.Errorf("flaresolverr solved with status %d", fs.Solution.Status),
		}
	}

	return &Result{
		Body:   []byte(fs.Solution.Response),
		Mode:   mode,
		Status: fs.Solution.Status,
	}, nil
}
