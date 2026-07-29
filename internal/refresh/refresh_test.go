package refresh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cdzombak/raindrop-public-browser/internal/covers"
	"github.com/cdzombak/raindrop-public-browser/internal/raindrop"
	"github.com/cdzombak/raindrop-public-browser/internal/render"
	"github.com/cdzombak/raindrop-public-browser/internal/store"
)

// --- test fixtures & helpers -------------------------------------------------

const listTmpl = `<!doctype html>
<html><body>
{{if .Empty}}<p>No bookmarks yet.</p>{{end}}
<p>page={{.Page}} totalPages={{.TotalPages}}</p>
<ul>
{{range .Bookmarks}}<li><a href="{{.URL}}">{{.Title}}</a>{{if .CoverPath}}<img src="{{.CoverPath}}" alt="">{{end}}</li>
{{end}}
</ul>
</body></html>
`

const searchTmpl = `<!doctype html>
<html><body>
{{template "results.html.tmpl" .Results}}
</body></html>
`

const resultsTmpl = `<div id="results">
{{if not .Queried}}<p>Start typing to search&hellip;</p>
{{else if eq .Count 0}}<p>No results for {{.Query}}</p>
{{else}}
<ul>
{{range .Bookmarks}}<li><a href="{{.URL}}">{{.Title}}</a></li>{{end}}
</ul>
{{if .Truncated}}<p>More than 100 matches &mdash; refine your search.</p>{{end}}
{{end}}
<p role="status">{{.StatusText}}</p>
</div>
`

// writeTestTemplates writes a minimal template set (not example-template) to
// a fresh temp directory and returns its path.
func writeTestTemplates(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		render.ListTemplate:    listTmpl,
		render.SearchTemplate:  searchTmpl,
		render.ResultsTemplate: resultsTmpl,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write template %s: %v", name, err)
		}
	}
	return dir
}

func newTestRenderer(t *testing.T) *render.Renderer {
	t.Helper()
	dir := writeTestTemplates(t)
	r, err := render.Load(dir, render.Config{
		PerPage:    10,
		BaseURL:    "https://example.com",
		DateFormat: "January 2, 2006",
		Location:   time.UTC,
		Version:    "test",
	})
	if err != nil {
		t.Fatalf("render.Load: %v", err)
	}
	return r
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// apiResponse mirrors the Raindrop API's multi-raindrop response shape.
type apiResponse struct {
	Result bool            `json:"result"`
	Items  []raindrop.Item `json:"items"`
}

// newAPIServer serves items from a paginated Raindrop-shaped endpoint.
// failOnRequestN, if > 0, makes the Nth request (1-based, across the whole
// server's lifetime) respond 500 instead of serving data.
func newAPIServer(t *testing.T, items []raindrop.Item, failOnRequestN int32) (*httptest.Server, *int32) {
	t.Helper()
	var requestCount int32
	const perPage = 50
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&requestCount, 1)
		if failOnRequestN > 0 && n == failOnRequestN {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("injected failure"))
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		start := page * perPage
		if start > len(items) {
			start = len(items)
		}
		end := start + perPage
		if end > len(items) {
			end = len(items)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apiResponse{Result: true, Items: items[start:end]})
	}))
	t.Cleanup(srv.Close)
	return srv, &requestCount
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func makeItem(id int64, title, cover string, created time.Time) raindrop.Item {
	return raindrop.Item{
		ID:      id,
		Title:   title,
		Excerpt: "excerpt for " + title,
		Link:    fmt.Sprintf("https://example.com/item/%d", id),
		Cover:   cover,
		Created: created.UTC().Format(time.RFC3339),
		Tags:    []string{"_public"},
	}
}

func newDownloader(t *testing.T) *covers.Downloader {
	t.Helper()
	return &covers.Downloader{
		ImagesDir: t.TempDir(),
		UserAgent: "test-agent/1.0",
		Logger:    testLogger(),
	}
}

// --- tests --------------------------------------------------------------

