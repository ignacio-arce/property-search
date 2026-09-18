package config

import (
	"fmt"
	"log/slog"
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
	// TelegramChatID is an optional default recipient for local runs. Without a
	// token the bot runs in dry-run and prints alerts instead.
	TelegramChatID string

	FlareSolverrURL string // optional
	// ZonapropProxy is the rotating proxy for outgoing Zonaprop requests. It is
	// deliberately NOT called HTTP_PROXY: Go's net/http reads HTTP_PROXY from the
	// environment by default, which would silently route Telegram, FlareSolverr
	// and image requests through the rotating proxy as well.
	ZonapropProxy string // optional

	FetchRetries      int
	FetchTimeout      time.Duration
	MaxBrowserTimeout time.Duration
	// FetchRateLimit is the minimum spacing between outbound Zonaprop requests.
	// It is the primary defence against Cloudflare degrading the IP, so it is
	// tunable rather than hardcoded.
	FetchRateLimit time.Duration

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

	// ScheduleTZ is the IANA zone the daily hour is interpreted in. It defaults to
	// Buenos Aires: an empty value is NOT a safe default, because
	// time.LoadLocation("") returns UTC without error and the digest would fire at
	// 06:00 local.
	ScheduleTZ string
	// DailyHour is the local time the digest runs, "HH:MM".
	DailyHour string
	// RunOnStart runs a cycle at boot. Off by default: with restart: unless-stopped
	// that would fire a digest on every restart.
	RunOnStart bool
	// MaxDaily caps how many listings one user gets per day. The surplus is carried
	// to the next day rather than dropped.
	MaxDaily int
	// LogLevel is the minimum severity the bot emits. Defaults to info, so the
	// per-operation summaries are visible without the per-item debug noise.
	LogLevel slog.Level
}

// Load reads configuration from the environment via getenv. It returns an
// error describing the first problem found.
func Load(getenv func(string) string) (*Config, error) {
	cfg := &Config{
		TelegramBotToken:  getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChatID:    getenv("TELEGRAM_CHAT_ID"),
		FlareSolverrURL:   strings.TrimSuffix(getenv("FLARESOLVERR_URL"), "/"),
		ZonapropProxy:     getenv("ZONAPROP_PROXY"),
		FetchRetries:      3,
		FetchTimeout:      30 * time.Second,
		MaxBrowserTimeout: 60 * time.Second,
		FetchRateLimit:    60 * time.Second,
		PostgresHost:      "localhost",
		PostgresPort:      "5432",
		PostgresUser:      "zonaprop",
		PostgresDB:        "zonaprop",
		ScheduleTZ:        "America/Argentina/Buenos_Aires",
		DailyHour:         "09:00",
		MaxDaily:          15,
		LogLevel:          slog.LevelInfo,
	}

	// TELEGRAM_CHAT_ID is optional: it is only a default target for local runs.
	// In production the recipient comes from the database, so requiring the pair
	// would reject the correct configuration (token set, no default chat).
	var err error

	if err := intEnv(getenv, "FETCH_RETRIES", 0, &cfg.FetchRetries); err != nil {
		return nil, err
	}

	for _, d := range []struct {
		key    string
		target *time.Duration
	}{
		{"FETCH_TIMEOUT", &cfg.FetchTimeout},
		{"MAX_BROWSER_TIMEOUT", &cfg.MaxBrowserTimeout},
		{"FETCH_RATE_LIMIT", &cfg.FetchRateLimit},
	} {
		if err := durationEnv(getenv, d.key, d.target); err != nil {
			return nil, err
		}
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

	if v := getenv("SCHEDULE_TZ"); v != "" {
		cfg.ScheduleTZ = v
	}
	if _, err := time.LoadLocation(cfg.ScheduleTZ); err != nil {
		return nil, fmt.Errorf("invalid SCHEDULE_TZ %q: %w", cfg.ScheduleTZ, err)
	}

	if v := getenv("DAILY_HOUR"); v != "" {
		if _, _, err := parseHour(v); err != nil {
			return nil, err
		}
		cfg.DailyHour = v
	}

	if v := getenv("RUN_ON_START"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("invalid RUN_ON_START %q: must be a boolean", v)
		}
		cfg.RunOnStart = b
	}

	if err := intEnv(getenv, "MAX_DAILY", 1, &cfg.MaxDaily); err != nil {
		return nil, err
	}

	if v := getenv("LOG_LEVEL"); v != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(v)); err != nil {
			return nil, fmt.Errorf("invalid LOG_LEVEL %q: must be one of debug, info, warn, error", v)
		}
	}

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

// durationEnv reads a positive duration, leaving the configured default in place
// when the variable is unset.
func durationEnv(getenv func(string) string, key string, target *time.Duration) error {
	v := getenv(key)
	if v == "" {
		return nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return fmt.Errorf("invalid %s %q: must be a positive duration", key, v)
	}
	*target = d
	return nil
}

// intEnv reads an integer no smaller than min, leaving the default when unset.
func intEnv(getenv func(string) string, key string, min int, target *int) error {
	v := getenv(key)
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < min {
		return fmt.Errorf("invalid %s %q: must be an integer >= %d", key, v, min)
	}
	*target = n
	return nil
}

// parseHour reads "HH:MM" into hour and minute.
func parseHour(v string) (int, int, error) {
	hour, minute, ok := strings.Cut(strings.TrimSpace(v), ":")
	if !ok {
		return 0, 0, fmt.Errorf("invalid DAILY_HOUR %q: expected HH:MM", v)
	}
	h, err := strconv.Atoi(hour)
	if err != nil || h < 0 || h > 23 {
		return 0, 0, fmt.Errorf("invalid DAILY_HOUR %q: hour must be 00-23", v)
	}
	m, err := strconv.Atoi(minute)
	if err != nil || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("invalid DAILY_HOUR %q: minute must be 00-59", v)
	}
	return h, m, nil
}

// DailyHourParts exposes the parsed hour and minute.
func (c *Config) DailyHourParts() (int, int) {
	h, m, err := parseHour(c.DailyHour)
	if err != nil {
		return 9, 0
	}
	return h, m
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

func validHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
