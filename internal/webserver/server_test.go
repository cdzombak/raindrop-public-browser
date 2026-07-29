package webserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cdzombak/raindrop-public-browser/internal/render"
	"github.com/cdzombak/raindrop-public-browser/internal/store"
)

// --- test fixtures & helpers -------------------------------------------------

// exampleTemplateDir is the shipped example template set. The handler tests
// deliberately render through it, so they double as verification that the
// example remains complete and current.
const exampleTemplateDir = "../../example-template"

const (
	testPerPage = 10
	testBaseURL = "https://bookmarks.example.com"
	// testCoverFile follows covers.FilenamePattern: "<raindropID>-<8 hex>.<ext>".
	testCoverFile = "1-0011aabb.jpg"
)

var (
	// fixedRefresh is the snapshot timestamp, used for Last-Modified.
	fixedRefresh = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	// fixedCreated anchors every fixture bookmark's creation time.
	fixedCreated = time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC)
)

// fixtureBookmarks returns 25 bookmarks — enough for exactly 3 pages at
// testPerPage. 23 are deterministically numbered; two carry realistic titles
// the search tests query for. Creation times run backwards from fixedCreated
// so "Bookmark 01" (the one given a cover) lands on page 1.
func fixtureBookmarks() []store.Bookmark {
	const numbered = 23
	bms := make([]store.Bookmark, 0, numbered+2)
	for i := 1; i <= numbered; i++ {
		bms = append(bms, store.Bookmark{
			ID:      int64(i),
			Title:   fmt.Sprintf("Bookmark %02d", i),
			Excerpt: fmt.Sprintf("Excerpt for bookmark %02d.", i),
			URL:     fmt.Sprintf("https://example.com/bookmark/%02d", i),
			Created: fixedCreated.Add(-time.Duration(i) * time.Hour),
		})
	}
	return append(bms,
		store.Bookmark{
			ID:      101,
			Title:   "SQLite for Servers",
			Excerpt: "Notes on running SQLite in production.",
			URL:     "https://news.ycombinator.com/item?id=1",
			Created: fixedCreated.Add(2 * time.Hour),
		},
		store.Bookmark{
			ID:      102,
			Title:   "Goroutines in Depth",
			Excerpt: "How the Go scheduler works.",
			URL:     "https://example.org/goroutines",
			Created: fixedCreated.Add(1 * time.Hour),
		},
	)
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func newExampleRenderer(t *testing.T) *render.Renderer {
	t.Helper()
	r, err := render.Load(exampleTemplateDir, render.Config{
		PerPage:    testPerPage,
		BaseURL:    testBaseURL,
		DateFormat: "January 2, 2006",
		Location:   time.UTC,
		Version:    "test",
	})
	if err != nil {
		t.Fatalf("render.Load(%s): %v", exampleTemplateDir, err)
	}
	return r
}

// env is a fully wired server under httptest, plus the pieces tests need to
// poke at directly.
type env struct {
	server    *Server
	store     *store.Store
	imagesDir string
	baseURL   string // the httptest server's origin

	// client does not follow redirects, so redirect status and Location are
	// assertable.
	client *http.Client
	// following follows redirects, for the cases where net/http's ServeMux
	// canonicalizes the path before any handler sees it.
	following *http.Client
}

func (e *env) url(path string) string { return e.baseURL + path }

// newEnv builds a store seeded with the given bookmarks, prerenders a
// snapshot through the example templates, and serves it over httptest.
func newEnv(t *testing.T, bms []store.Bookmark) *env {
	t.Helper()
	ctx := context.Background()

	st := newTestStore(t)
	if err := st.Upsert(ctx, bms); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	imagesDir := t.TempDir()
	// Bookmark 1 gets a cover so cover markup renders and /covers/ has
	// something real to serve. The bytes need not be a valid JPEG: the
	// handler serves the type implied by the filename.
	for _, b := range bms {
		if b.ID != 1 {
			continue
		}
		if err := st.SetCover(ctx, 1, testCoverFile, "image/jpeg"); err != nil {
			t.Fatalf("SetCover: %v", err)
		}
		if err := os.WriteFile(filepath.Join(imagesDir, testCoverFile), []byte("\xff\xd8\xff\xe0 fake jpeg bytes"), 0o600); err != nil {
			t.Fatalf("write cover file: %v", err)
		}
	}

	renderer := newExampleRenderer(t)
	snap, err := renderer.Snapshot(ctx, st, fixedRefresh)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	srv, err := New(st, renderer, imagesDir, testBaseURL, snap, testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	httpSrv := httptest.NewServer(srv.Handler())
	t.Cleanup(httpSrv.Close)

	return &env{
		server:    srv,
		store:     st,
		imagesDir: imagesDir,
		baseURL:   httpSrv.URL,
		client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		following: &http.Client{},
	}
}

// response is a fully-read HTTP response.
type response struct {
	status int
	header http.Header
	body   string
}

func (r response) contains(t *testing.T, want string) {
	t.Helper()
	if !strings.Contains(r.body, want) {
		t.Errorf("response body does not contain %q", want)
	}
}

func (r response) notContains(t *testing.T, unwanted string) {
	t.Helper()
	if strings.Contains(r.body, unwanted) {
		t.Errorf("response body unexpectedly contains %q", unwanted)
	}
}

func (r response) wantStatus(t *testing.T, want int) {
	t.Helper()
	if r.status != want {
		t.Fatalf("status = %d, want %d", r.status, want)
	}
}

func (r response) wantHeader(t *testing.T, name, want string) {
	t.Helper()
	if got := r.header.Get(name); got != want {
		t.Errorf("%s = %q, want %q", name, got, want)
	}
}

func do(t *testing.T, c *http.Client, method, rawURL string, headers map[string]string) response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, rawURL, nil)
	if err != nil {
		t.Fatalf("new request %s %s: %v", method, rawURL, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body of %s %s: %v", method, rawURL, err)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: string(body)}
}

func (e *env) get(t *testing.T, path string) response {
	t.Helper()
	return do(t, e.client, http.MethodGet, e.url(path), nil)
}

// --- list pages ----------------------------------------------------------

func TestListPages(t *testing.T) {
	e := newEnv(t, fixtureBookmarks())

	root := e.get(t, "/")
	root.wantStatus(t, http.StatusOK)
	page1 := e.get(t, "/1")
	page1.wantStatus(t, http.StatusOK)
	page2 := e.get(t, "/2")
	page2.wantStatus(t, http.StatusOK)

	t.Run("root and page 1 are byte-identical", func(t *testing.T) {
		if root.body != page1.body {
			t.Errorf("GET / and GET /1 bodies differ (%d vs %d bytes)", len(root.body), len(page1.body))
		}
		if root.header.Get("ETag") != page1.header.Get("ETag") {
			t.Errorf("GET / ETag %q != GET /1 ETag %q", root.header.Get("ETag"), page1.header.Get("ETag"))
		}
	})

	t.Run("page 2 differs", func(t *testing.T) {
		if page2.body == page1.body {
			t.Error("GET /2 body is identical to GET /1")
		}
	})

	t.Run("root declares /1 as canonical", func(t *testing.T) {
		root.contains(t, `<link rel="canonical" href="`+testBaseURL+`/1">`)
	})

	t.Run("covers render on page 1", func(t *testing.T) {
		page1.contains(t, "Bookmark 01")
		page1.contains(t, `src="/covers/`+testCoverFile+`"`)
	})

	t.Run("three pages exist", func(t *testing.T) {
		e.get(t, "/3").wantStatus(t, http.StatusOK)
	})

	// aria-current="page" must name the page you are actually on, and only
	// that one. The site-title link points at /1 from every page, so it must
	// not claim it.
	t.Run("exactly one aria-current=page, on the current page's link", func(t *testing.T) {
		for _, tc := range []struct {
			page string
			body string
		}{{"1", page1.body}, {"2", page2.body}} {
			// The trailing ">" keeps the example's `a[aria-current="page"]`
			// CSS selector out of the count.
			if n := strings.Count(tc.body, `aria-current="page">`); n != 1 {
				t.Errorf("page %s: %d elements have aria-current=\"page\", want 1", tc.page, n)
			}
			want := `<a href="/` + tc.page + `" aria-current="page">`
			if !strings.Contains(tc.body, want) {
				t.Errorf("page %s: no link marked %s", tc.page, want)
			}
		}
	})
}

func TestNotFoundPaths(t *testing.T) {
	e := newEnv(t, fixtureBookmarks())

	for _, path := range []string{
		"/99",      // out of range
		"/0",       // pages are 1-based
		"/foo",     // non-numeric
		"/01",      // non-canonical numeric form
		"/foo/bar", // multi-segment
	} {
		t.Run(path, func(t *testing.T) {
			r := e.get(t, path)
			r.wantStatus(t, http.StatusNotFound)
			r.wantHeader(t, "Cache-Control", "no-store")
		})
	}
}

func TestTrailingSlashRedirects(t *testing.T) {
	e := newEnv(t, fixtureBookmarks())

	for path, want := range map[string]string{
		"/2/":      "/2",
		"/search/": "/search",
	} {
		t.Run(path, func(t *testing.T) {
			r := e.get(t, path)
			r.wantStatus(t, http.StatusMovedPermanently)
			r.wantHeader(t, "Location", want)
		})
	}
}

func TestListPageCaching(t *testing.T) {
	e := newEnv(t, fixtureBookmarks())

	r := e.get(t, "/1")
	r.wantStatus(t, http.StatusOK)
	r.wantHeader(t, "Cache-Control", "public, max-age=300")

	etag := r.header.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on a prerendered list page")
	}
	if !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) {
		t.Errorf("ETag %q is not a quoted strong validator", etag)
	}

	t.Run("matching If-None-Match returns 304 with no body", func(t *testing.T) {
		c := do(t, e.client, http.MethodGet, e.url("/1"), map[string]string{"If-None-Match": etag})
		c.wantStatus(t, http.StatusNotModified)
		if c.body != "" {
			t.Errorf("304 response has a %d-byte body, want empty", len(c.body))
		}
	})

	t.Run("HEAD returns headers without a body", func(t *testing.T) {
		h := do(t, e.client, http.MethodHead, e.url("/"), nil)
		h.wantStatus(t, http.StatusOK)
		h.wantHeader(t, "Cache-Control", "public, max-age=300")
		if h.header.Get("ETag") != etag {
			t.Errorf("HEAD ETag = %q, want %q", h.header.Get("ETag"), etag)
		}
		if h.body != "" {
			t.Errorf("HEAD response has a %d-byte body, want empty", len(h.body))
		}
	})
}

