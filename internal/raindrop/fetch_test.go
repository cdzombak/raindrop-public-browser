package raindrop

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// makeItems builds n canned Item fixtures starting at id offset.
func makeItems(n, offset int) []Item {
	out := make([]Item, n)
	for i := 0; i < n; i++ {
		out[i] = Item{
			ID:      int64(offset + i),
			Title:   fmt.Sprintf("Item %d", offset+i),
			Excerpt: "excerpt",
			Link:    fmt.Sprintf("https://example.com/%d", offset+i),
			Created: "2026-01-01T00:00:00Z",
			Tags:    []string{"_public"},
		}
	}
	return out
}

func TestFetchTaggedPaginatesAcrossPages(t *testing.T) {
	pages := [][]Item{
		makeItems(50, 0),
		makeItems(50, 50),
		makeItems(7, 100),
	}

	var gotPages []string
	var gotSearch []string
	var gotAuth []string
	var gotUA []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, err := strconv.Atoi(r.URL.Query().Get("page"))
		if err != nil || page < 0 || page >= len(pages) {
			t.Errorf("unexpected page param %q", r.URL.Query().Get("page"))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		gotPages = append(gotPages, r.URL.Query().Get("page"))
		gotSearch = append(gotSearch, r.URL.Query().Get("search"))
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		gotUA = append(gotUA, r.Header.Get("User-Agent"))

		if got := r.URL.Query().Get("perpage"); got != "50" {
			t.Errorf("perpage = %q, want 50", got)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(multiResponse{Result: true, Items: pages[page]})
	}))
	defer srv.Close()

	f := &Fetcher{
		BaseURL:   srv.URL,
		PageDelay: time.Millisecond,
		UserAgent: "test-agent/1.0",
	}
	items, err := f.FetchTagged(t.Context(), "test-token", "_public")
	if err != nil {
		t.Fatalf("FetchTagged: %v", err)
	}

	wantTotal := 50 + 50 + 7
	if len(items) != wantTotal {
		t.Fatalf("len(items) = %d, want %d", len(items), wantTotal)
	}
	for i, it := range items {
		if it.ID != int64(i) {
			t.Errorf("items[%d].ID = %d, want %d", i, it.ID, i)
		}
	}

	if want := []string{"0", "1", "2"}; !equalStrSlices(gotPages, want) {
		t.Errorf("pages requested = %v, want %v", gotPages, want)
	}

	for i, s := range gotSearch {
		if !strings.Contains(s, `"key":"tag"`) || !strings.Contains(s, `"val":"_public"`) {
			t.Errorf("search param on request %d = %q, want it to contain the tag filter", i, s)
		}
	}

	for i, a := range gotAuth {
		if a != "Bearer test-token" {
			t.Errorf("Authorization header on request %d = %q, want %q", i, a, "Bearer test-token")
		}
	}

	for i, ua := range gotUA {
		if ua != "test-agent/1.0" {
			t.Errorf("User-Agent header on request %d = %q, want %q", i, ua, "test-agent/1.0")
		}
	}
}

func equalStrSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFetchTaggedNon200IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()

	f := &Fetcher{BaseURL: srv.URL, PageDelay: time.Millisecond}
	_, err := f.FetchTagged(t.Context(), "token", "_public")
	if err == nil {
		t.Fatal("FetchTagged returned nil error for a 500 response, want error")
	}
}

func TestFetchTaggedResultFalseIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(multiResponse{Result: false})
	}))
	defer srv.Close()

	f := &Fetcher{BaseURL: srv.URL, PageDelay: time.Millisecond}
	_, err := f.FetchTagged(t.Context(), "token", "_public")
	if err == nil {
		t.Fatal("FetchTagged returned nil error for result=false, want error")
	}
}
