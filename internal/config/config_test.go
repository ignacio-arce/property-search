package config

import (
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"
)

func envFromMap(m map[string]string) func(string) string {
	return func(key string) string { return m[key] }
}

func TestDefaultsApplied(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
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
	if cfg.FlareSolverrURL != "" || cfg.ZonapropProxy != "" {
		t.Errorf("optional network services must default to empty, got fs=%q proxy=%q",
			cfg.FlareSolverrURL, cfg.ZonapropProxy)
	}
}

func TestZonapropProxyParsed(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{
		"ZONAPROP_PROXY": "http://192.168.1.150:8089",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ZonapropProxy != "http://192.168.1.150:8089" {
		t.Errorf("ZonapropProxy = %q", cfg.ZonapropProxy)
	}
}

func TestHTTPProxyIsIgnored(t *testing.T) {
	// The app proxy variable is deliberately NOT named HTTP_PROXY: Go's net/http
	// reads HTTP_PROXY from the environment by default, which would silently route
	// Telegram and FlareSolverr traffic through the rotating proxy.
	cfg, err := Load(envFromMap(map[string]string{
		"HTTP_PROXY": "http://127.0.0.1:3128",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ZonapropProxy != "" {
		t.Errorf("HTTP_PROXY must not populate ZonapropProxy, got %q", cfg.ZonapropProxy)
	}
}

func TestPostgresDefaultsAndDSN(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{
		"POSTGRES_PASSWORD": "s3cret",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.PostgresHost != "localhost" || cfg.PostgresPort != "5432" {
		t.Errorf("postgres host/port defaults = %q/%q", cfg.PostgresHost, cfg.PostgresPort)
	}
	want := "postgres://zonaprop:s3cret@localhost:5432/zonaprop"
	if got := cfg.DatabaseURL(); got != want {
		t.Errorf("DatabaseURL() = %q, want %q", got, want)
	}
}

func TestDatabaseURLEscapesPasswordMetacharacters(t *testing.T) {
	// A strong password containing @ : / # % must survive DSN assembly. Interpolating
	// it in compose instead would produce a DSN pgx parses wrongly, surfacing as an
	// auth error that points at the wrong cause.
	const pass = "p@ss:w/rd#1%"
	cfg, err := Load(envFromMap(map[string]string{
		"POSTGRES_USER":     "u",
		"POSTGRES_PASSWORD": pass,
		"POSTGRES_HOST":     "db",
		"POSTGRES_PORT":     "6000",
		"POSTGRES_DB":       "d",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	parsed, err := url.Parse(cfg.DatabaseURL())
	if err != nil {
		t.Fatalf("DatabaseURL() is not a valid URL: %v", err)
	}
	if got := parsed.User.Username(); got != "u" {
		t.Errorf("username = %q, want u", got)
	}
	gotPass, _ := parsed.User.Password()
	if gotPass != pass {
		t.Errorf("password = %q, want %q", gotPass, pass)
	}
	if parsed.Host != "db:6000" {
		t.Errorf("host = %q, want db:6000", parsed.Host)
	}
	if parsed.Path != "/d" {
		t.Errorf("path = %q, want /d", parsed.Path)
	}
}

func TestSeedURLsParsing(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{
		"SEED_CHAT_ID": "-100123",
		"SEED_URLS":    "San Isidro 3amb|https://www.zonaprop.com.ar/a.html, Pilar|https://www.zonaprop.com.ar/b.html",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.SeedChatID != "-100123" {
		t.Errorf("SeedChatID = %q", cfg.SeedChatID)
	}
	want := []SeedURL{
		{Label: "San Isidro 3amb", URL: "https://www.zonaprop.com.ar/a.html"},
		{Label: "Pilar", URL: "https://www.zonaprop.com.ar/b.html"},
	}
	if len(cfg.SeedURLs) != len(want) {
		t.Fatalf("got %d seed urls, want %d: %+v", len(cfg.SeedURLs), len(want), cfg.SeedURLs)
	}
	for i := range want {
		if cfg.SeedURLs[i] != want[i] {
			t.Errorf("seed[%d] = %+v, want %+v", i, cfg.SeedURLs[i], want[i])
		}
	}
}

func TestSeedURLsRejectsMalformedEntries(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"missing separator", "https://www.zonaprop.com.ar/a.html"},
		{"empty label", "|https://www.zonaprop.com.ar/a.html"},
		{"empty url", "San Isidro|"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(envFromMap(map[string]string{"SEED_URLS": tc.raw}))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "SEED_URLS") {
				t.Errorf("error should name SEED_URLS, got: %v", err)
			}
		})
	}
}

// The chat id is only a default target for local runs; the production recipient
// comes from the database, so a token without a chat id is a valid configuration.
func TestTelegramChatIDIsOptional(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"token alone is valid (production)", map[string]string{"TELEGRAM_BOT_TOKEN": "t"}},
		{"chat alone is harmless (dry-run)", map[string]string{"TELEGRAM_CHAT_ID": "c"}},
		{"neither is valid (dry-run)", map[string]string{}},
		{"both is valid (local dev)", map[string]string{"TELEGRAM_BOT_TOKEN": "t", "TELEGRAM_CHAT_ID": "c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(envFromMap(tc.env)); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
func TestFetchRetriesParsing(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{
		"FETCH_RETRIES": "0",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.FetchRetries != 0 {
		t.Errorf("FetchRetries = %d, want 0", cfg.FetchRetries)
	}
	if _, err := Load(envFromMap(map[string]string{
		"FETCH_RETRIES": "abc",
	})); err == nil {
		t.Fatal("expected error for invalid FETCH_RETRIES")
	}
}

func TestFetchRateLimitParsing(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.FetchRateLimit != 60*time.Second {
		t.Errorf("FetchRateLimit default = %v, want 60s", cfg.FetchRateLimit)
	}

	cfg, err = Load(envFromMap(map[string]string{"FETCH_RATE_LIMIT": "10m"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.FetchRateLimit != 10*time.Minute {
		t.Errorf("FetchRateLimit = %v, want 10m", cfg.FetchRateLimit)
	}

	if _, err := Load(envFromMap(map[string]string{"FETCH_RATE_LIMIT": "0s"})); err == nil {
		t.Fatal("expected error for a non-positive FETCH_RATE_LIMIT")
	}
}

func TestLogLevelParsing(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel default = %v, want INFO", cfg.LogLevel)
	}

	cases := []struct {
		raw  string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
		{"info", slog.LevelInfo},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			cfg, err := Load(envFromMap(map[string]string{"LOG_LEVEL": tc.raw}))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.LogLevel != tc.want {
				t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, tc.want)
			}
		})
	}
}

func TestLogLevelRejectsUnknownValue(t *testing.T) {
	_, err := Load(envFromMap(map[string]string{"LOG_LEVEL": "verbose"}))
	if err == nil {
		t.Fatal("expected error for an unknown LOG_LEVEL")
	}
	if !strings.Contains(err.Error(), "LOG_LEVEL") {
		t.Errorf("error should name LOG_LEVEL, got: %v", err)
	}
}

func TestFlareSolverrURLTrimmed(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{
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
		{"invalid flaresolverr url", map[string]string{"FLARESOLVERR_URL": "://bad"}},
		{"invalid zonaprop proxy", map[string]string{"ZONAPROP_PROXY": "://bad"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(envFromMap(tc.env)); err == nil {
				t.Fatal("expected error for invalid URL")
			}
		})
	}
}
