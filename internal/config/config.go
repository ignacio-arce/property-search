package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime configuration, sourced from environment variables.
// Optional network services (FlareSolverr, HTTP proxy) are only used when their
// env var is set; otherwise the bot connects directly.
type Config struct {
	TelegramBotToken string
	TelegramChatID   string
	SearchURLs       []string
	CheckInterval    time.Duration

	FlareSolverrURL string // optional
	HTTPProxy       string // optional

	FetchRetries      int
	FetchTimeout      time.Duration
	MaxBrowserTimeout time.Duration

	DataDir string
}

// Load reads configuration from the environment via getenv. It returns an
// error describing the first problem found.
func Load(getenv func(string) string) (*Config, error) {
	cfg := &Config{
		TelegramBotToken:  getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChatID:    getenv("TELEGRAM_CHAT_ID"),
		CheckInterval:     60 * time.Minute,
		FlareSolverrURL:   strings.TrimSuffix(getenv("FLARESOLVERR_URL"), "/"),
		HTTPProxy:         getenv("HTTP_PROXY"),
		FetchRetries:      3,
		FetchTimeout:      30 * time.Second,
		MaxBrowserTimeout: 60 * time.Second,
		DataDir:           "data",
	}

	if (cfg.TelegramBotToken == "") != (cfg.TelegramChatID == "") {
		return nil, errors.New("TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID must be set together (or both omitted for dry-run)")
	}

	var err error
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

	if cfg.FlareSolverrURL != "" && !validHTTPURL(cfg.FlareSolverrURL) {
		return nil, fmt.Errorf("invalid FLARESOLVERR_URL %q", cfg.FlareSolverrURL)
	}
	if cfg.HTTPProxy != "" && !validHTTPURL(cfg.HTTPProxy) {
		return nil, fmt.Errorf("invalid HTTP_PROXY %q", cfg.HTTPProxy)
	}

	return cfg, nil
}

// FromEnv loads configuration from the process environment.
func FromEnv() (*Config, error) {
	return Load(os.Getenv)
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
	if len(urls) == 0 {
		return nil, errors.New("no search URLs configured: set SEARCH_URLS or SEARCH_URLS_FILE")
	}
	return urls, nil
}

func validHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
