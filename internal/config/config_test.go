package config

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func envFromMap(m map[string]string) func(string) string {
	return func(key string) string { return m[key] }
}

func TestNoSearchURLsIsValid(t *testing.T) {
	// Search URLs are per-user in Postgres now, so the process must be able to boot
	// without any configured in env.
	cfg, err := Load(envFromMap(map[string]string{}))
	if err != nil {
		t.Fatalf("expected no error without search URLs, got: %v", err)
	}
	if len(cfg.SearchURLs) != 0 {
		t.Errorf("SearchURLs = %v, want empty", cfg.SearchURLs)
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
		{"invalid zonaprop proxy", map[string]string{"SEARCH_URLS": "u", "ZONAPROP_PROXY": "://bad"}},
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