// --- search --------------------------------------------------------------

func TestSearchPage(t *testing.T) {
	e := newEnv(t, fixtureBookmarks())

	t.Run("no query serves the prerendered empty state", func(t *testing.T) {
		r := e.get(t, "/search")
		r.wantStatus(t, http.StatusOK)
		r.contains(t, "Start typing to search")
		r.contains(t, `<meta name="robots" content="noindex">`)
	})

	t.Run("query renders a full no-JS page", func(t *testing.T) {
		r := e.get(t, "/search?q=sqlite")
		r.wantStatus(t, http.StatusOK)
		r.wantHeader(t, "Cache-Control", "no-store")
		r.contains(t, "<html")
		r.contains(t, "SQLite for Servers")
	})

	t.Run("too-short query returns the empty state, not results", func(t *testing.T) {
		r := e.get(t, "/search?q=x")
		r.wantStatus(t, http.StatusOK)
		r.contains(t, "Start typing to search")
		r.notContains(t, "search-hits")
	})

	// Otherwise every distinct spelling of "no query" would be a separate
	// cacheable URL serving an identical copy of the prerendered page.
	t.Run("blank queries redirect to the canonical /search", func(t *testing.T) {
		for _, path := range []string{"/search?q=", "/search?q=%20%20", "/search?q=%09"} {
			r := e.get(t, path)
			r.wantStatus(t, http.StatusFound)
			r.wantHeader(t, "Location", "/search")
			r.wantHeader(t, "Cache-Control", "no-store")
		}
	})

	t.Run("following a blank-query redirect reaches the empty state", func(t *testing.T) {
		r := do(t, e.following, http.MethodGet, e.url("/search?q=%20%20"), nil)
		r.wantStatus(t, http.StatusOK)
		r.contains(t, "Start typing to search")
		r.notContains(t, "search-hits")
	})
}

