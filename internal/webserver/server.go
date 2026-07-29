// Package webserver serves the prerendered snapshot, search, covers, and
// status endpoints.
package webserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cdzombak/raindrop-public-browser/internal/covers"
	"github.com/cdzombak/raindrop-public-browser/internal/norm"
	"github.com/cdzombak/raindrop-public-browser/internal/render"
	"github.com/cdzombak/raindrop-public-browser/internal/store"
)

// Server holds the state shared by all handlers. The current snapshot is
// swapped atomically after each refresh; requests always serve a complete,
// consistent snapshot.
type Server struct {
	store    *store.Store
	renderer *render.Renderer
	// images is the cover directory as a restricted root: every path
	// resolved through it stays inside, so no request can escape it.
	images  *os.Root
	baseURL string
	logger  *slog.Logger

	snapshot      atomic.Pointer[render.Snapshot]
	lastRefreshOK atomic.Bool
}

// New creates a Server serving the given initial snapshot.
// last_refresh_ok starts true, until the initial refresh runs and we know
// whether it succeeded.
//
// The images directory is opened here, once, rather than per cover request.
// It must already exist. Call Close when done with the server.
func New(st *store.Store, r *render.Renderer, imagesDir, baseURL string, snap *render.Snapshot, logger *slog.Logger) (*Server, error) {
	images, err := os.OpenRoot(imagesDir)
	if err != nil {
		return nil, fmt.Errorf("open images directory: %w", err)
	}
	s := &Server{
		store:    st,
		renderer: r,
		images:   images,
		baseURL:  baseURL,
		logger:   logger,
	}
	s.snapshot.Store(snap)
	s.lastRefreshOK.Store(true)
	return s, nil
}

// Close releases the handle on the images directory.
func (s *Server) Close() error { return s.images.Close() }

// SetSnapshot atomically swaps in a freshly prerendered snapshot.
func (s *Server) SetSnapshot(snap *render.Snapshot) { s.snapshot.Store(snap) }

// SetLastRefreshOK records the outcome of the most recent refresh.
func (s *Server) SetLastRefreshOK(ok bool) { s.lastRefreshOK.Store(ok) }

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { s.servePage(w, r, 1) })
	mux.HandleFunc("GET /search", s.serveSearch)
	mux.HandleFunc("GET /search/results", s.serveSearchResults)
	mux.HandleFunc("GET /covers/{file}", s.serveCover)
	mux.HandleFunc("GET /robots.txt", s.serveRobots)
	mux.HandleFunc("GET /sitemap.xml", s.serveSitemap)
	mux.HandleFunc("GET /_status", s.serveStatus)
	mux.HandleFunc("GET /{page}", s.serveNumberedPage)
	// Everything else (multi-segment paths, unmatched methods) 404s with
	// the no-store header error responses require.
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { s.notFound(w) })
	return stripTrailingSlash(mux)
}

// stripTrailingSlash redirects any path ending in "/" (except the root) to
// the same path without it.
func stripTrailingSlash(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if len(p) > 1 && strings.HasSuffix(p, "/") {
			trimmed := strings.TrimRight(p, "/")
			// Collapse leading slashes so a path like "//host/" cannot
			// become a protocol-relative (open) redirect.
			for len(trimmed) > 1 && trimmed[1] == '/' {
				trimmed = trimmed[1:]
			}
			if trimmed == "" {
				trimmed = "/"
			}
			u := url.URL{Path: trimmed, RawQuery: r.URL.RawQuery}
			http.Redirect(w, r, u.String(), http.StatusMovedPermanently) //nolint:gosec // path-only URL, leading slashes collapsed above
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) notFound(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "404 page not found", http.StatusNotFound)
}

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.logger.Error("internal error", "path", r.URL.Path, "error", err)
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "500 internal server error", http.StatusInternalServerError)
}

// servePrerendered serves an in-memory page with caching headers and correct
// conditional-request and HEAD handling (via http.ServeContent).
func (s *Server) servePrerendered(w http.ResponseWriter, r *http.Request, p *render.Page, lastMod time.Time) {
	w.Header().Set("Content-Type", p.ContentType)
	w.Header().Set("ETag", p.ETag)
	w.Header().Set("Cache-Control", "public, max-age=300")
	http.ServeContent(w, r, "", lastMod, bytes.NewReader(p.Body))
}

