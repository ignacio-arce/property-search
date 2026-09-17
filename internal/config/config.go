package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// SeedURL is one preseeded search URL with its short label, parsed from SEED_URLS.
// Seeds exist so a developer can exercise the pipeline before onboarding ships.
type SeedURL struct {
	Label string
	URL   string
}

// Config holds all runtime configuration, sourced from environment variables.
// Optional network services are only used when their env var is set; otherwise
// the corresponding path is skipped.
type Config struct {
	TelegramBotToken string
	TelegramChatID   string

	// SearchURLs is deprecated. Search URLs are per-user rows in Postgres now
	// (seeded via SEED_URLS or added through /start). Kept temporarily so the v1
	// bot and probe keep working; V7.3 removes the field entirely.
	SearchURLs []string
	// CheckInterval is deprecated along with the ticker loop it drove; the daily
	// scheduler replaces it. Removed in V7.3.
	CheckInterval time.Duration

	FlareSolverrURL string // optional
	// ZonapropProxy is the rotating proxy for outgoing Zonaprop requests. It is
	// deliberately NOT called HTTP_PROXY: Go's net/http reads HTTP_PROXY from the
	// environment by default, which would silently route Telegram, FlareSolverr
	// and image requests through the rotating proxy as well.
	ZonapropProxy string // optional

	FetchRetries      int
	FetchTimeout      time.Duration
	MaxBrowserTimeout time.Duration

	DataDir string

	// Postgres connection parts. The DSN is assembled in Go by DatabaseURL rather
	// than interpolated in compose, so a password containing URL metacharacters
	// (a strong one) survives instead of producing an auth error with a confusing
	// cause.
	PostgresHost     string
	PostgresPort     string
	PostgresUser     string
	PostgresPassword string
	PostgresDB       string

	// SeedChatID and SeedURLs preseed one user at startup.
	SeedChatID string
	SeedURLs   []SeedURL
}

// Load reads configuration from the environment via getenv. It returns an
// error describing the first problem found.
func Load(getenv func(string) string) (*Config, error) {
	cfg := &Config{
		TelegramBotToken:  getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChatID:    getenv("TELEGRAM_CHAT_ID"),
		CheckInterval:     60 * time.Minute,
		FlareSolverrURL:   strings.TrimSuffix(getenv("FLARESOLVERR_URL"), "/"),
		ZonapropProxy:     getenv("ZONAPROP_PROXY"),
		FetchRetries:      3,
		FetchTimeout:      30 * time.Second,
		MaxBrowserTimeout: 60 * time.Second,
		DataDir:           "data",
		PostgresHost:      "localhost",
		PostgresPort:      "5432",
		PostgresUser:      "zonaprop",
		PostgresDB:        "zonaprop",
	}

	if (cfg.TelegramBotToken == "") != (cfg.TelegramChatID == "") {
		return nil, errors.New("TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID must be set together (or both omitted for dry-run)")
	}

	var err error
	// Deprecated and optional: an empty result is valid now that search URLs live
	// in Postgres. V7.3 removes this block.
	if cfg.SearchURLs, err = searchURLs(getenv("SEARCH_URLS"), getenv("SEARCH_URLS_FILE")); err != nil {
		return nil, err
	}

	if v := getenv("CHECK_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("invalid CHECK_INTERVAL %q: %w", v, err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("CHECK_INTERVAL must be positive, got %q", v)
		}
		cfg.CheckInterval = d
	}

	if v := getenv("FETCH_RETRIES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("invalid FETCH_RETRIES %q: must be a non-negative integer", v)
		}
		cfg.FetchRetries = n
	}

	if v := getenv("FETCH_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("invalid FETCH_TIMEOUT %q: must be a positive duration", v)
		}
		cfg.FetchTimeout = d
	}

	if v := getenv("MAX_BROWSER_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("invalid MAX_BROWSER_TIMEOUT %q: must be a positive duration", v)
		}
		cfg.MaxBrowserTimeout = d
	}

	if v := getenv("DATA_DIR"); v != "" {
		cfg.DataDir = v
	}

	if v := getenv("POSTGRES_HOST"); v != "" {
		cfg.PostgresHost = v
	}
	if v := getenv("POSTGRES_PORT"); v != "" {
		cfg.PostgresPort = v
	}
	if v := getenv("POSTGRES_USER"); v != "" {
		cfg.PostgresUser = v
	}
	if v := getenv("POSTGRES_DB"); v != "" {
		cfg.PostgresDB = v
	}
	cfg.PostgresPassword = getenv("POSTGRES_PASSWORD")

	cfg.SeedChatID = getenv("SEED_CHAT_ID")
	if cfg.SeedURLs, err = seedURLs(getenv("SEED_URLS")); err != nil {
		return nil, err
	}

	if cfg.FlareSolverrURL != "" && !validHTTPURL(cfg.FlareSolverrURL) {
		return nil, fmt.Errorf("invalid FLARESOLVERR_URL %q", cfg.FlareSolverrURL)
	}
	if cfg.ZonapropProxy != "" && !validHTTPURL(cfg.ZonapropProxy) {
		return nil, fmt.Errorf("invalid ZONAPROP_PROXY %q", cfg.ZonapropProxy)
	}

	return cfg, nil
}

// FromEnv loads configuration from the process environment.
func FromEnv() (*Config, error) {
	return Load(os.Getenv)
}

// DatabaseURL assembles the Postgres DSN. The password is escaped through
// url.UserPassword, so values containing @ : / # % do not corrupt the DSN.
func (c *Config) DatabaseURL() string {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.PostgresUser, c.PostgresPassword),
		Host:   net.JoinHostPort(c.PostgresHost, c.PostgresPort),
		Path:   "/" + c.PostgresDB,
	}
	return u.String()
}

// seedURLs parses SEED_URLS: comma-separated entries of the form "label|url", on
// a single line so it works through env_file (which breaks on unquoted
// multi-line values). Labels cannot contain "|" or ",".
func seedURLs(raw string) ([]SeedURL, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out []SeedURL
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		label, u, ok := strings.Cut(entry, "|")
		if !ok {
			return nil, fmt.Errorf("invalid SEED_URLS entry %q: expected \"label|url\"", entry)
		}
		label, u = strings.TrimSpace(label), strings.TrimSpace(u)
		if label == "" || u == "" {
			return nil, fmt.Errorf("invalid SEED_URLS entry %q: empty label or url", entry)
		}
		out = append(out, SeedURL{Label: label, URL: u})
	}
	return out, nil
}

func searchURLs(env, file string) ([]string, error) {
	var raw []string
	if env != "" {
		raw = append(raw, env)
	}
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("reading SEARCH_URLS_FILE %q: %w", file, err)
		}
		raw = append(raw, string(b))
	}

	var urls []string
	seen := make(map[string]bool)
	for _, chunk := range raw {
		for _, u := range strings.FieldsFunc(chunk, func(r rune) bool { return r == '\n' || r == ',' }) {
			u = strings.TrimSpace(u)
			if u == "" {
				continue
			}
			if !seen[u] {
				seen[u] = true
				urls = append(urls, u)
			}
		}
	}
	// An empty result is intentionally not an error: URLs come from Postgres now.
	return urls, nil
}

func validHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
