package fetch

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"zonapropbot/internal/config"
)

func cfgFrom(t *testing.T, env map[string]string) *config.Config {
	t.Helper()
	cfg, err := config.Load(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

func fsReply(status string, solutionStatus int, html string) string {
	statusField := `"status":"` + status + `"`
	if status == "ok" {
		return `{` + statusField + `,"message":"","solution":{"status":` +
			strconv.Itoa(solutionStatus) + `,"response":` + jsonString(html) + `}}`
	}
	return `{` + statusField + `,"message":"boom"}`
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestFlareSolverrSuccess(t *testing.T) {
	var fsReq atomic.Value
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		fsReq.Store(req)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fsReply("ok", 200, "<html><div data-to-posting>casa</div></html>")))
	}))
	defer fs.Close()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("TLS fallback should not be used when FlareSolverr succeeds")
	}))
	defer target.Close()

	cfg := cfgFrom(t, map[string]string{
		"SEARCH_URLS":      target.URL,
		"FLARESOLVERR_URL": fs.URL,
		"FETCH_RETRIES":    "0",
	})
	c := New(cfg)
	res, err := c.Fetch(context.Background(), target.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Mode != "flaresolverr" {
		t.Errorf("Mode = %q, want flaresolverr", res.Mode)
	}
	if !strings.Contains(string(res.Body), "data-to-posting") {
		t.Errorf("body does not contain data-to-posting: %q", res.Body)
	}

	got := fsReq.Load().(map[string]any)
	if got["cmd"] != "request.get" {
		t.Errorf("cmd = %v, want request.get", got["cmd"])
	}
	if got["url"] != target.URL {
		t.Errorf("url = %v, want %s", got["url"], target.URL)
	}
	if got["maxTimeout"] == nil {
		t.Error("maxTimeout missing from FlareSolverr request")
	}
}

func TestFlareSolverrRetriesUntilSuccess(t *testing.T) {
	var hits atomic.Int32
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) < 3 {
			w.Write([]byte(fsReply("error", 0, "")))
			return
		}
		w.Write([]byte(fsReply("ok", 200, "<html>finally</html>")))
	}))
	defer fs.Close()

	cfg := cfgFrom(t, map[string]string{
		"SEARCH_URLS":      "https://target.invalid/x.html",
		"FLARESOLVERR_URL": fs.URL,
		"FETCH_RETRIES":    "3",
	})
	c := New(cfg)
	res, err := c.Fetch(context.Background(), "https://target.invalid/x.html")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(res.Body) != "<html>finally</html>" {
		t.Errorf("body = %q, want <html>finally</html>", res.Body)
	}
	if hits.Load() != 3 {
		t.Errorf("FlareSolverr hits = %d, want 3", hits.Load())
	}
}

func TestFlareSolverrFallsBackToTLS(t *testing.T) {
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fsReply("error", 0, "flaresolverr down")))
	}))
	defer fs.Close()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><div data-to-posting>fallback</div></html>"))
	}))
	defer target.Close()

	cfg := cfgFrom(t, map[string]string{
		"SEARCH_URLS":      target.URL,
		"FLARESOLVERR_URL": fs.URL,
		"FETCH_RETRIES":    "1",
	})
	c := New(cfg)
	res, err := c.Fetch(context.Background(), target.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Mode != "tls" {
		t.Errorf("Mode = %q, want tls fallback", res.Mode)
	}
	if !strings.Contains(string(res.Body), "fallback") {
		t.Errorf("body = %q, want fallback content", res.Body)
	}
}

func TestTLSRetriesOnStatus403(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) < 3 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Write([]byte("<html>ok</html>"))
	}))
	defer target.Close()

	cfg := cfgFrom(t, map[string]string{
		"SEARCH_URLS":   target.URL,
		"FETCH_RETRIES": "3",
	})
	c := New(cfg)
	res, err := c.Fetch(context.Background(), target.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(res.Body) != "<html>ok</html>" {
		t.Errorf("body = %q, want <html>ok</html>", res.Body)
	}
	if hits.Load() != 3 {
		t.Errorf("target hits = %d, want 3", hits.Load())
	}
}

func TestTLSExhaustsRetries(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer target.Close()

	cfg := cfgFrom(t, map[string]string{
		"SEARCH_URLS":   target.URL,
		"FETCH_RETRIES": "1",
	})
	c := New(cfg)
	if _, err := c.Fetch(context.Background(), target.URL); err == nil {
		t.Fatal("expected error after exhausting retries")
	}
}

func TestTLSThroughProxy(t *testing.T) {
	var proxied atomic.Bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			t.Errorf("expected CONNECT, got %s %s", r.Method, r.RequestURI)
			return
		}
		proxied.Store(true)
		targetConn, err := net.Dial("tcp", r.Host)
		if err != nil {
			t.Errorf("dial target: %v", err)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("response writer does not support hijacking")
			return
		}
		clientConn, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		go func() { _, _ = io.Copy(targetConn, clientConn) }()
		go func() { _, _ = io.Copy(clientConn, targetConn) }()
	}))
	defer proxy.Close()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>via proxy</html>"))
	}))
	defer target.Close()

	cfg := cfgFrom(t, map[string]string{
		"SEARCH_URLS":    target.URL,
		"ZONAPROP_PROXY": proxy.URL,
		"FETCH_RETRIES":  "0",
	})
	c := New(cfg)
	res, err := c.Fetch(context.Background(), target.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !proxied.Load() {
		t.Error("proxy did not receive a CONNECT request")
	}
	if res.Mode != "tls-proxy" {
		t.Errorf("Mode = %q, want tls-proxy", res.Mode)
	}
}
