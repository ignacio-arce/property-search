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

func fsReply(status string, solutionStatus int, body string) string {
	statusField := `"status":"` + status + `"`
	if status == "ok" {
		return `{` + statusField + `,"message":"","solution":{"status":` +
			strconv.Itoa(solutionStatus) + `,"response":` + jsonString(body) + `}}`
	}
	// For the error case the third argument is the message, so tests can exercise
	// the challenge wording that FlareSolverr actually returns.
	return `{` + statusField + `,"message":` + jsonString(body) + `}`
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

// FlareSolverr must not be retried: each attempt launches a browser, so retrying
// it multiplies the damage on an already-suspicious IP. The TLS loop carries the
// retries instead.
func TestFlareSolverrIsNotRetried(t *testing.T) {
	var hits atomic.Int32
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(fsReply("error", 0, "flaresolverr down")))
	}))
	defer fs.Close()

	cfg := cfgFrom(t, map[string]string{
		"FLARESOLVERR_URL": fs.URL,
		"FETCH_RETRIES":    "0",
	})
	c := New(cfg)
	if _, err := c.Fetch(context.Background(), "https://target.invalid/x.html"); err == nil {
		t.Fatal("expected failure when FlareSolverr is down and the target is unreachable")
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("FlareSolverr hits = %d, want 1", got)
	}
}

func TestFlareSolverrChallengeStopsImmediately(t *testing.T) {
	var fsHits atomic.Int32
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fsHits.Add(1)
		w.Write([]byte(fsReply("error", 0, "Error solving the challenge. Timeout after 60.0 seconds.")))
	}))
	defer fs.Close()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("TLS must not be attempted after a challenge: it would burn the IP further")
	}))
	defer target.Close()

	cfg := cfgFrom(t, map[string]string{
		"FLARESOLVERR_URL": fs.URL,
		"FETCH_RETRIES":    "3",
	})
	c := New(cfg)
	_, err := c.Fetch(context.Background(), target.URL)
	if err == nil {
		t.Fatal("expected an error")
	}
	if kind, ok := KindOf(err); !ok || kind != KindBlocked {
		t.Errorf("kind = %v (ok=%v), want blocked", kind, ok)
	}
	if got := fsHits.Load(); got != 1 {
		t.Errorf("FlareSolverr hits = %d, want 1", got)
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

func TestResultCarriesStatusAndHeader(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test-Marker", "present")
		w.Write([]byte("<html>ok</html>"))
	}))
	defer target.Close()

	cfg := cfgFrom(t, map[string]string{
		"FETCH_RETRIES": "0",
	})
	res, err := New(cfg).Fetch(context.Background(), target.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Status != http.StatusOK {
		t.Errorf("Status = %d, want 200", res.Status)
	}
	if got := res.Header.Get("X-Test-Marker"); got != "present" {
		t.Errorf("Header not propagated, X-Test-Marker = %q", got)
	}
}

func TestForbiddenWithChallengeIsNotRetried(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// This is exactly what Zonaprop answered during the probes.
		w.Header().Set("cf-mitigated", "challenge")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer target.Close()

	cfg := cfgFrom(t, map[string]string{
		"FETCH_RETRIES": "3",
	})
	_, err := New(cfg).Fetch(context.Background(), target.URL)
	if err == nil {
		t.Fatal("expected an error")
	}
	if kind, ok := KindOf(err); !ok || kind != KindBlocked {
		t.Errorf("kind = %v (ok=%v), want blocked", kind, ok)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("target hits = %d, want 1: a challenge must not be retried", got)
	}
}

func TestTransportErrorIsClassified(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := target.URL
	target.Close() // nothing is listening now

	cfg := cfgFrom(t, map[string]string{
		"FETCH_RETRIES": "0",
	})
	_, err := New(cfg).Fetch(context.Background(), url)
	if err == nil {
		t.Fatal("expected an error")
	}
	if kind, ok := KindOf(err); !ok || kind != KindTransport {
		t.Errorf("kind = %v (ok=%v), want transport", kind, ok)
	}
}

// FlareSolverr wraps every failure as "Error solving the challenge", so the word
// alone is not evidence of a Cloudflare block: a connection error means the
// challenge was never reached, and backing the gate off for it would be wrong.
func TestClassifyFlareSolverrFailure(t *testing.T) {
	cases := []struct {
		message string
		want    ErrorKind
	}{
		{"Error solving the challenge. Timeout after 60.0 seconds.", KindBlocked},
		{"Error solving the challenge. Message: unknown error: net::ERR_CONNECTION_REFUSED", KindTransport},
		{"Error solving the challenge. Message: unknown error: net::ERR_NAME_NOT_RESOLVED", KindTransport},
		// A bare timeout has no challenge marker: a slow page must not trigger the
		// cooldown and must not stop the TLS fallback.
		{"Timeout after 60.0 seconds", KindHTTPStatus},
		{"something else entirely", KindHTTPStatus},
	}
	for _, tc := range cases {
		if got := classifyFlareSolverrFailure(tc.message); got != tc.want {
			t.Errorf("classifyFlareSolverrFailure(%q) = %v, want %v", tc.message, got, tc.want)
		}
	}
}