func TestSearchResultsSnippet(t *testing.T) {
	e := newEnv(t, fixtureBookmarks())

	t.Run("fragment matches the full page's results region", func(t *testing.T) {
		frag := e.get(t, "/search/results?q=sqlite")
		frag.wantStatus(t, http.StatusOK)
		frag.wantHeader(t, "Cache-Control", "no-store")
		frag.notContains(t, "<html")
		frag.contains(t, "SQLite for Servers")

		full := e.get(t, "/search?q=sqlite")
		full.wantStatus(t, http.StatusOK)
		// The snippet endpoint and the no-JS page render the same template
		// with the same data, so the fragment must appear verbatim inside
		// the page. If this fails the two paths have drifted.
		if !strings.Contains(full.body, frag.body) {
			t.Errorf("snippet body (%d bytes) is not a substring of the /search?q= page (%d bytes)",
				len(frag.body), len(full.body))
		}
	})

	t.Run("empty query returns the empty-state fragment", func(t *testing.T) {
		r := e.get(t, "/search/results?q=")
		r.wantStatus(t, http.StatusOK)
		r.wantHeader(t, "Cache-Control", "no-store")
		r.notContains(t, "<html")
		r.contains(t, "Start typing to search")
	})

	t.Run("no matches names the query", func(t *testing.T) {
		r := e.get(t, "/search/results?q=zzzznomatch")
		r.wantStatus(t, http.StatusOK)
		r.contains(t, "No results for")
		r.contains(t, "zzzznomatch")
	})
}

