package render

import (
	"fmt"
	"strings"
	"testing"
)

// summarize renders links as "1 … 7 [8] 9 … 42", with the current page in
// brackets and "…" wherever GapBefore is set, so expectations read like the
// control the user sees.
func summarize(links []PageLink) string {
	parts := make([]string, 0, len(links)*2)
	for _, l := range links {
		if l.GapBefore {
			parts = append(parts, "…")
		}
		if l.Current {
			parts = append(parts, fmt.Sprintf("[%d]", l.Num))
			continue
		}
		parts = append(parts, fmt.Sprint(l.Num))
	}
	return strings.Join(parts, " ")
}

func TestPageLinks(t *testing.T) {
	for _, tc := range []struct {
		name             string
		page, totalPages int
		want             string
	}{
		{"single page", 1, 1, "[1]"},
		{"no elision needed", 3, 5, "1 2 [3] 4 5"},
		{"exactly at the elision threshold", 4, 7, "1 2 3 [4] 5 6 7"},
		{"first page of many", 1, 42, "[1] 2 3 … 42"},
		{"middle of many", 20, 42, "1 … 18 19 [20] 21 22 … 42"},
		{"last page of many", 42, 42, "1 … 40 41 [42]"},
		{"near the start", 4, 42, "1 2 3 [4] 5 6 … 42"},
		{"near the end", 39, 42, "1 … 37 38 [39] 40 41 42"},
		{"gap of exactly one page is still a gap", 5, 42, "1 … 3 4 [5] 6 7 … 42"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := summarize(pageLinks(tc.page, tc.totalPages))
			if got != tc.want {
				t.Errorf("pageLinks(%d, %d) =\n  %s\nwant\n  %s", tc.page, tc.totalPages, got, tc.want)
			}
		})
	}
}

// However many pages exist, the control stays a fixed size — that is the
// point of windowing.
func TestPageLinksAreBounded(t *testing.T) {
	const maxLinks = 2*pageWindow + 3 // window either side, current, first, last
	for _, total := range []int{1, 2, 7, 50, 1000, 100000} {
		for _, page := range []int{1, 2, total / 2, total - 1, total} {
			if page < 1 || page > total {
				continue // never rendered; /N 404s beyond the last page
			}
			links := pageLinks(page, total)
			if len(links) > maxLinks {
				t.Errorf("pageLinks(%d, %d) returned %d links, want at most %d",
					page, total, len(links), maxLinks)
			}
			// The current page must always be reachable and marked.
			var current int
			for _, l := range links {
				if l.Current {
					current++
					if l.Num != page {
						t.Errorf("pageLinks(%d, %d) marked page %d current", page, total, l.Num)
					}
				}
			}
			if current != 1 {
				t.Errorf("pageLinks(%d, %d) marked %d links current, want 1", page, total, current)
			}
		}
	}
}
