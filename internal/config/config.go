// Package config loads application configuration from environment variables.
package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Env variable names. All app configuration is via environment; see README.
const (
	EnvClientID        = "RAINDROP_CLIENT_ID"
	EnvClientSecret    = "RAINDROP_CLIENT_SECRET" //nolint:gosec // env var name, not a credential
	EnvOAuthStateFile  = "OAUTH_STATE_FILE"
	EnvOAuthRedirect   = "OAUTH_REDIRECT_URI"
	EnvDBDir           = "DB_DIR"
	EnvTemplateDir     = "TEMPLATE_DIR"
	EnvImagesDir       = "IMAGES_DIR"
	EnvListenAddr      = "LISTEN_ADDR"
	EnvBaseURL         = "BASE_URL"
	EnvRefreshInterval = "REFRESH_INTERVAL_MINUTES"
	EnvTag             = "PUBLIC_TAG"
	EnvPerPage         = "PER_PAGE"
	EnvDateFormat      = "DATE_FORMAT"
	EnvTimezone        = "DISPLAY_TIMEZONE"
	EnvLogLevel        = "LOG_LEVEL"
	// EnvInDocker is set by the Dockerfile. When present, the OAuth callback
	// server binds 0.0.0.0 instead of the redirect URI's host, so a published
	// container port (docker run -p) can reach it.
	EnvInDocker = "RAINDROP_PUBLIC_BROWSER_IN_DOCKER"
)

// Config is the resolved application configuration.
type Config struct {
	ClientID       string
	ClientSecret   string
	OAuthStateFile string
	OAuthRedirect  string

	DBDir       string
	TemplateDir string
	ImagesDir   string

	ListenAddr string
	BaseURL    string // external base URL, no trailing slash; used for sitemap and canonical links

	RefreshInterval time.Duration
	Tag             string
	PerPage         int

	DateFormat string
	Location   *time.Location

	InDocker bool
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Load reads configuration from the environment. It validates only what every
// mode needs; mode-specific requirements (e.g. the template dir for serve) are
// checked by the callers that need them.
func Load() (*Config, error) {
	c := &Config{
		ClientID:       os.Getenv(EnvClientID),
		ClientSecret:   os.Getenv(EnvClientSecret),
		OAuthStateFile: os.Getenv(EnvOAuthStateFile),
		OAuthRedirect:  getenvDefault(EnvOAuthRedirect, "http://localhost:8080/oauth"),
		DBDir:          os.Getenv(EnvDBDir),
		TemplateDir:    os.Getenv(EnvTemplateDir),
		ImagesDir:      os.Getenv(EnvImagesDir),
		ListenAddr:     getenvDefault(EnvListenAddr, ":8080"),
		Tag:            getenvDefault(EnvTag, "_public"),
		DateFormat:     getenvDefault(EnvDateFormat, "January 2, 2006"),
		InDocker:       os.Getenv(EnvInDocker) != "",
	}

	base, err := baseURL(getenvDefault(EnvBaseURL, "http://localhost:8080"))
	if err != nil {
		return nil, err
	}
	c.BaseURL = base

	interval, err := envInt(EnvRefreshInterval, 15)
	if err != nil {
		return nil, err
	}
	if interval < 1 {
		return nil, fmt.Errorf("%s must be >= 1", EnvRefreshInterval)
	}
	c.RefreshInterval = time.Duration(interval) * time.Minute

	c.PerPage, err = envInt(EnvPerPage, 10)
	if err != nil {
		return nil, err
	}
	if c.PerPage < 1 {
		return nil, fmt.Errorf("%s must be >= 1", EnvPerPage)
	}

	tz := getenvDefault(EnvTimezone, "UTC")
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("invalid %s %q: %w", EnvTimezone, tz, err)
	}
	c.Location = loc

	return c, nil
}

// baseURL validates and canonicalizes the external base URL. It has to be
// absolute: it is pasted verbatim into sitemap entries and canonical links,
// where a scheme-less value like "bookmarks.example.com" would be silently
// wrong in a way nobody notices until a search engine does. For the same
// reason it has to name the site root, with no path of its own.
func baseURL(raw string) (string, error) {
	trimmed := strings.TrimRight(raw, "/")
	if trimmed == "" {
		return "", fmt.Errorf("%s must not be empty", EnvBaseURL)
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid %s %q: %w", EnvBaseURL, raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("invalid %s %q: must be an absolute URL beginning with http:// or https://", EnvBaseURL, raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("invalid %s %q: no host", EnvBaseURL, raw)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("invalid %s %q: must not carry a query string or fragment", EnvBaseURL, raw)
	}
	// The app serves from the site root: pages link to "/2", covers to
	// "/covers/…". A base URL with a path would put that prefix into the
	// canonical links, the sitemap and robots.txt while every link on every
	// page kept pointing at the root — half-broken in whichever direction
	// the reverse proxy was configured.
	if u.Path != "" {
		return "", fmt.Errorf("invalid %s %q: must not include a path; the app serves from the site root", EnvBaseURL, raw)
	}
	return trimmed, nil
}

// LogLevel resolves $LOG_LEVEL to a slog level, defaulting to info. It is
// separate from Load because logging is set up before configuration is read —
// a configuration error is itself something to log.
func LogLevel() (slog.Level, error) {
	v := os.Getenv(EnvLogLevel)
	if v == "" {
		return slog.LevelInfo, nil
	}
	// UnmarshalText accepts the level names case-insensitively, plus offsets
	// like "warn+2"; the error it returns names neither the variable nor the
	// accepted values, so it is replaced.
	var l slog.Level
	if err := l.UnmarshalText([]byte(v)); err != nil {
		return slog.LevelInfo, fmt.Errorf("invalid %s %q: want debug, info, warn, or error", EnvLogLevel, v)
	}
	return l, nil
}

func envInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", key, v, err)
	}
	return n, nil
}
