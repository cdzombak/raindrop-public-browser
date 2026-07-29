package config

import (
	"log/slog"
	"testing"
)

func TestLogLevel(t *testing.T) {
	for raw, want := range map[string]slog.Level{
		"":      slog.LevelInfo, // unset
		"debug": slog.LevelDebug,
		"INFO":  slog.LevelInfo, // case-insensitive
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
	} {
		t.Setenv(EnvLogLevel, raw)
		got, err := LogLevel()
		if err != nil {
			t.Errorf("LogLevel() with %s=%q: unexpected error: %v", EnvLogLevel, raw, err)
			continue
		}
		if got != want {
			t.Errorf("LogLevel() with %s=%q = %v, want %v", EnvLogLevel, raw, got, want)
		}
	}

	// A bad value must still yield a usable logger: the caller warns and
	// carries on rather than refusing to start over a logging preference.
	t.Setenv(EnvLogLevel, "verbose")
	got, err := LogLevel()
	if err == nil {
		t.Error("LogLevel() accepted \"verbose\", want an error")
	}
	if got != slog.LevelInfo {
		t.Errorf("LogLevel() fell back to %v, want %v", got, slog.LevelInfo)
	}
}

func TestBaseURL(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		for raw, want := range map[string]string{
			"https://bookmarks.example.com":   "https://bookmarks.example.com",
			"https://bookmarks.example.com/":  "https://bookmarks.example.com",
			"https://bookmarks.example.com//": "https://bookmarks.example.com",
			// A path prefix is legitimate: the app can be mounted under one.
			"https://example.com/bookmarks/": "https://example.com/bookmarks",
			"http://localhost:8080":          "http://localhost:8080",
		} {
			got, err := baseURL(raw)
			if err != nil {
				t.Errorf("baseURL(%q): unexpected error: %v", raw, err)
				continue
			}
			if got != want {
				t.Errorf("baseURL(%q) = %q, want %q", raw, got, want)
			}
		}
	})

	// These would otherwise be pasted verbatim into sitemap entries and
	// canonical links, and go unnoticed until a search engine reads them.
	t.Run("rejected", func(t *testing.T) {
		for _, raw := range []string{
			"",                            // empty
			"/",                           // trims to empty
			"bookmarks.example.com",       // no scheme — parses as a bare path
			"//bookmarks.example.com",     // protocol-relative
			"ftp://bookmarks.example.com", // not http(s)
			"https://",                    // no host
			"https://example.com?x=1",     // query string
			"https://example.com#frag",    // fragment
			"http://exa mple.com",         // unparseable
		} {
			if got, err := baseURL(raw); err == nil {
				t.Errorf("baseURL(%q) = %q, want an error", raw, got)
			}
		}
	})
}
