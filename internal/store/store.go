// Package store persists bookmarks in SQLite and provides the queries the
// web server and refresher need, including FTS5-backed search.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/cdzombak/raindrop-public-browser/internal/norm"
)

// DBFileName is the SQLite database filename inside the configured directory.
const DBFileName = "bookmarks.db"

// Bookmark is a stored public bookmark.
type Bookmark struct {
	ID       int64
	Title    string
	Excerpt  string
	URL      string
	Domain   string
	CoverURL string
	// CoverFile and CoverType are empty until a cover has been downloaded
	// and verified; the template omits the image entirely in that case.
	CoverFile string
	CoverType string
	Created   time.Time
}

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if necessary) the database in dir and applies the
// schema. The directory must exist or be creatable.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}
	path := filepath.Join(dir, DBFileName)
	// modernc.org/sqlite: _pragma values are applied per-connection.
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}
	// A single writer avoids SQLITE_BUSY between the refresher and readers;
	// WAL lets reads proceed concurrently on the same connection pool.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS bookmarks (
	id                   INTEGER PRIMARY KEY,
	title                TEXT NOT NULL,
	excerpt              TEXT NOT NULL,
	url                  TEXT NOT NULL,
	domain               TEXT NOT NULL,
	cover_url            TEXT NOT NULL DEFAULT '',
	cover_file           TEXT,
	cover_type           TEXT,
	cover_attempts       INTEGER NOT NULL DEFAULT 0,
	cover_permanent_fail INTEGER NOT NULL DEFAULT 0,
	created              TEXT NOT NULL,
	search_text          TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_bookmarks_created ON bookmarks (created DESC, id DESC);

CREATE VIRTUAL TABLE IF NOT EXISTS bookmarks_fts USING fts5(
	search_text,
	content='bookmarks',
	content_rowid='id',
	tokenize="unicode61 remove_diacritics 2",
	prefix='2 3'
);

CREATE TRIGGER IF NOT EXISTS bookmarks_ai AFTER INSERT ON bookmarks BEGIN
	INSERT INTO bookmarks_fts(rowid, search_text) VALUES (new.id, new.search_text);
END;

CREATE TRIGGER IF NOT EXISTS bookmarks_ad AFTER DELETE ON bookmarks BEGIN
	INSERT INTO bookmarks_fts(bookmarks_fts, rowid, search_text) VALUES ('delete', old.id, old.search_text);
END;

CREATE TRIGGER IF NOT EXISTS bookmarks_au AFTER UPDATE ON bookmarks BEGIN
	INSERT INTO bookmarks_fts(bookmarks_fts, rowid, search_text) VALUES ('delete', old.id, old.search_text);
	INSERT INTO bookmarks_fts(rowid, search_text) VALUES (new.id, new.search_text);
END;
`

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	return nil
}

const timeFormat = time.RFC3339 // stored in UTC; lexicographic order == chronological

// Upsert inserts or updates a batch of bookmarks in one transaction.
// Titles and excerpts are assumed immutable once public, but are written
// anyway; a changed cover URL resets the cover download state so the new
// cover is fetched.
func (s *Store) Upsert(ctx context.Context, bms []Bookmark) error {
	if len(bms) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO bookmarks (id, title, excerpt, url, domain, cover_url, created, search_text)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			title = excluded.title,
			excerpt = excluded.excerpt,
			url = excluded.url,
			domain = excluded.domain,
			search_text = excluded.search_text,
			created = excluded.created,
			cover_file = CASE WHEN bookmarks.cover_url = excluded.cover_url THEN bookmarks.cover_file ELSE NULL END,
			cover_type = CASE WHEN bookmarks.cover_url = excluded.cover_url THEN bookmarks.cover_type ELSE NULL END,
			cover_attempts = CASE WHEN bookmarks.cover_url = excluded.cover_url THEN bookmarks.cover_attempts ELSE 0 END,
			cover_permanent_fail = CASE WHEN bookmarks.cover_url = excluded.cover_url THEN bookmarks.cover_permanent_fail ELSE 0 END,
			cover_url = excluded.cover_url
	`)
	if err != nil {
		return fmt.Errorf("prepare upsert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, b := range bms {
		domain := norm.Domain(b.URL)
		searchText := norm.SearchText(b.Title, b.URL)
		created := b.Created.UTC().Format(timeFormat)
		if _, err := stmt.ExecContext(ctx, b.ID, b.Title, b.Excerpt, b.URL, domain, b.CoverURL, created, searchText); err != nil {
			return fmt.Errorf("upsert bookmark %d: %w", b.ID, err)
		}
	}
	return tx.Commit()
}

