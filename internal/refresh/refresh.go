// Package refresh orchestrates a bookmarks refresh: obtain a valid access
// token, fetch tagged raindrops, upsert them, download covers, and prerender
// a new snapshot.
package refresh

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/cdzombak/raindrop-public-browser/internal/covers"
	"github.com/cdzombak/raindrop-public-browser/internal/raindrop"
	"github.com/cdzombak/raindrop-public-browser/internal/render"
	"github.com/cdzombak/raindrop-public-browser/internal/store"
)

// TokenSource yields a valid Raindrop access token, refreshing (and
// persisting) it as needed.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// StaticToken is a TokenSource returning a fixed token; used by tests.
type StaticToken string

// Token implements TokenSource.
func (t StaticToken) Token(context.Context) (string, error) { return string(t), nil }

// Refresher runs bookmark refreshes.
type Refresher struct {
	Store      *store.Store
	Fetcher    *raindrop.Fetcher
	Downloader *covers.Downloader
	Renderer   *render.Renderer
	Tokens     TokenSource
	Tag        string
	Logger     *slog.Logger
	// Clock is injected for tests; defaults to time.Now.
	Clock func() time.Time
	// OnSnapshot receives each successfully prerendered snapshot.
	OnSnapshot func(*render.Snapshot)
}

func (r *Refresher) now() time.Time {
	if r.Clock != nil {
		return r.Clock()
	}
	return time.Now()
}

// Refresh performs one full refresh. On error the previous snapshot keeps
// serving; a partial refresh is harmless because the next one runs again.
func (r *Refresher) Refresh(ctx context.Context) error {
	start := r.now()

	token, err := r.Tokens.Token(ctx)
	if err != nil {
		return fmt.Errorf("obtain access token: %w", err)
	}

	items, err := r.Fetcher.FetchTagged(ctx, token, r.Tag)
	if err != nil {
		return fmt.Errorf("fetch tagged raindrops: %w", err)
	}

	bms := make([]store.Bookmark, 0, len(items))
	untagged := 0
	for _, it := range items {
		// Publishing a bookmark is driven entirely by this tag, so do not
		// take the API's search results on faith: confirm each raindrop
		// really carries it. Comparison is case-insensitive because Raindrop
		// treats tags that way.
		if !hasTag(it.Tags, r.Tag) {
			untagged++
			r.Logger.Warn("skipping raindrop returned by the tag search but not carrying the tag",
				"raindrop_id", it.ID, "tag", r.Tag, "tags", it.Tags)
			continue
		}
		created, err := time.Parse(time.RFC3339, it.Created)
		if err != nil {
			r.Logger.Warn("skipping raindrop with unparseable created timestamp",
				"raindrop_id", it.ID, "created", it.Created)
			continue
		}
		bms = append(bms, store.Bookmark{
			ID:       it.ID,
			Title:    it.Title,
			Excerpt:  it.Excerpt,
			URL:      it.Link,
			CoverURL: it.Cover,
			Created:  created,
		})
	}
	// A tag search that returns raindrops, none of which carry the tag, means
	// the request or the response shape is wrong — not that the collection
	// was emptied. Fail the refresh so last_refresh_ok reports it, rather
	// than quietly never importing anything again.
	if untagged > 0 && untagged == len(items) {
		return fmt.Errorf("all %d raindrops returned by the tag search lack the %q tag", untagged, r.Tag)
	}

	if err := r.Store.Upsert(ctx, bms); err != nil {
		return fmt.Errorf("store bookmarks: %w", err)
	}

	// Covers are downloaded after bookmark rows are committed and before
	// pages are prerendered. Individual cover failures never fail the
	// refresh; only context cancellation or database errors surface here.
	if err := r.Downloader.DownloadAll(ctx, r.Store); err != nil {
		return fmt.Errorf("download covers: %w", err)
	}

	snap, err := r.Renderer.Snapshot(ctx, r.Store, r.now())
	if err != nil {
		return fmt.Errorf("prerender snapshot: %w", err)
	}
	if r.OnSnapshot != nil {
		r.OnSnapshot(snap)
	}

	r.Logger.Info("bookmarks refresh complete",
		"fetched", len(items), "stored", len(bms), "untagged", untagged,
		"pages", snap.TotalPages,
		"duration", r.now().Sub(start).Round(time.Millisecond).String())
	return nil
}

// hasTag reports whether tags contains tag, case-insensitively.
func hasTag(tags []string, tag string) bool {
	return slices.ContainsFunc(tags, func(t string) bool { return strings.EqualFold(t, tag) })
}

// Run performs an immediate refresh, then refreshes every interval until ctx
// is cancelled. Refresh outcomes are reported via report (used to drive
// /_status's last_refresh_ok).
func (r *Refresher) Run(ctx context.Context, interval time.Duration, report func(ok bool)) {
	refresh := func() {
		err := r.Refresh(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return // shutdown, not a refresh failure
			}
			r.Logger.Error("bookmarks refresh failed", "error", err)
		}
		report(err == nil)
	}

	refresh()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}