// --- robots, sitemap, status ---------------------------------------------

func TestRobotsTxt(t *testing.T) {
	e := newEnv(t, fixtureBookmarks())
	r := e.get(t, "/robots.txt")
	r.wantStatus(t, http.StatusOK)
	r.contains(t, "Disallow: /search")
	r.contains(t, testBaseURL+"/sitemap.xml")
}

func TestSitemap(t *testing.T) {
	e := newEnv(t, fixtureBookmarks())

	r := e.get(t, "/sitemap.xml")
	r.wantStatus(t, http.StatusOK)
	if ct := r.header.Get("Content-Type"); !strings.HasPrefix(ct, "application/xml") {
		t.Errorf("Content-Type = %q, want application/xml", ct)
	}
	r.wantHeader(t, "Cache-Control", "public, max-age=300")
	r.contains(t, "<loc>"+testBaseURL+"/1</loc>")
	r.contains(t, "<loc>"+testBaseURL+"/3</loc>")

	etag := r.header.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on the sitemap")
	}
	c := do(t, e.client, http.MethodGet, e.url("/sitemap.xml"), map[string]string{"If-None-Match": etag})
	c.wantStatus(t, http.StatusNotModified)
	if c.body != "" {
		t.Errorf("304 sitemap response has a %d-byte body, want empty", len(c.body))
	}
}

func TestStatus(t *testing.T) {
	e := newEnv(t, fixtureBookmarks())

	decode := func(t *testing.T, r response) (up, lastRefreshOK bool) {
		t.Helper()
		var got struct {
			Up            bool `json:"up"`
			LastRefreshOK bool `json:"last_refresh_ok"`
		}
		if err := json.Unmarshal([]byte(r.body), &got); err != nil {
			t.Fatalf("decode %q: %v", r.body, err)
		}
		return got.Up, got.LastRefreshOK
	}

	r := e.get(t, "/_status")
	r.wantStatus(t, http.StatusOK)
	r.wantHeader(t, "Content-Type", "application/json")
	r.wantHeader(t, "Cache-Control", "no-store")
	if up, ok := decode(t, r); !up || !ok {
		t.Errorf("initial status = {up:%v, last_refresh_ok:%v}, want both true", up, ok)
	}

	e.server.SetLastRefreshOK(false)
	r = e.get(t, "/_status")
	r.wantStatus(t, http.StatusOK)
	if up, ok := decode(t, r); !up || ok {
		t.Errorf("after a failed refresh, status = {up:%v, last_refresh_ok:%v}, want {true, false}", up, ok)
	}
}

// --- covers ---------------------------------------------------------------

func TestCovers(t *testing.T) {
	e := newEnv(t, fixtureBookmarks())

	t.Run("stored cover is served immutably", func(t *testing.T) {
		r := e.get(t, "/covers/"+testCoverFile)
		r.wantStatus(t, http.StatusOK)
		r.wantHeader(t, "Content-Type", "image/jpeg")
		r.wantHeader(t, "X-Content-Type-Options", "nosniff")
		r.wantHeader(t, "Cache-Control", "public, max-age=31536000, immutable")
		if r.body == "" {
			t.Error("cover response body is empty")
		}

		etag := r.header.Get("ETag")
		if etag == "" {
			t.Fatal("no ETag on a cover response")
		}
		c := do(t, e.client, http.MethodGet, e.url("/covers/"+testCoverFile), map[string]string{"If-None-Match": etag})
		c.wantStatus(t, http.StatusNotModified)
		if c.body != "" {
			t.Errorf("304 cover response has a %d-byte body, want empty", len(c.body))
		}
	})

	t.Run("well-formed name with no file on disk 404s", func(t *testing.T) {
		r := e.get(t, "/covers/999-0011aabb.jpg")
		r.wantStatus(t, http.StatusNotFound)
		r.wantHeader(t, "Cache-Control", "no-store")
	})

	t.Run("name outside the generated pattern 404s", func(t *testing.T) {
		r := e.get(t, "/covers/notmatching.txt")
		r.wantStatus(t, http.StatusNotFound)
		r.wantHeader(t, "Cache-Control", "no-store")
	})

	t.Run("percent-encoded traversal 404s", func(t *testing.T) {
		// "%2e%2e%2f" survives ServeMux's path cleaning as a single segment,
		// so the handler itself must reject it: it does not match the
		// generated-filename pattern.
		r := e.get(t, "/covers/%2e%2e%2fx.jpg")
		r.wantStatus(t, http.StatusNotFound)
		r.wantHeader(t, "Cache-Control", "no-store")
	})

	t.Run("literal traversal never reaches a file", func(t *testing.T) {
		// ServeMux canonicalizes "/covers/../secret" to "/secret" with a 301
		// before any handler runs, so follow the redirect to observe the
		// final outcome.
		r := do(t, e.following, http.MethodGet, e.url("/covers/../secret"), nil)
		r.wantStatus(t, http.StatusNotFound)
	})
}