func TestRefreshImportsAllPagesAndFiresOnSnapshot(t *testing.T) {
	const total = 51 // 50 + 1, exercises two API pages
	items := make([]raindrop.Item, total)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < total; i++ {
		items[i] = makeItem(int64(i+1), fmt.Sprintf("Bookmark %d", i+1), "", base.Add(time.Duration(i)*time.Minute))
	}
	apiSrv, reqCount := newAPIServer(t, items, 0)

	st := newTestStore(t)
	renderer := newTestRenderer(t) // 51 items / 10 per page => 6 pages
	dl := newDownloader(t)

	var snapshots []*render.Snapshot
	r := &Refresher{
		Store:      st,
		Fetcher:    &raindrop.Fetcher{BaseURL: apiSrv.URL, PageDelay: time.Millisecond},
		Downloader: dl,
		Renderer:   renderer,
		Tokens:     StaticToken("test-token"),
		Tag:        "_public",
		Logger:     testLogger(),
		Clock:      func() time.Time { return base },
		OnSnapshot: func(s *render.Snapshot) { snapshots = append(snapshots, s) },
	}

	if err := r.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if *reqCount < 2 {
		t.Fatalf("expected at least 2 API requests (pagination), got %d", *reqCount)
	}

	count, err := st.Count(context.Background())
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != total {
		t.Fatalf("store Count = %d, want %d", count, total)
	}

	if len(snapshots) != 1 {
		t.Fatalf("OnSnapshot called %d times, want 1", len(snapshots))
	}
	if snapshots[0].TotalPages != 6 {
		t.Errorf("snapshot.TotalPages = %d, want 6", snapshots[0].TotalPages)
	}
	if len(snapshots[0].Lists) != 6 {
		t.Errorf("len(snapshot.Lists) = %d, want 6", len(snapshots[0].Lists))
	}
}

func TestRefreshIdempotentStableETags(t *testing.T) {
	items := []raindrop.Item{
		makeItem(1, "First Bookmark", "", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		makeItem(2, "Second Bookmark", "", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)),
		makeItem(3, "Third Bookmark", "", time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)),
	}
	apiSrv, _ := newAPIServer(t, items, 0)

	st := newTestStore(t)
	renderer := newTestRenderer(t)
	dl := newDownloader(t)
	fixedClock := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

	var snapshots []*render.Snapshot
	r := &Refresher{
		Store:      st,
		Fetcher:    &raindrop.Fetcher{BaseURL: apiSrv.URL, PageDelay: time.Millisecond},
		Downloader: dl,
		Renderer:   renderer,
		Tokens:     StaticToken("test-token"),
		Tag:        "_public",
		Logger:     testLogger(),
		Clock:      func() time.Time { return fixedClock },
		OnSnapshot: func(s *render.Snapshot) { snapshots = append(snapshots, s) },
	}

	if err := r.Refresh(context.Background()); err != nil {
		t.Fatalf("first Refresh: %v", err)
	}
	if err := r.Refresh(context.Background()); err != nil {
		t.Fatalf("second Refresh: %v", err)
	}

	count, err := st.Count(context.Background())
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != len(items) {
		t.Fatalf("Count = %d, want %d", count, len(items))
	}

	if len(snapshots) != 2 {
		t.Fatalf("OnSnapshot called %d times, want 2", len(snapshots))
	}
	s1, s2 := snapshots[0], snapshots[1]
	if s1.TotalPages != s2.TotalPages {
		t.Fatalf("TotalPages changed between refreshes: %d vs %d", s1.TotalPages, s2.TotalPages)
	}
	for p := 1; p <= s1.TotalPages; p++ {
		if s1.Lists[p].ETag != s2.Lists[p].ETag {
			t.Errorf("page %d ETag changed between identical refreshes: %q vs %q", p, s1.Lists[p].ETag, s2.Lists[p].ETag)
		}
	}
	if s1.Search.ETag != s2.Search.ETag {
		t.Errorf("search page ETag changed between identical refreshes: %q vs %q", s1.Search.ETag, s2.Search.ETag)
	}
}

