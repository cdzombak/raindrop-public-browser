// Package raindrop fetches tagged raindrops from the Raindrop.io API.
//
// The cdzombak/raindrop-io-api-client library is used for the OAuth token
// dance, but its raindrop-fetching helpers support neither tag filter +
// pagination together nor the raindrop `_id` field, and hardcode the API base
// URL (which would make tests hit the network). So the paginated fetch is
// implemented here directly.
package raindrop

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// DefaultBaseURL is the production Raindrop API base URL.
const DefaultBaseURL = "https://api.raindrop.io"

// perPage is the Raindrop API's maximum page size.
const perPage = 50

// Item is one raindrop as returned by GET /rest/v1/raindrops/0.
type Item struct {
	ID      int64    `json:"_id"`
	Title   string   `json:"title"`
	Excerpt string   `json:"excerpt"`
	Link    string   `json:"link"`
	Cover   string   `json:"cover"`
	Created string   `json:"created"` // RFC3339
	Tags    []string `json:"tags"`
}

type multiResponse struct {
	Result bool   `json:"result"`
	Items  []Item `json:"items"`
}

// Fetcher retrieves all raindrops carrying a tag, across however many pages
// the API returns, pausing between page requests to avoid rate limiting.
type Fetcher struct {
	// BaseURL defaults to DefaultBaseURL; tests point it at an httptest server.
	BaseURL string
	// HTTPClient defaults to a client with a 30s timeout.
	HTTPClient *http.Client
	// PageDelay is the pause between page requests; defaults to 500ms.
	// Tests set it near zero.
	PageDelay time.Duration
	UserAgent string
}

func (f *Fetcher) baseURL() string {
	if f.BaseURL != "" {
		return f.BaseURL
	}
	return DefaultBaseURL
}

func (f *Fetcher) httpClient() *http.Client {
	if f.HTTPClient != nil {
		return f.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (f *Fetcher) pageDelay() time.Duration {
	if f.PageDelay > 0 {
		return f.PageDelay
	}
	return 500 * time.Millisecond
}

// FetchTagged returns every raindrop tagged tag, in API order, fetching
// pages sequentially with a pause between requests.
func (f *Fetcher) FetchTagged(ctx context.Context, accessToken, tag string) ([]Item, error) {
	search, err := json.Marshal([]map[string]string{{"key": "tag", "val": tag}})
	if err != nil {
		return nil, err
	}

	var all []Item
	for page := 0; ; page++ {
		if page > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(f.pageDelay()):
			}
		}

		items, err := f.fetchPage(ctx, accessToken, string(search), page)
		if err != nil {
			return nil, fmt.Errorf("fetch raindrops page %d: %w", page, err)
		}
		all = append(all, items...)
		if len(items) < perPage {
			return all, nil
		}
	}
}

func (f *Fetcher) fetchPage(ctx context.Context, accessToken, search string, page int) ([]Item, error) {
	q := url.Values{}
	q.Set("search", search)
	q.Set("perpage", fmt.Sprint(perPage))
	q.Set("page", fmt.Sprint(page))
	// Collection 0 is Raindrop's "all raindrops" pseudo-collection.
	u := f.baseURL() + "/rest/v1/raindrops/0?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if f.UserAgent != "" {
		req.Header.Set("User-Agent", f.UserAgent)
	}

	resp, err := f.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var mr multiResponse
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if !mr.Result {
		return nil, fmt.Errorf("api returned result=false")
	}
	return mr.Items, nil
}