// --- startup and empty-store behavior ------------------------------------

// copyExampleTemplates copies the three required templates out of the example
// directory into a fresh temp directory, so one of them can be corrupted
// without touching the shipped example.
func copyExampleTemplates(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	for _, name := range []string{render.ListTemplate, render.SearchTemplate, render.ResultsTemplate} {
		b, err := os.ReadFile(filepath.Join(exampleTemplateDir, name)) //nolint:gosec // fixed in-repo fixture path
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dst, name), b, 0o600); err != nil { //nolint:gosec // destination is this test's own t.TempDir()
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dst
}

func TestLoadRejectsMalformedTemplate(t *testing.T) {
	dir := copyExampleTemplates(t)
	// An unclosed action: parsing must fail so the app exits non-zero at
	// startup rather than serving template errors.
	broken := "{{define \"bookmark-item\"}}<li>{{.Title</li>\n"
	if err := os.WriteFile(filepath.Join(dir, render.ListTemplate), []byte(broken), 0o600); err != nil {
		t.Fatalf("corrupt template: %v", err)
	}

	_, err := render.Load(dir, render.Config{
		PerPage:    testPerPage,
		BaseURL:    testBaseURL,
		DateFormat: "January 2, 2006",
		Location:   time.UTC,
		Version:    "test",
	})
	if err == nil {
		t.Fatal("render.Load succeeded on a malformed template directory, want an error")
	}
}

func TestExampleTemplatePassesVerify(t *testing.T) {
	if err := newExampleRenderer(t).Verify(); err != nil {
		t.Fatalf("the shipped example template fails Verify: %v", err)
	}
}

// A template that parses but blows up when it renders a bookmark must be
// caught at startup. Otherwise a fresh install starts cleanly (the startup
// prerender only exercises the empty state), shows "No bookmarks yet."
// forever, and reports the real cause only as a failing refresh.
func TestVerifyCatchesTemplateThatOnlyFailsOnRealData(t *testing.T) {
	dir := copyExampleTemplates(t)
	list, err := os.ReadFile(filepath.Join(dir, render.ListTemplate)) //nolint:gosec // this test's own t.TempDir()
	if err != nil {
		t.Fatalf("read list template: %v", err)
	}
	// Valid syntax, nonexistent field, reachable only when a bookmark renders.
	broken := strings.Replace(string(list), "{{.Title}}", "{{.Titel}}", 1)
	if broken == string(list) {
		t.Fatal("did not find {{.Title}} to corrupt in the example list template")
	}
	if err := os.WriteFile(filepath.Join(dir, render.ListTemplate), []byte(broken), 0o600); err != nil { //nolint:gosec // this test's own t.TempDir()
		t.Fatalf("corrupt template: %v", err)
	}

	cfg := render.Config{
		PerPage:    testPerPage,
		BaseURL:    testBaseURL,
		DateFormat: "January 2, 2006",
		Location:   time.UTC,
		Version:    "test",
	}
	r, err := render.Load(dir, cfg)
	if err != nil {
		t.Fatalf("render.Load: %v", err)
	}

	// Precondition: this is exactly the template that slips past parsing and
	// past a prerender of an empty database.
	if _, err := r.Snapshot(context.Background(), newTestStore(t), fixedRefresh); err != nil {
		t.Fatalf("prerender of an empty store failed, so this is not the case under test: %v", err)
	}

	if err := r.Verify(); err == nil {
		t.Fatal("Verify accepted a template that cannot render a bookmark, want an error")
	}
}

func TestEmptyStore(t *testing.T) {
	e := newEnv(t, nil)

	r := e.get(t, "/")
	r.wantStatus(t, http.StatusOK)
	r.contains(t, "No bookmarks yet.")

	p2 := e.get(t, "/2")
	p2.wantStatus(t, http.StatusNotFound)
	p2.wantHeader(t, "Cache-Control", "no-store")
}
