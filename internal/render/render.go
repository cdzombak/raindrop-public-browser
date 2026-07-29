// Package render loads the operator-provided templates, prerenders the
// bookmark list pages, search page, and sitemap into an in-memory snapshot,
// and renders search results for both the snippet endpoint and the no-JS
// search page.
//
// Template contract (see TEMPLATES.md):
//   - list.html.tmpl    — a bookmark list page; data is ListData
//   - search.html.tmpl  — the full search page; data is SearchData
//   - results.html.tmpl — the search-results region; data is ResultsData.
//     It is included by search.html.tmpl and also rendered alone for the
//     JS snippet endpoint, so the two paths cannot drift.
package render

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"time"

	"github.com/cdzombak/raindrop-public-browser/internal/store"
)

// Template file names required in the template directory.
const (
	ListTemplate    = "list.html.tmpl"
	SearchTemplate  = "search.html.tmpl"
	ResultsTemplate = "results.html.tmpl"
)

// Bookmark is the view model for one bookmark.
type Bookmark struct {
	Title   string
	URL     string
	Excerpt string
	Domain  string
	// CoverPath is the site-relative cover URL ("/covers/123-abcd1234.jpg"),
	// or "" when the bookmark has no stored cover; templates must omit the
	// image entirely in that case.
	CoverPath string
	// Date is the display-formatted creation date.
	Date string
}

// PageLink is one entry in the pagination control.
type PageLink struct {
	Num     int
	URL     string
	Current bool
	// GapBefore is true when the previous entry's page number is not
	// Num-1, i.e. pages were elided between them. Templates should render
	// some separator ("…") so 1, 7, 8, 9, 42 does not read as consecutive.
	GapBefore bool
}

// pageWindow is how many pages are linked either side of the current one, in
// addition to the always-present first and last.
const pageWindow = 2

// ListData is the data passed to list.html.tmpl.
type ListData struct {
	Bookmarks []Bookmark
	// Empty is true when no bookmarks have been imported at all.
	Empty      bool
	Page       int
	TotalPages int
	// PrevURL/NextURL are "" when there is no previous/next page.
	PrevURL string
	NextURL string
	Pages   []PageLink
	// CanonicalURL is the absolute canonical URL of this page.
	CanonicalURL string
	BaseURL      string
	Version      string
}

// ResultsData is the data passed to results.html.tmpl.
type ResultsData struct {
	// Query is the user's query as typed (trimmed, and capped); autoescaping
	// makes it safe to echo. It is populated in every state that had a query
	// at all, including TooShort, so the search box can echo it back.
	Query string
	// Queried is false for the initial/empty/too-short states, where the
	// template shows its "Start typing to search…" prompt.
	Queried bool
	// TooShort is true when a query was submitted but normalized to fewer
	// than the minimum number of characters, so no search ran. It is a
	// distinct state from the initial prompt: the user typed something, and
	// telling them to start typing would be wrong.
	TooShort  bool
	Bookmarks []Bookmark
	Count     int
	// Truncated means more than the result cap matched and the list was cut.
	Truncated bool
	// StatusText is the outcome announcement for the visually-hidden
	// role="status" region: "12 results for goroutines", "No results for x".
	// Empty only in the initial state, which announces nothing.
	StatusText string
}

// SearchData is the data passed to search.html.tmpl.
type SearchData struct {
	Results ResultsData
	BaseURL string
	Version string
}

// Page is one prerendered response body.
type Page struct {
	Body        []byte
	ETag        string // strong ETag, hash of Body
	ContentType string
}

// Snapshot is everything served from memory: list pages, the search page,
// and the sitemap, plus the refresh timestamp used for Last-Modified.
type Snapshot struct {
	// Lists is indexed by page number, 1-based.
	Lists      map[int]*Page
	TotalPages int
	Search     *Page
	Sitemap    *Page
	// Refreshed is the time of the refresh that produced this snapshot.
	Refreshed time.Time
}

// Config is what the renderer needs besides templates.
type Config struct {
	PerPage    int
	BaseURL    string // absolute, no trailing slash
	DateFormat string
	Location   *time.Location
	Version    string
}

// Renderer renders pages from loaded templates.
type Renderer struct {
	tpl *template.Template
	cfg Config
}

// Load parses the three required templates from dir. Any missing or
// malformed template is an error; the app must refuse to start rather than
// serve errors.
func Load(dir string, cfg Config) (*Renderer, error) {
	tpl := template.New("root")
	for _, name := range []string{ListTemplate, SearchTemplate, ResultsTemplate} {
		b, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // dir is the operator-configured template directory
		if err != nil {
			return nil, fmt.Errorf("read template %s: %w", name, err)
		}
		if _, err := tpl.New(name).Parse(string(b)); err != nil {
			return nil, fmt.Errorf("parse template %s: %w", name, err)
		}
	}
	return &Renderer{tpl: tpl, cfg: cfg}, nil
}