const bookmarkCols = `id, title, excerpt, url, domain, cover_url,
	COALESCE(cover_file, ''), COALESCE(cover_type, ''), created`

func scanBookmarks(rows *sql.Rows) ([]Bookmark, error) {
	var out []Bookmark
	for rows.Next() {
		var b Bookmark
		var created string
		if err := rows.Scan(&b.ID, &b.Title, &b.Excerpt, &b.URL, &b.Domain, &b.CoverURL, &b.CoverFile, &b.CoverType, &created); err != nil {
			return nil, err
		}
		t, err := time.Parse(timeFormat, created)
		if err != nil {
			return nil, fmt.Errorf("bookmark %d: bad created timestamp %q: %w", b.ID, created, err)
		}
		b.Created = t
		out = append(out, b)
	}
	return out, rows.Err()
}

// Count returns the total number of bookmarks.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bookmarks`).Scan(&n)
	return n, err
}

// Page returns one page of bookmarks, reverse-chronological, 1-based.
func (s *Store) Page(ctx context.Context, page, perPage int) ([]Bookmark, error) {
	if page < 1 {
		return nil, errors.New("page must be >= 1")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+bookmarkCols+` FROM bookmarks
		ORDER BY created DESC, id DESC
		LIMIT ? OFFSET ?`, perPage, (page-1)*perPage)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanBookmarks(rows)
}

// SearchLimit is the maximum number of search results returned. Search
// fetches one extra row so callers can detect that more matches exist.
const SearchLimit = 100

// Search runs a normalized prefix search. It returns up to SearchLimit
// results plus a flag indicating more matches exist. An empty or
// unmatchable query returns (nil, false, nil).
func (s *Store) Search(ctx context.Context, query string) ([]Bookmark, bool, error) {
	expr := norm.MatchExpr(query)
	if expr == "" {
		return nil, false, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT b.id, b.title, b.excerpt, b.url, b.domain, b.cover_url,
			COALESCE(b.cover_file, ''), COALESCE(b.cover_type, ''), b.created
		FROM bookmarks_fts f
		JOIN bookmarks b ON b.id = f.rowid
		WHERE bookmarks_fts MATCH ?
		ORDER BY b.created DESC, b.id DESC
		LIMIT ?`, expr, SearchLimit+1)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	bms, err := scanBookmarks(rows)
	if err != nil {
		return nil, false, err
	}
	if len(bms) > SearchLimit {
		return bms[:SearchLimit], true, nil
	}
	return bms, false, nil
}

// CoverCandidate describes a bookmark whose cover still needs downloading.
type CoverCandidate struct {
	ID       int64
	CoverURL string
}

// CoversNeeded returns bookmarks that have a cover URL, no stored cover
// file, and are not marked permanently coverless.
func (s *Store) CoversNeeded(ctx context.Context) ([]CoverCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, cover_url FROM bookmarks
		WHERE cover_url != '' AND cover_file IS NULL AND cover_permanent_fail = 0
		ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []CoverCandidate
	for rows.Next() {
		var c CoverCandidate
		if err := rows.Scan(&c.ID, &c.CoverURL); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetCover records a successfully downloaded cover for a bookmark.
func (s *Store) SetCover(ctx context.Context, id int64, filename, contentType string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE bookmarks SET cover_file = ?, cover_type = ?, cover_attempts = 0
		WHERE id = ?`, filename, contentType, id)
	return err
}

// MaxCoverAttempts is the number of consecutive failed downloads after
// which a bookmark is marked permanently coverless.
const MaxCoverAttempts = 3

// RecordCoverFailure increments the failure counter for a bookmark's cover
// and marks it permanently coverless after MaxCoverAttempts consecutive
// failures. It reports whether the bookmark is now permanently coverless.
func (s *Store) RecordCoverFailure(ctx context.Context, id int64) (bool, error) {
	var permanent bool
	err := s.db.QueryRowContext(ctx, `
		UPDATE bookmarks SET
			cover_attempts = cover_attempts + 1,
			cover_permanent_fail = CASE WHEN cover_attempts + 1 >= ? THEN 1 ELSE 0 END
		WHERE id = ?
		RETURNING cover_permanent_fail`, MaxCoverAttempts, id).Scan(&permanent)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return permanent, err
}