func (s *Server) servePage(w http.ResponseWriter, r *http.Request, page int) {
	snap := s.snapshot.Load()
	p, ok := snap.Lists[page]
	if !ok {
		s.notFound(w)
		return
	}
	s.servePrerendered(w, r, p, snap.Refreshed)
}

func (s *Server) serveNumberedPage(w http.ResponseWriter, r *http.Request) {
	seg := r.PathValue("page")
	page, err := strconv.Atoi(seg)
	if err != nil || page < 1 || strconv.Itoa(page) != seg {
		s.notFound(w)
		return
	}
	s.servePage(w, r, page)
}

func (s *Server) serveSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		// "?q=", "?q=%20", "?q=%20%20" … are unboundedly many URLs that would
		// each serve — and let a cache store — an identical copy of the
		// prerendered page. Anything with a query string that does not amount
		// to a query collapses onto the canonical URL. no-store for the same
		// reason: caching the redirects just moves the unbounded key space
		// rather than closing it.
		if r.URL.RawQuery != "" {
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, "/search", http.StatusFound)
			return
		}
		// The prerendered search page (empty state).
		snap := s.snapshot.Load()
		s.servePrerendered(w, r, snap.Search, snap.Refreshed)
		return
	}
	// No-JS full-page results. Never cached.
	d, err := s.resultsData(r, q)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	body, err := s.renderer.SearchPage(d)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body) //nolint:gosec // body is html/template output with contextual autoescaping
}

func (s *Server) serveSearchResults(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	d, err := s.resultsData(r, q)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	body, err := s.renderer.ResultsFragment(d)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body) //nolint:gosec // body is html/template output with contextual autoescaping
}

// maxQueryLen bounds a search query, in runes. Every token in a query becomes
// its own prefix term in the FTS5 match expression, and every search runs on
// the store's single connection — so an unbounded query is unbounded work
// that blocks every other search and the refresher behind it. Nothing else
// rate-limits this endpoint.
//
// Over-long queries are truncated rather than rejected: no real search is
// this long, and truncating still answers with something sensible.
const maxQueryLen = 128

// capQuery truncates q to maxQueryLen runes. Cutting mid-token is harmless —
// every token is a prefix term already.
func capQuery(q string) string {
	runes := []rune(q)
	if len(runes) <= maxQueryLen {
		return q
	}
	return strings.TrimSpace(string(runes[:maxQueryLen]))
}

// resultsData executes the query (if valid) and builds the template data.
// Empty, whitespace-only, unmatchable, and too-short queries all return a
// non-results state with HTTP 200.
func (s *Server) resultsData(r *http.Request, q string) (render.ResultsData, error) {
	if q == "" {
		return render.EmptyResults(), nil
	}
	q = capQuery(q)
	// Minimum length applies to the whole normalized query, not per token:
	// while typing "sqlite s", the trailing 1-char token is a legitimate
	// prefix filter.
	if len([]rune(norm.Normalize(q))) < render.MinQueryLen {
		return render.TooShortResults(q), nil
	}
	bms, truncated, err := s.store.Search(r.Context(), q)
	if err != nil {
		return render.ResultsData{}, fmt.Errorf("search %q: %w", q, err)
	}
	return s.renderer.Results(q, bms, truncated), nil
}

func (s *Server) serveCover(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	ctype := covers.ContentTypeForFilename(name)
	if ctype == "" {
		s.notFound(w)
		return
	}
	f, err := s.images.Open(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.notFound(w)
			return
		}
		s.serverError(w, r, err)
		return
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", ctype)
	// These are bytes fetched from arbitrary third-party hosts; browsers
	// must not be allowed to reinterpret them.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Safe because filenames are content-addressed by source URL.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("ETag", fmt.Sprintf(`"%x-%x"`, fi.ModTime().UnixNano(), fi.Size()))
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

func (s *Server) serveRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = fmt.Fprintf(w, "User-agent: *\nDisallow: /search\n\nSitemap: %s/sitemap.xml\n", s.baseURL)
}

func (s *Server) serveSitemap(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot.Load()
	s.servePrerendered(w, r, snap.Sitemap, snap.Refreshed)
}

func (s *Server) serveStatus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]bool{
		"up":              true,
		"last_refresh_ok": s.lastRefreshOK.Load(),
	})
}
