package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func envFromMap(m map[string]string) func(string) string {
	return func(key string) string { return m[key] }
}

func TestDefaultsAndRequiredURLs(t *testing.T) {
	_, err := Load(envFromMap(map[string]string{}))
	if err == nil {
		t.Fatal("expected error when no search URLs are provided")
	}
	if !strings.Contains(err.Error(), "search") {
		t.Fatalf("error should mention search urls, got: %v", err)
	}
}

func TestDefaultsApplied(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{
		"SEARCH_URLS": "https://www.zonaprop.com.ar/x.html",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.CheckInterval != 60*time.Minute {
		t.Errorf("CheckInterval default = %v, want 60m", cfg.CheckInterval)
	}
	if cfg.FetchRetries != 3 {
		t.Errorf("FetchRetries default = %d, want 3", cfg.FetchRetries)
	}
	if cfg.FetchTimeout != 30*time.Second {
		t.Errorf("FetchTimeout default = %v, want 30s", cfg.FetchTimeout)
	}
	if cfg.MaxBrowserTimeout != 60*time.Second {
		t.Errorf("MaxBrowserTimeout default = %v, want 60s", cfg.MaxBrowserTimeout)
	}
	if cfg.DataDir != "data" {
		t.Errorf("DataDir default = %q, want data", cfg.DataDir)
	}
	if cfg.FlareSolverrURL != "" || cfg.HTTPProxy != "" {
		t.Errorf("optional network services must default to empty, got fs=%q proxy=%q",
			cfg.FlareSolverrURL, cfg.HTTPProxy)
	}
}

func TestSearchURLsSplitOnNewlineAndComma(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{
		"SEARCH_URLS": "https://a.example/x.html\nhttps://b.example/y.html, https://c.example/z.html",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{
		"https://a.example/x.html",
		"https://b.example/y.html",
		"https://c.example/z.html",
	}
	if len(cfg.SearchURLs) != len(want) {
		t.Fatalf("got %d urls, want %d: %v", len(cfg.SearchURLs), len(want), cfg.SearchURLs)
	}
	for i := range want {
		if cfg.SearchURLs[i] != want[i] {
			t.Errorf("url[%d] = %q, want %q", i, cfg.SearchURLs[i], want[i])
		}
	}
}

func TestSearchURLsDeduplicated(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{
		"SEARCH_URLS": "https://a.example/x.html,\nhttps://a.example/x.html",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.SearchURLs) != 1 {
		t.Errorf("duplicate URLs not removed: %v", cfg.SearchURLs)
	}
}

func TestSearchURLsFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "urls.txt")
	if err := os.WriteFile(path, []byte("https://a.example/x.html\nhttps://b.example/y.html\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(envFromMap(map[string]string{"SEARCH_URLS_FILE": path}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.SearchURLs) != 2 {
		t.Errorf("got %d urls from file, want 2: %v", len(cfg.SearchURLs), cfg.SearchURLs)
	}
}

func TestSearchURLsFromFileAndEnvMerged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "urls.txt")
	if err := os.WriteFile(path, []byte("https://a.example/x.html\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(envFromMap(map[string]string{
		"SEARCH_URLS":      "https://b.example/y.html",
		"SEARCH_URLS_FILE": path,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.SearchURLs) != 2 {
		t.Errorf("got %d urls, want 2 merged: %v", len(cfg.SearchURLs), cfg.SearchURLs)
	}
}

func TestTelegramCredentialsPairing(t *testing.T) {
	cases := []struct {
		name  string
		env   map[string]string
		valid bool
	}{
		{"both empty is valid (dry-run)", map[string]string{"SEARCH_URLS": "u"}, true},
		{"token without chat invalid", map[string]string{"SEARCH_URLS": "u", "TELEGRAM_BOT_TOKEN": "t"}, false},
		{"chat without token invalid", map[string]string{"SEARCH_URLS": "u", "TELEGRAM_CHAT_ID": "c"}, false},
		{"both set valid", map[string]string{"SEARCH_URLS": "u", "TELEGRAM_BOT_TOKEN": "t", "TELEGRAM_CHAT_ID": "c"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(envFromMap(tc.env))
			if tc.valid && err != nil {
				t.Fatalf("expected valid, got error: %v", err)
			}
			if !tc.valid && err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestCheckIntervalParsing(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{
		"SEARCH_URLS":    "u",
		"CHECK_INTERVAL": "5m",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.CheckInterval != 5*time.Minute {
		t.Errorf("CheckInterval = %v, want 5m", cfg.CheckInterval)
	}

	if _, err := Load(envFromMap(map[string]string{
		"SEARCH_URLS":    "u",
		"CHECK_INTERVAL": "not-a-duration",
	})); err == nil {
		t.Fatal("expected error for invalid CHECK_INTERVAL")
	}
}

func TestFetchRetriesParsing(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{
		"SEARCH_URLS":   "u",
		"FETCH_RETRIES": "0",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.FetchRetries != 0 {
		t.Errorf("FetchRetries = %d, want 0", cfg.FetchRetries)
	}
	if _, err := Load(envFromMap(map[string]string{
		"SEARCH_URLS":   "u",
		"FETCH_RETRIES": "abc",
	})); err == nil {
		t.Fatal("expected error for invalid FETCH_RETRIES")
	}
}

func TestFlareSolverrURLTrimmed(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{
		"SEARCH_URLS":      "u",
		"FLARESOLVERR_URL": "http://192.168.1.150:8191/",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.FlareSolverrURL != "http://192.168.1.150:8191" {
		t.Errorf("FlareSolverrURL = %q, want trailing slash trimmed", cfg.FlareSolverrURL)
	}
}

func TestOptionalServicesInvalidURLs(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"invalid flaresolverr url", map[string]string{"SEARCH_URLS": "u", "FLARESOLVERR_URL": "://bad"}},
		{"invalid http proxy", map[string]string{"SEARCH_URLS": "u", "HTTP_PROXY": "://bad"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(envFromMap(tc.env)); err == nil {
				t.Fatal("expected error for invalid URL")
			}
		})
	}
}

func TestTelegramCredentialsRoundTrip(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{
		"SEARCH_URLS":        "https://www.zonaprop.com.ar/a.html",
		"TELEGRAM_BOT_TOKEN": "123:abc",
		"TELEGRAM_CHAT_ID":   "-100123",
		"DATA_DIR":           "/tmp/custom",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.TelegramBotToken != "123:abc" || cfg.TelegramChatID != "-100123" {
		t.Errorf("telegram creds not parsed: %+v", cfg)
	}
	if cfg.DataDir != "/tmp/custom" {
		t.Errorf("DataDir = %q, want /tmp/custom", cfg.DataDir)
	}
}
