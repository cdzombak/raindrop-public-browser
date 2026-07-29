package store

import (
	"context"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// fixtureBookmarks returns a small, fixed-timestamp fixture set matching the
// spec's worked search examples.
func fixtureBookmarks() []Bookmark {
	t := func(y int, m time.Month, d int) time.Time {
		return time.Date(y, m, d, 12, 0, 0, 0, time.UTC)
	}
	return []Bookmark{
		{
			ID:      1,
			Title:   "SQLite for Servers",
			Excerpt: "an excerpt",
			URL:     "https://kerkour.com/sqlite-for-servers",
			Created: t(2026, 1, 1),
		},
		{
			ID:      2,
			Title:   "Goroutines Explained",
			Excerpt: "an excerpt",
			URL:     "https://example.com/goroutines",
			Created: t(2026, 1, 2),
		},
		{
			ID:      3,
			Title:   "Hacker News Item",
			Excerpt: "an excerpt",
			URL:     "https://www.news.ycombinator.com/item?id=1",
			Created: t(2026, 1, 3),
		},
	}
}

func TestSearchOrderIndependentPrefixMatch(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.Upsert(ctx, fixtureBookmarks()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	bms, truncated, err := s.Search(ctx, "sqlite serv")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if truncated {
		t.Fatalf("unexpected truncation")
	}
	if len(bms) != 1 || bms[0].ID != 1 {
		t.Fatalf("Search(%q) = %+v, want just bookmark 1", "sqlite serv", bms)
	}

	bms, _, err = s.Search(ctx, "serv sqlite")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(bms) != 1 || bms[0].ID != 1 {
		t.Fatalf("Search(%q) = %+v, want just bookmark 1", "serv sqlite", bms)
	}
}

func TestSearchPrefixNotSubstring(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.Upsert(ctx, fixtureBookmarks()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	bms, _, err := s.Search(ctx, "routines")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(bms) != 0 {
		t.Fatalf("Search(%q) = %+v, want no matches (prefix, not substring)", "routines", bms)
	}
}

func TestSearchByDomain(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.Upsert(ctx, fixtureBookmarks()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	bms, _, err := s.Search(ctx, "news.ycombinator.com")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(bms) != 1 || bms[0].ID != 3 {
		t.Fatalf("Search(%q) = %+v, want just bookmark 3", "news.ycombinator.com", bms)
	}
}

func TestSearchFTSKeywordLiteral(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	bms := []Bookmark{
		{
			ID:      10,
			Title:   "This and That",
			Excerpt: "",
			URL:     "https://example.com/and-that",
			Created: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			ID:      11,
			Title:   "Near Miss",
			Excerpt: "",
			URL:     "https://example.com/near-miss",
			Created: time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC),
		},
	}
	if err := s.Upsert(ctx, bms); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, _, err := s.Search(ctx, "and")
	if err != nil {
		t.Fatalf("Search(%q): %v (should not be a syntax error)", "and", err)
	}
	if len(got) != 1 || got[0].ID != 10 {
		t.Fatalf("Search(%q) = %+v, want just bookmark 10", "and", got)
	}

	got, _, err = s.Search(ctx, "near")
	if err != nil {
		t.Fatalf("Search(%q): %v (should not be a syntax error)", "near", err)
	}
	if len(got) != 1 || got[0].ID != 11 {
		t.Fatalf("Search(%q) = %+v, want just bookmark 11", "near", got)
	}
}

func TestSearchOrderingReverseChronological(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	// Same created timestamp for two bookmarks to also exercise the id
	// tiebreaker.
	same := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	bms := []Bookmark{
		{ID: 1, Title: "match one", URL: "https://example.com/1", Created: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{ID: 2, Title: "match two", URL: "https://example.com/2", Created: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)},
		{ID: 3, Title: "match three", URL: "https://example.com/3", Created: same},
		{ID: 4, Title: "match four", URL: "https://example.com/4", Created: same},
	}
	if err := s.Upsert(ctx, bms); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, _, err := s.Search(ctx, "match")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	wantOrder := []int64{4, 3, 2, 1}
	if len(got) != len(wantOrder) {
		t.Fatalf("Search returned %d results, want %d", len(got), len(wantOrder))
	}
	for i, id := range wantOrder {
		if got[i].ID != id {
			t.Errorf("result[%d].ID = %d, want %d (order: %+v)", i, got[i].ID, id, got)
		}
	}
}

func TestSearchResultCap(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	makeBookmarks := func(n int) []Bookmark {
		out := make([]Bookmark, n)
		for i := 0; i < n; i++ {
			out[i] = Bookmark{
				ID:      int64(i + 1),
				Title:   "capped result",
				URL:     "https://example.com/x",
				Created: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute),
			}
		}
		return out
	}

	// Exactly 100 matches: not truncated.
	s100 := openTestStore(t)
	if err := s100.Upsert(ctx, makeBookmarks(100)); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, truncated, err := s100.Search(ctx, "capped")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 100 {
		t.Fatalf("len(got) = %d, want 100", len(got))
	}
	if truncated {
		t.Fatalf("truncated = true, want false for exactly 100 matches")
	}

	// 101 matches: truncated, capped at 100.
	if err := s.Upsert(ctx, makeBookmarks(101)); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, truncated, err = s.Search(ctx, "capped")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 100 {
		t.Fatalf("len(got) = %d, want 100", len(got))
	}
	if !truncated {
		t.Fatalf("truncated = false, want true for 101 matches")
	}
}

func TestSearchEmptyOrUnmatchableQuery(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.Upsert(ctx, fixtureBookmarks()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	for _, q := range []string{"", "   ", "!!! ---"} {
		bms, truncated, err := s.Search(ctx, q)
		if err != nil {
			t.Fatalf("Search(%q): %v", q, err)
		}
		if bms != nil {
			t.Errorf("Search(%q) bookmarks = %+v, want nil", q, bms)
		}
		if truncated {
			t.Errorf("Search(%q) truncated = true, want false", q)
		}
	}
}

func TestUpsertIdempotent(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	bms := fixtureBookmarks()

	if err := s.Upsert(ctx, bms); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	count1, err := s.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count1 != len(bms) {
		t.Fatalf("Count = %d, want %d", count1, len(bms))
	}

	if err := s.Upsert(ctx, bms); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}
	count2, err := s.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count2 != count1 {
		t.Fatalf("Count after re-Upsert = %d, want %d (no duplicates)", count2, count1)
	}
}

func TestFTSTracksBookmarksAcrossInsertAndUpdate(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	bm := Bookmark{
		ID:      1,
		Title:   "Original Title",
		URL:     "https://example.com/page",
		Created: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := s.Upsert(ctx, []Bookmark{bm}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, _, err := s.Search(ctx, "original")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Search(%q) after insert = %+v, want 1 match", "original", got)
	}

	// Update the title; the FTS index must follow via the update triggers.
	bm.Title = "Updated Title"
	if err := s.Upsert(ctx, []Bookmark{bm}); err != nil {
		t.Fatalf("Upsert (update): %v", err)
	}

	got, _, err = s.Search(ctx, "updated")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Search(%q) after update = %+v, want 1 match", "updated", got)
	}

	got, _, err = s.Search(ctx, "original")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Search(%q) after update = %+v, want no matches (stale FTS entry)", "original", got)
	}
}

func TestUpsertChangedCoverURLResetsCoverState(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	bm := Bookmark{
		ID:       1,
		Title:    "Has Cover",
		URL:      "https://example.com/page",
		CoverURL: "https://covers.example.com/one.jpg",
		Created:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := s.Upsert(ctx, []Bookmark{bm}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := s.SetCover(ctx, 1, "1-abcd1234.jpg", "image/jpeg"); err != nil {
		t.Fatalf("SetCover: %v", err)
	}

	needed, err := s.CoversNeeded(ctx)
	if err != nil {
		t.Fatalf("CoversNeeded: %v", err)
	}
	if len(needed) != 0 {
		t.Fatalf("CoversNeeded after SetCover = %+v, want empty", needed)
	}

	// Changing the cover URL should reset cover_file/attempts so the new
	// cover gets fetched.
	bm.CoverURL = "https://covers.example.com/two.jpg"
	if err := s.Upsert(ctx, []Bookmark{bm}); err != nil {
		t.Fatalf("Upsert (changed cover url): %v", err)
	}

	needed, err = s.CoversNeeded(ctx)
	if err != nil {
		t.Fatalf("CoversNeeded: %v", err)
	}
	if len(needed) != 1 || needed[0].ID != 1 || needed[0].CoverURL != bm.CoverURL {
		t.Fatalf("CoversNeeded after cover url change = %+v, want [{1 %s}]", needed, bm.CoverURL)
	}
}

func TestRecordCoverFailurePermanentAfterThree(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	bm := Bookmark{
		ID:       1,
		Title:    "Cover Fails",
		URL:      "https://example.com/page",
		CoverURL: "https://covers.example.com/dead.jpg",
		Created:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := s.Upsert(ctx, []Bookmark{bm}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	for i := 1; i <= MaxCoverAttempts; i++ {
		permanent, err := s.RecordCoverFailure(ctx, 1)
		if err != nil {
			t.Fatalf("RecordCoverFailure attempt %d: %v", i, err)
		}
		wantPermanent := i >= MaxCoverAttempts
		if permanent != wantPermanent {
			t.Fatalf("RecordCoverFailure attempt %d permanent = %v, want %v", i, permanent, wantPermanent)
		}
	}

	needed, err := s.CoversNeeded(ctx)
	if err != nil {
		t.Fatalf("CoversNeeded: %v", err)
	}
	if len(needed) != 0 {
		t.Fatalf("CoversNeeded after permanent failure = %+v, want empty", needed)
	}
}

func TestCoversNeededExcludesEmptyCoverURL(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	bm := Bookmark{
		ID:      1,
		Title:   "No Cover",
		URL:     "https://example.com/page",
		Created: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := s.Upsert(ctx, []Bookmark{bm}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	needed, err := s.CoversNeeded(ctx)
	if err != nil {
		t.Fatalf("CoversNeeded: %v", err)
	}
	if len(needed) != 0 {
		t.Fatalf("CoversNeeded for bookmark with empty cover_url = %+v, want empty", needed)
	}
}