// Verify executes every template against synthetic data covering each branch
// the real pages take, discarding the output. Parsing a template only proves
// its syntax; a bad field reference or a call to an undefined template fails
// at execution time, and the branches that render a bookmark are not reached
// at all when the database is still empty. Without this, a typo in the
// bookmark markup starts cleanly, serves "no bookmarks yet" forever, and
// surfaces only as a failing refresh in the log.
func (r *Renderer) Verify() error {
	sample := []store.Bookmark{
		{
			ID: 1, Title: "Sample Bookmark", Excerpt: "A sample excerpt.",
			URL: "https://example.com/sample", Domain: "example.com",
			CoverFile: "1-00000000.jpg", CoverType: "image/jpeg",
			Created: time.Unix(0, 0).UTC(),
		},
		// No title, excerpt or cover: the templates take their fallback paths.
		{ID: 2, URL: "https://example.org/bare", Domain: "example.org", Created: time.Unix(0, 0).UTC()},
	}

	// Both list-page shapes: a populated middle page (previous and next links
	// present) and the empty state.
	if _, err := r.renderList(2, 3, len(sample), sample); err != nil {
		return err
	}
	if _, err := r.renderList(1, 1, 0, nil); err != nil {
		return err
	}

	// Every results state, standalone and wrapped in the search page.
	states := []ResultsData{
		EmptyResults(),
		TooShortResults("x"),
		r.Results("nothing matches this", nil, false),
		r.Results("sample", sample, false),
		r.Results("sample", sample, true),
	}
	for _, d := range states {
		if _, err := r.ResultsFragment(d); err != nil {
			return err
		}
		if _, err := r.SearchPage(d); err != nil {
			return err
		}
	}
	return nil
}

// bookmarkView converts a stored bookmark to its view model.
func (r *Renderer) bookmarkView(b store.Bookmark) Bookmark {
	v := Bookmark{
		Title:   b.Title,
		URL:     b.URL,
		Excerpt: b.Excerpt,
		Domain:  b.Domain,
		Date:    b.Created.In(r.cfg.Location).Format(r.cfg.DateFormat),
	}
	if b.CoverFile != "" {
		v.CoverPath = "/covers/" + b.CoverFile
	}
	return v
}

// BookmarkViews converts stored bookmarks to view models.
func (r *Renderer) BookmarkViews(bms []store.Bookmark) []Bookmark {
	out := make([]Bookmark, len(bms))
	for i, b := range bms {
		out[i] = r.bookmarkView(b)
	}
	return out
}

func newPage(body []byte, contentType string) *Page {
	sum := sha256.Sum256(body)
	return &Page{
		Body:        body,
		ETag:        `"` + hex.EncodeToString(sum[:])[:20] + `"`,
		ContentType: contentType,
	}
}

// Snapshot prerenders everything from the store's current contents.
// refreshed is the timestamp of the refresh producing this snapshot.
func (r *Renderer) Snapshot(ctx context.Context, st *store.Store, refreshed time.Time) (*Snapshot, error) {
	count, err := st.Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("count bookmarks: %w", err)
	}
	// Page 1 always exists, rendering the empty state when there are no
	// bookmarks at all.
	totalPages := max((count+r.cfg.PerPage-1)/r.cfg.PerPage, 1)

	lists := make(map[int]*Page, totalPages)
	for p := 1; p <= totalPages; p++ {
		bms, err := st.Page(ctx, p, r.cfg.PerPage)
		if err != nil {
			return nil, fmt.Errorf("load page %d: %w", p, err)
		}
		body, err := r.renderList(p, totalPages, count, bms)
		if err != nil {
			return nil, err
		}
		lists[p] = newPage(body, "text/html; charset=utf-8")
	}

	searchBody, err := r.SearchPage(EmptyResults())
	if err != nil {
		return nil, err
	}

	sitemap, err := r.renderSitemap(totalPages, count, refreshed)
	if err != nil {
		return nil, err
	}

	return &Snapshot{
		Lists:      lists,
		TotalPages: totalPages,
		Search:     newPage(searchBody, "text/html; charset=utf-8"),
		Sitemap:    newPage(sitemap, "application/xml; charset=utf-8"),
		Refreshed:  refreshed,
	}, nil
}

