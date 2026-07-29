package render_test

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cdzombak/raindrop-public-browser/internal/render"
	"github.com/cdzombak/raindrop-public-browser/internal/store"
)

// update rewrites the golden files instead of comparing against them:
//
//	go test ./internal/render/ -run Golden -update
//
// The golden files are change detectors, not correctness proofs. Their job is
// to make template and markup changes visible in review.
var update = flag.Bool("update", false, "update golden files")

// exampleTemplateDir is the shipped example template set; rendering the
// goldens through it keeps the example honest.
const exampleTemplateDir = "../../example-template"

const goldenBaseURL = "https://bookmarks.example.com"

// goldenRefreshed is the snapshot timestamp. It affects only the sitemap, but
// is fixed anyway so every rendered artifact is reproducible.
var goldenRefreshed = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

// goldenBookmarks is a deliberately small, fully deterministic fixture set:
// one bookmark whose title and excerpt need HTML escaping, one with a cover,
// one without. Two of the three match the query "sqlite" (by title and by
// title-plus-domain), the third does not.
func goldenBookmarks() []store.Bookmark {
	return []store.Bookmark{
		{
			ID:      1,
			Title:   "Ampersands & <Angles>",
			Excerpt: `Escaping "quotes", <tags> & entities.`,
			URL:     "https://example.com/escaping",
			Created: time.Date(2026, 1, 3, 15, 4, 5, 0, time.UTC),
		},
		{
			ID:       2,
			Title:    "SQLite for Servers",
			Excerpt:  "Notes on running SQLite in production.",
			URL:      "https://news.ycombinator.com/item?id=1",
			CoverURL: "https://example.com/cover.jpg",
			Created:  time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC),
		},
		{
			ID:      3,
			Title:   "Understanding SQLite WAL Mode",
			Excerpt: "",
			URL:     "https://sqlite.org/wal.html",
			Created: time.Date(2026, 1, 1, 15, 4, 5, 0, time.UTC),
		},
	}
}

// goldenCoverFile follows covers.FilenamePattern for bookmark 2.
const goldenCoverFile = "2-0011aabb.jpg"

// newGoldenFixture returns a renderer over the example templates and a store
// seeded with goldenBookmarks.
func newGoldenFixture(t *testing.T) (*render.Renderer, *store.Store) {
	t.Helper()
	ctx := context.Background()

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := st.Upsert(ctx, goldenBookmarks()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := st.SetCover(ctx, 2, goldenCoverFile, "image/jpeg"); err != nil {
		t.Fatalf("SetCover: %v", err)
	}

	r, err := render.Load(exampleTemplateDir, render.Config{
		PerPage:    10,
		BaseURL:    goldenBaseURL,
		DateFormat: "January 2, 2006",
		Location:   time.UTC,
		Version:    "golden",
	})
	if err != nil {
		t.Fatalf("render.Load(%s): %v", exampleTemplateDir, err)
	}
	return r, st
}

// compareGolden compares got against testdata/<name>, or rewrites it when
// -update is passed.
func compareGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)

	if *update {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatalf("create testdata dir: %v", err)
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatalf("update %s: %v", path, err)
		}
		t.Logf("updated %s (%d bytes)", path, len(got))
		return
	}

	want, err := os.ReadFile(path) //nolint:gosec // fixed testdata path
	if err != nil {
		t.Fatalf("read %s (run `go test ./internal/render/ -run Golden -update` to create it): %v", path, err)
	}
	if bytes.Equal(got, want) {
		return
	}

	offset := firstDiff(got, want)
	t.Errorf("output does not match %s\n  got  %d bytes, want %d bytes\n  first difference at byte %d\n  got  ...%s...\n  want ...%s...\n"+
		"  if this change is intended, re-run with -update and review the diff",
		path, len(got), len(want), offset, excerpt(got, offset), excerpt(want, offset))
}

// firstDiff returns the offset of the first differing byte, or the length of
// the shorter slice when one is a prefix of the other.
func firstDiff(a, b []byte) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// excerpt returns a short window of b around offset, for the failure message.
func excerpt(b []byte, offset int) string {
	const window = 60
	start := max(offset-window/2, 0)
	end := min(start+window, len(b))
	return string(b[start:end])
}

func TestGoldenListPage(t *testing.T) {
	r, st := newGoldenFixture(t)

	snap, err := r.Snapshot(context.Background(), st, goldenRefreshed)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.TotalPages != 1 {
		t.Fatalf("TotalPages = %d, want 1", snap.TotalPages)
	}
	page, ok := snap.Lists[1]
	if !ok {
		t.Fatal("snapshot has no page 1")
	}
	compareGolden(t, "list-page-1.golden.html", page.Body)
}

func TestGoldenSearchResults(t *testing.T) {
	r, st := newGoldenFixture(t)

	const query = "sqlite"
	bms, truncated, err := st.Search(context.Background(), query)
	if err != nil {
		t.Fatalf("Search(%q): %v", query, err)
	}
	if len(bms) != 2 {
		t.Fatalf("Search(%q) returned %d bookmarks, want 2", query, len(bms))
	}
	if truncated {
		t.Fatalf("Search(%q) reported truncation on a 3-bookmark fixture", query)
	}

	body, err := r.ResultsFragment(r.Results(query, bms, truncated))
	if err != nil {
		t.Fatalf("ResultsFragment: %v", err)
	}
	compareGolden(t, "search-results.golden.html", body)
}