func TestRefreshFailurePartwayKeepsPreviousSnapshot(t *testing.T) {
	items := []raindrop.Item{
		makeItem(1, "First Bookmark", "", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
	}
	// The 2nd HTTP request to the API (i.e. the request made by the second
	// Refresh() call, since this fixture fits in one page) fails.
	apiSrv, _ := newAPIServer(t, items, 2)

	st := newTestStore(t)
	renderer := newTestRenderer(t)
	dl := newDownloader(t)

	var snapshots []*render.Snapshot
	r := &Refresher{
		Store:      st,
		Fetcher:    &raindrop.Fetcher{BaseURL: apiSrv.URL, PageDelay: time.Millisecond},
		Downloader: dl,
		Renderer:   renderer,
		Tokens:     StaticToken("test-token"),
		Tag:        "_public",
		Logger:     testLogger(),
		Clock:      func() time.Time { return time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC) },
		OnSnapshot: func(s *render.Snapshot) { snapshots = append(snapshots, s) },
	}

	if err := r.Refresh(context.Background()); err != nil {
		t.Fatalf("first Refresh: %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("after first Refresh, OnSnapshot called %d times, want 1", len(snapshots))
	}
	firstSnapshot := snapshots[0]

	err := r.Refresh(context.Background())
	if err == nil {
		t.Fatal("second Refresh returned nil error, want an error from the injected API failure")
	}

	if len(snapshots) != 1 {
		t.Fatalf("after failed second Refresh, OnSnapshot called %d times, want still 1 (previous snapshot must keep serving)", len(snapshots))
	}
	if snapshots[0] != firstSnapshot {
		t.Fatalf("the snapshot changed after a failed refresh; previous snapshot must keep serving")
	}
}

func TestRefreshCovers(t *testing.T) {
	pngBytes := tinyPNG(t)

	var validRequests, deadRequests int32
	coverSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/valid.png":
			atomic.AddInt32(&validRequests, 1)
			_, _ = w.Write(pngBytes)
		case "/dead.png":
			atomic.AddInt32(&deadRequests, 1)
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(coverSrv.Close)

	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	items := []raindrop.Item{
		makeItem(100, "Valid Cover Bookmark", coverSrv.URL+"/valid.png", created),
		makeItem(200, "Dead Cover Bookmark", coverSrv.URL+"/dead.png", created.Add(time.Minute)),
		makeItem(300, "No Cover Bookmark", "", created.Add(2*time.Minute)),
	}
	apiSrv, _ := newAPIServer(t, items, 0)

	st := newTestStore(t)
	renderer := newTestRenderer(t)
	imagesDir := t.TempDir()
	dl := &covers.Downloader{
		ImagesDir: imagesDir,
		UserAgent: "test-agent/1.0",
		Logger:    testLogger(),
	}

	var snapshots []*render.Snapshot
	r := &Refresher{
		Store:      st,
		Fetcher:    &raindrop.Fetcher{BaseURL: apiSrv.URL, PageDelay: time.Millisecond},
		Downloader: dl,
		Renderer:   renderer,
		Tokens:     StaticToken("test-token"),
		Tag:        "_public",
		Logger:     testLogger(),
		Clock:      func() time.Time { return time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC) },
		OnSnapshot: func(s *render.Snapshot) { snapshots = append(snapshots, s) },
	}

	// Run the refresh 3 times: enough for the dead cover to hit
	// MaxCoverAttempts and be marked permanently coverless. Each refresh
	// must succeed regardless of the individual cover failure.
	for i := 1; i <= store.MaxCoverAttempts; i++ {
		if err := r.Refresh(context.Background()); err != nil {
			t.Fatalf("Refresh #%d: %v", i, err)
		}
	}
	if len(snapshots) != store.MaxCoverAttempts {
		t.Fatalf("OnSnapshot called %d times, want %d", len(snapshots), store.MaxCoverAttempts)
	}

	bms, err := st.Page(context.Background(), 1, 10)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	byID := make(map[int64]store.Bookmark, len(bms))
	for _, b := range bms {
		byID[b.ID] = b
	}

	t.Run("valid cover downloaded", func(t *testing.T) {
		b, ok := byID[100]
		if !ok {
			t.Fatal("bookmark 100 not found")
		}
		wantFilename := covers.Filename(100, coverSrv.URL+"/valid.png", "image/png")
		if b.CoverFile != wantFilename {
			t.Errorf("CoverFile = %q, want %q", b.CoverFile, wantFilename)
		}
		if b.CoverType != "image/png" {
			t.Errorf("CoverType = %q, want image/png", b.CoverType)
		}
		if _, err := os.Stat(filepath.Join(imagesDir, wantFilename)); err != nil {
			t.Errorf("expected cover file on disk: %v", err)
		}
		// Only downloaded once across 3 refreshes (idempotent: file already
		// exists on disk after the first).
		if got := atomic.LoadInt32(&validRequests); got == 0 {
			t.Errorf("valid cover URL was never requested")
		}
	})

	t.Run("dead cover permanently coverless after 3 failures", func(t *testing.T) {
		b, ok := byID[200]
		if !ok {
			t.Fatal("bookmark 200 not found")
		}
		if b.CoverFile != "" {
			t.Errorf("CoverFile = %q, want empty (coverless)", b.CoverFile)
		}

		needed, err := st.CoversNeeded(context.Background())
		if err != nil {
			t.Fatalf("CoversNeeded: %v", err)
		}
		for _, c := range needed {
			if c.ID == 200 {
				t.Errorf("bookmark 200 still in CoversNeeded after %d failed attempts, want permanently excluded", store.MaxCoverAttempts)
			}
		}
		if got := atomic.LoadInt32(&deadRequests); got != int32(store.MaxCoverAttempts) {
			t.Errorf("dead cover URL requested %d times, want %d (one retry per refresh until permanent)", got, store.MaxCoverAttempts)
		}
	})

	t.Run("no cover URL skipped silently", func(t *testing.T) {
		b, ok := byID[300]
		if !ok {
			t.Fatal("bookmark 300 not found")
		}
		if b.CoverFile != "" {
			t.Errorf("CoverFile = %q, want empty", b.CoverFile)
		}
		if b.CoverURL != "" {
			t.Errorf("CoverURL = %q, want empty", b.CoverURL)
		}
	})
}
