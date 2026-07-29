// Package covers downloads bookmark cover images to a local directory with
// verification, and knows the naming scheme under which they are served.
package covers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/cdzombak/raindrop-public-browser/internal/store"
)

// MaxBytes caps a cover download, enforced on the stream rather than on
// Content-Length, which may be absent or wrong.
const MaxBytes = 5 << 20 // 5 MB

// DefaultTimeout covers connect, headers, and body for one image.
const DefaultTimeout = 10 * time.Second

// DefaultConcurrency is how many covers download at once.
const DefaultConcurrency = 4

// extByType maps an accepted sniffed content type to its file extension.
// Anything not in this map is rejected.
var extByType = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

// FilenamePattern matches every filename this package can generate; the
// covers handler refuses to serve anything else.
var FilenamePattern = regexp.MustCompile(`^[0-9]+-[0-9a-f]{8}\.(jpg|png|gif|webp)$`)

// ContentTypeForFilename returns the content type implied by a generated
// cover filename, or "" if the name doesn't match the pattern.
func ContentTypeForFilename(name string) string {
	if !FilenamePattern.MatchString(name) {
		return ""
	}
	for typ, ext := range extByType {
		if filepath.Ext(name) == ext {
			return typ
		}
	}
	return ""
}

// urlHash returns the first 8 hex chars of the SHA-256 of the source cover
// URL. Including it in the filename means a changed cover URL produces a new
// filename rather than a stale cached file, which is what makes immutable
// cache headers safe.
func urlHash(coverURL string) string {
	sum := sha256.Sum256([]byte(coverURL))
	return hex.EncodeToString(sum[:])[:8]
}

// Filename returns the storage filename for a bookmark's cover.
func Filename(raindropID int64, coverURL, sniffedType string) string {
	return fmt.Sprintf("%d-%s%s", raindropID, urlHash(coverURL), extByType[sniffedType])
}

// Downloader fetches cover images with bounded concurrency.
type Downloader struct {
	ImagesDir   string
	UserAgent   string
	Logger      *slog.Logger
	HTTPClient  *http.Client  // defaults to http.DefaultClient's transport with Timeout
	Timeout     time.Duration // per image; defaults to DefaultTimeout
	Concurrency int           // defaults to DefaultConcurrency
}

func (d *Downloader) timeout() time.Duration {
	if d.Timeout > 0 {
		return d.Timeout
	}
	return DefaultTimeout
}

func (d *Downloader) concurrency() int {
	if d.Concurrency > 0 {
		return d.Concurrency
	}
	return DefaultConcurrency
}

func (d *Downloader) httpClient() *http.Client {
	if d.HTTPClient != nil {
		return d.HTTPClient
	}
	return http.DefaultClient
}

// DownloadAll downloads every cover the store still needs. Failures are
// logged and recorded per bookmark; they never fail the refresh, so the only
// returned errors are context cancellation or database errors.
func (d *Downloader) DownloadAll(ctx context.Context, st *store.Store) error {
	candidates, err := st.CoversNeeded(ctx)
	if err != nil {
		return fmt.Errorf("list covers needed: %w", err)
	}
	if len(candidates) == 0 {
		return nil
	}
	if err := os.MkdirAll(d.ImagesDir, 0o750); err != nil {
		return fmt.Errorf("create images directory: %w", err)
	}

	sem := make(chan struct{}, d.concurrency())
	var wg sync.WaitGroup
	var mu sync.Mutex
	var dbErr error

	for _, c := range candidates {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(c store.CoverCandidate) {
			defer wg.Done()
			defer func() { <-sem }()

			filename, sniffed, err := d.downloadOne(ctx, c.ID, c.CoverURL)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if ctx.Err() != nil {
					return // shutdown, not a real failure
				}
				d.Logger.Warn("cover download failed",
					"bookmark_id", c.ID, "url", c.CoverURL, "reason", err.Error())
				permanent, derr := st.RecordCoverFailure(ctx, c.ID)
				if derr != nil && dbErr == nil {
					dbErr = derr
				}
				if permanent {
					d.Logger.Warn("cover marked permanently unavailable; will not retry",
						"bookmark_id", c.ID, "url", c.CoverURL)
				}
				return
			}
			if derr := st.SetCover(ctx, c.ID, filename, sniffed); derr != nil && dbErr == nil {
				dbErr = derr
			}
		}(c)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return dbErr
}

// downloadOne fetches one cover, verifies it, and moves it into place.
// It returns the stored filename and sniffed content type.
func (d *Downloader) downloadOne(ctx context.Context, id int64, coverURL string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, coverURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", d.UserAgent)

	resp, err := d.httpClient().Do(req)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	// Read one byte past the cap so exceeding it is detectable on the stream.
	limited := io.LimitReader(resp.Body, MaxBytes+1)

	head := make([]byte, 512)
	n, err := io.ReadFull(limited, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", "", fmt.Errorf("read image head: %w", err)
	}
	head = head[:n]

	// The response Content-Type header is not trusted; the sniffed type is
	// what gets stored and later served.
	sniffed := http.DetectContentType(head)
	if _, ok := extByType[sniffed]; !ok {
		return "", "", fmt.Errorf("unsupported content type %q", sniffed)
	}

	filename := Filename(id, coverURL, sniffed)
	dest := filepath.Join(d.ImagesDir, filename)
	if _, err := os.Stat(dest); err == nil {
		// Already downloaded on a previous refresh; nothing to do.
		return filename, sniffed, nil
	}

	tmp, err := os.CreateTemp(d.ImagesDir, ".cover-*")
	if err != nil {
		return "", "", fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed

	written, err := io.Copy(tmp, io.MultiReader(bytes.NewReader(head), limited))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", "", fmt.Errorf("download body: %w", err)
	}
	if written > MaxBytes {
		return "", "", fmt.Errorf("image exceeds %d byte limit", int64(MaxBytes))
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return "", "", fmt.Errorf("move into place: %w", err)
	}
	return filename, sniffed, nil
}
