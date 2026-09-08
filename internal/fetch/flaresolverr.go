package fetch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
func (c *Client) fetchViaFlareSolverr(ctx context.Context, u string) ([]byte, error) {
	payload, err := json.Marshal(fsRequest{
		Cmd:        "request.get",
		URL:        u,
		MaxTimeout: int(c.cfg.MaxBrowserTimeout.Milliseconds()),
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.cfg.FlareSolverrURL+"/v1", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	hc := &http.Client{Timeout: c.cfg.FetchTimeout}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("flaresolverr request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("flaresolverr read: %w", err)
	}

	var fs fsResponse
	if err := json.Unmarshal(raw, &fs); err != nil {
		return nil, fmt.Errorf("flaresolverr bad response (%d): %w", resp.StatusCode, err)
	}
	if fs.Status != "ok" || fs.Solution.Status != 200 {
		return nil, fmt.Errorf("flaresolverr: status=%q message=%q solution_status=%d",
			fs.Status, fs.Message, fs.Solution.Status)
	}
	return []byte(fs.Solution.Response), nil
}