// pageLinks builds the pagination entries for one page: the first page, a
// window of pageWindow either side of the current one, and the last page.
// Linking every page instead would put one anchor per page on every page —
// quadratic in the number of pages, and unreadable well before it is slow.
func pageLinks(page, totalPages int) []PageLink {
	var links []PageLink
	prev := 0
	for p := 1; p <= totalPages; p++ {
		if p != 1 && p != totalPages && (p < page-pageWindow || p > page+pageWindow) {
			continue
		}
		links = append(links, PageLink{
			Num:       p,
			URL:       fmt.Sprintf("/%d", p),
			Current:   p == page,
			GapBefore: prev != 0 && p != prev+1,
		})
		prev = p
	}
	return links
}

func (r *Renderer) renderList(page, totalPages, count int, bms []store.Bookmark) ([]byte, error) {
	d := ListData{
		Bookmarks:    r.BookmarkViews(bms),
		Empty:        count == 0,
		Page:         page,
		TotalPages:   totalPages,
		CanonicalURL: fmt.Sprintf("%s/%d", r.cfg.BaseURL, page),
		BaseURL:      r.cfg.BaseURL,
		Version:      r.cfg.Version,
	}
	if page > 1 {
		d.PrevURL = fmt.Sprintf("/%d", page-1)
	}
	if page < totalPages {
		d.NextURL = fmt.Sprintf("/%d", page+1)
	}
	d.Pages = pageLinks(page, totalPages)
	var buf bytes.Buffer
	if err := r.tpl.ExecuteTemplate(&buf, ListTemplate, d); err != nil {
		return nil, fmt.Errorf("render list page %d: %w", page, err)
	}
	return buf.Bytes(), nil
}

// MinQueryLen is the minimum normalized query length (of the whole query, not
// of any one token) below which no search runs.
const MinQueryLen = 2

// EmptyResults is the results state before any query at all: the search
// page's initial prompt.
func EmptyResults() ResultsData {
	return ResultsData{}
}

// TooShortResults is the state for a query that normalized to fewer than
// MinQueryLen characters. It keeps Query so the search box still echoes what
// the user typed — dropping it would silently empty the field on the no-JS
// round trip.
func TooShortResults(query string) ResultsData {
	return ResultsData{
		Query:      query,
		TooShort:   true,
		StatusText: fmt.Sprintf("Enter at least %d characters to search", MinQueryLen),
	}
}

// Results builds the ResultsData for an executed query.
func (r *Renderer) Results(query string, bms []store.Bookmark, truncated bool) ResultsData {
	d := ResultsData{
		Query:     query,
		Queried:   true,
		Bookmarks: r.BookmarkViews(bms),
		Count:     len(bms),
		Truncated: truncated,
	}
	switch {
	case d.Count == 0:
		d.StatusText = fmt.Sprintf("No results for %s", query)
	case d.Count == 1:
		d.StatusText = fmt.Sprintf("1 result for %s", query)
	case truncated:
		d.StatusText = fmt.Sprintf("More than %d results for %s", d.Count, query)
	default:
		d.StatusText = fmt.Sprintf("%d results for %s", d.Count, query)
	}
	return d
}

// ResultsFragment renders just the results region (the JS snippet response).
func (r *Renderer) ResultsFragment(d ResultsData) ([]byte, error) {
	var buf bytes.Buffer
	if err := r.tpl.ExecuteTemplate(&buf, ResultsTemplate, d); err != nil {
		return nil, fmt.Errorf("render results fragment: %w", err)
	}
	return buf.Bytes(), nil
}

// SearchPage renders the full search page around the given results state
// (the no-JS path, and the prerendered empty search page).
func (r *Renderer) SearchPage(d ResultsData) ([]byte, error) {
	var buf bytes.Buffer
	sd := SearchData{Results: d, BaseURL: r.cfg.BaseURL, Version: r.cfg.Version}
	if err := r.tpl.ExecuteTemplate(&buf, SearchTemplate, sd); err != nil {
		return nil, fmt.Errorf("render search page: %w", err)
	}
	return buf.Bytes(), nil
}

// sitemap XML types.
type sitemapURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod"`
}

type urlSet struct {
	XMLName xml.Name     `xml:"urlset"`
	Xmlns   string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}

func (r *Renderer) renderSitemap(totalPages, count int, refreshed time.Time) ([]byte, error) {
	lastMod := refreshed.UTC().Format("2006-01-02")
	us := urlSet{Xmlns: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	if count > 0 {
		for p := 1; p <= totalPages; p++ {
			us.URLs = append(us.URLs, sitemapURL{
				Loc:     fmt.Sprintf("%s/%d", r.cfg.BaseURL, p),
				LastMod: lastMod,
			})
		}
	} else {
		us.URLs = append(us.URLs, sitemapURL{Loc: r.cfg.BaseURL + "/1", LastMod: lastMod})
	}
	body, err := xml.MarshalIndent(us, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal sitemap: %w", err)
	}
	return append([]byte(xml.Header), append(body, '\n')...), nil
}
