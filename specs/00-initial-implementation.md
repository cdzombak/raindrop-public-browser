# Raindrop Public Bookmarks Browser Webapp Spec
## Project Overview
This application fetches all my Raindrop.io bookmarks tagged `_public` . It presents a paginated browser for those bookmarks, along with a search page that allows the user to search the bookmarks collection.

## Data Source
https://developer.raindrop.io/v1/raindrops/multiple is used to fetch Raindrops with the “_public” tag. The results are persisted in a SQLite database.

To simplify import/sync, the following assumptions are made:
- once a bookmark is made public, it remains public forever
- once a bookmark is made public, its title and excerpt will not change

Refreshes happen every N minutes, where N is defined by an environment variable, defaulting to 15 minutes. There is a 500ms pause between requests, to avoid rate limiting.

(A refresh also happens immediately after app startup — the startup process goes something like prerender pages to memory, start serving, and then kick off a refresh.)

## Functional Requirements
### Bookmarks List Pages
The most-used pages of the app are the bookmarks list pages. These show the public bookmarks, ordered reverse-chronologically by creation timestamp. Each bookmark’s “cover” (image), title, and excerpt are shown.

Bookmarks list pages are prerendered in memory at startup and after every bookmarks refresh from the Raindrop API, and they’re served from memory on user requests.

Outbound links: rel="noopener noreferrer”; **not** `nofollow`.

### Cover Images
Covers are downloaded during each refresh, after bookmark rows are committed and
before pages are prerendered, with bounded concurrency (4 at a time). A bookmark
whose Raindrop record has no cover URL is skipped silently — that is not a failure.

#### Download
- Per-image timeout of 10 seconds covering connect, headers, and body.
- Requests send a `User-Agent` identifying this application and its version.
- The body is read through an `io.LimitReader` capped at 5 MB. The cap is enforced
  on the stream, not on `Content-Length`, which may be absent or wrong. Exceeding
  the cap aborts the download and counts as a failure.
- The response `Content-Type` header is not trusted. The first 512 bytes are
  sniffed with `http.DetectContentType`; the result must be `image/jpeg`,
  `image/png`, `image/gif`, or `image/webp`. Anything else is a failure. The
  sniffed type — not the header — is what gets stored and later served.
- Downloads are written to a temp file in the images directory and `rename`d into
  place, so a crash or truncated transfer can never leave a partial file being
  served.

#### Storage and naming
Filename is `{raindropID}-{first 8 hex chars of SHA-256 of the source cover URL}`
plus the extension implied by the sniffed content type. Including the URL hash
means a bookmark whose cover URL changes produces a new filename rather than a
stale cached file, which is what makes immutable cache headers safe.

The bookmark row stores the filename and the sniffed content type, or NULL if no
cover is stored. If the target file already exists on disk, the download is
skipped — refreshes are idempotent and cost no bandwidth for existing covers.

The images directory grows without bound. There is no garbage collection; given
the "public bookmarks are permanent" assumption, orphaned files should not occur
in normal operation.

#### Failure handling
A failed download logs at warn level with the bookmark ID, source URL, and reason.
The bookmark is rendered coverless — the template omits the image entirely rather
than substituting a placeholder — and the download is retried on the next refresh.

After 3 consecutive failed attempts the bookmark is marked permanently coverless
and no longer retried, so a dead cover URL does not generate a request and a log
line every 15 minutes forever. The attempt counter resets if the cover URL changes.
Cover failures never fail the overall refresh or affect `last_refresh_ok`.

#### Serving
Covers are served at `/covers/{filename}`. The handler serves only files matching
the generated name pattern from the configured images directory; anything else
404s. Path traversal is prevented by resolving against a restricted root rather
than by string inspection of the request path.

Response headers:

- `Content-Type`: the stored sniffed type
- `X-Content-Type-Options: nosniff` — these are bytes fetched from arbitrary
  third-party hosts, so browsers must not be allowed to reinterpret them
- `Cache-Control: public, max-age=31536000, immutable` — safe because filenames
  are content-addressed by source URL
- `ETag` and `Last-Modified` from file size and mtime, with conditional requests
  and `HEAD` handled correctly

### Search
A separate page presents a search field. As the user types, bookmark titles and
domains are searched, and results appear below the search field (with some delay
& debounce to reduce server load). Results are sorted reverse-chronologically by
creation timestamp, same as on the list pages.

The search page is prerendered in memory at startup and served from memory.

So that search results can use the same templates/engine as the rest of the site,
search results are rendered to HTML snippets server side and returned to the
client, which updates the search-results area of the page with the new HTML. The
endpoint for the HTML snippet is left as a decision for the implementing agent.
Its cache headers include `no-store`.

Without JavaScript, the search form degrades to a normal HTML form, which submits
to `/search?q=foo` and retrieves a rendered HTML page with results.

`robots.txt` disallows `/search`.

#### Limits and edge cases
- **Minimum query length:** 2 characters after normalization, applied to the whole
  query rather than per token — while typing `sqlite s`, the trailing 1-character
  token is a legitimate prefix filter and must be honored. Below the threshold the
  client does not fire a request; the server, if hit directly, returns the empty
  state with HTTP 200.
- **Empty or whitespace-only query:** returns the empty state ("Start typing to
  search…") with HTTP 200. It does not return all bookmarks and does not redirect.
- **Query that normalizes to nothing** (e.g. all punctuation): same as empty.
- **No matches:** a distinct no-results region naming the query.
- **Result cap:** 100, no pagination. Fetch 101 rows; if 101 return, render the
  first 100 and append "More than 100 matches — refine your search." This avoids a
  second `COUNT(*)` at the cost of not showing an exact total.
- **Ranking:** none, as above.

### URLs
Search : `/search`
Bookmarks: `/` or `/1` for page 1; `/2` and so on for each page.
Cover images: `/covers/XXX.jpg`
Also: `/robots.txt`, `/sitemap.xml`, `/_status`

- Note that `/search`, `/robots.txt`, `/sitemap.xml`, `/_status`, and `/covers/...` take precedence over `/N`.
- The bare `/` should indicate that the canonical version of page 1 is `/1`, avoiding search engine indexing penalties for duplicate content.
- Trailing slashes are stripped.
- Nonexistent pages `/0`, `/foo`, or `/N` where there is no page N of bookmarks return 404.

### Raindrop Authentication
The application can be run via a terminal in “login” mode, as distinct from “serve” mode.

Login mode spawns a local web server to handle Raindrop OAuth authentication, and walks the user through the OAuth process. 

Environment variables are used to set the OAuth client ID and key, and to provide the path of an “OAuth state file” which is a JSON file where the application stores OAuth credentials. If the file does not exist, it is created with 0600 permissions. 

The local web server used to receive OAuth credentials listens on loopback, unless a special environment variable is set, in which case it listens on 0.0.0.0. The Dockerfile sets this special env variable. This allows the user to perform authentication via the Docker container using port binding. 

See https://github.com/cdzombak/raindrop-public-rss-feed for an example of how OAuth should be handled. 

If `serve` starts with no OAuth state file, it refuses to run. If the refresh token is present but turns out to be expired, it logs an error, but `serve` continues to serve content. `serve` mode writes refreshed tokens back to the state file after every bookmarks refresh.

### Status Monitoring
An additional endpoint, `/_status`, is provided that just returns a tiny JSON blob `{"up": true, “last_refresh_ok”: boolean}`. “
up” indicates that the server is up and running; “last_refresh_ok” indicates whether the most recent bookmarks refresh succeeded or failed (for any reason: Raindrop API error, OAuth failure, refresh token expired, etc). `last_refresh_ok` is `true` at startup, until the initial refresh runs and we know whether it succeeded. My monitoring tool will poll this endpoint to tell whether to alert me.

`/_status` is served with `Cache-Control: no-store`.

Of course, more detailed messages about what went wrong will be available in the log.

### Templating
The end user can completely customize output pages via templates, loaded from a
directory on disk provided via environment variable.

Templates are loaded once at startup and are not reloaded on change; the operator
restarts the app to pick up edits. If any template is missing or fails to parse, the app logs the error and exits non-zero rather than starting and serving errors.

One template renders search results for both the JS snippet response and the no-JS full-page response, so the two paths cannot drift.

Interpolated values — including the user's search query, echoed back in the
no-results case — rely on `html/template`'s contextual autoescaping. Template data
is not passed as `template.HTML`.

The set of template files, the data passed to each, and any helper functions are
left to the implementing agent, who documents them in the repo. A complete,
working example template is provided in an `example-template` folder; it is the
only starting point a new operator gets, so it must exercise every template file
and demonstrate every documented field.

Static assets belonging to a template (CSS, fonts) are out of scope for this application — the template is free to use them, but they must be hosted and served by some other process, and the template can pull them in via URL.

### Empty State
When no bookmarks ahve been imported yet, the application just shows a message in the middle of the page that says “No bookmarks yet.”

### CLI
The CLI has three subcommands, “login,” “serve,” and “healthcheck”. Login runs the OAuth process; Serve runs the web server. 

Healthcheck hits the `/_status` endpoint and returns `0` if `up` is `true`; `1` otherwise.

### Date Display
- Bookmark dates are rendered like “July 28, 2026” by default; an environment variable allows customizing this format via a Golang date format string
- An environment variable allows the user to set the timezone used for date display, like `America/Detroit`

### SEO & Discovery
A `sitemap.xml` file is generated at launch and after every Raindrop refresh, persisted in memory, and served from memory at `/sitemap.xml` with appropriate HTTP caching headers.

### Other
- App configuration is via environment variables. Environment variables are documented in the README.
- The target tag is configurable via an env variable, but defaults to `_public`.
- SQLite DB directory is configurable via env variable. (Directory, not `.db` file, allowing for WAL.)
- Number of bookmarks per page is configurable via an env variable, defaulting to 10.
- Listen address and port are configurable via an environment variable.
## Template Requirements
The initial template for this application is intended to appear as if it’s part of dzombak.com. The template should include the same header and footer as that website, and it should use the same typography, colors, etc. Existing markup and CSS classes are reused where possible. The CSS and any required fonts, etc are not served by this application; they can be used directly from dzombak.com. Any CSS that’s specific to this app can be embedded in the template directly.

Note that the source code for this site’s template is available at https://github.com/cdzombak/ghost-theme-dzombakdotcom .

The Search template specifes `<meta name="robots" content="noindex">` on results pages.

### Dark Mode
Like Dzombak.com, the template supports dark mode, using the same color scheme as Dzombak.com. Dark mode is used automatically when the user’s system is in dark mode. 

### Responsive Design
Like Dzombak.com, the template scales down from desktop, to tablet-class devices, to phones.

### Metadata
The template includes a `<link>` tag for my bookmarks RSS feed, https://www.dzombak.com/feeds/bookmarks.rss.xml .

No OpenGraph tags are included in the MVP template.

### Accessibility
Target: WCAG 2.2 Level AA. Where the inherited dzombak.com markup or palette fails a criterion, the agent should flag it rather than silently diverge from the site's existing design.
#### Bookmark list pages
- Each page's bookmarks are a single `<ul>`; each bookmark is one `<li>`.
- The bookmark title is the link. The card is not a wrapping anchor.
- Cover images are decorative: `alt=""`. 
- Covers get explicit `width`/`height` (or a CSS aspect-ratio box) plus
  `loading="lazy"`, so lazy-loaded images don't shift text under the pointer
  or keyboard focus.
#### Pagination
- Wrapped in `<nav aria-label="Pagination">`.
- The current page carries `aria-current="page"`.
- Every control has a text accessible name — "Previous page", not a bare `«`.
- Unavailable prev/next are omitted or rendered as non-focusable text, never as
  `aria-disabled` links.
#### Search
- The input is a real `<label>`ed `<input type="search">`. Placeholder text is
  not a label.
- `/search` works without JavaScript as a GET form submission, rendering
  results server-side into the full page. The JS path is an enhancement over
  this, not a replacement for it.
- Results are **not** a combobox/listbox. They are a page region that updates.
  Do not apply `role="combobox"`, `aria-expanded`, or `aria-activedescendant`.
- A visually-hidden `role="status"` element announces the outcome only —
  "12 results for goroutines", "No results for xyzzy". The results container
  itself is not a live region; announcing every result on each keystroke is
  unusable.
- Focus never leaves the input on update. The results container gets
  `aria-busy="true"` while a request is in flight.
- Debounce is at least 300ms, which also keeps status announcements from
  queueing up.
- The results container is `aria-labelledby` the search input's label so its
  contents have context when navigated by landmark or heading.
## Implementation Details
- Language: Go
- Database: SQLite
- Database library: modernc.org/sqlite (cgo-free, and ships FTS5 enabled by default)
- Raindrop API client: https://github.com/cdzombak/raindrop-io-api-client
- Templating library: Golang standard library `html/template`

### Data Model
Data model is left to the implementing agent, execpt as defined under Search, who should keep in mind the various queries that’ll need to be supported, and especially search.

### Search Implementation
#### Normalization
Both the indexed text and the user's query pass through the same normalizer. Any
divergence between index-time and query-time normalization produces silently
unmatchable records, so this must be a single shared function.

1. Apply Unicode NFKD.
2. Apply the special-case fold table below (characters NFKD does not decompose).
3. Strip all combining marks (Unicode category `Mn`) — this makes `e` match `é`.
4. Lowercase.
5. Delete `'`, `’`, and `` ` `` outright, with no replacement. ("Dzombak's" →
   `dzombaks`, so `dzombak` and `dzombaks` both match.) Replacing them with a
   space instead would emit a useless `s` token.
6. Replace every character that is not a Unicode letter or digit with a space.
   This covers `,` `.` `-` `/` `:` `&` `—` emoji, and everything else.
7. Collapse runs of spaces; trim.

Fold table: `ß→ss  ø→o  đ→d  ł→l  æ→ae  œ→oe  þ→th  ð→d` (and uppercase forms).

Non-Latin scripts pass through step 6 intact and are matched literally;
transliteration is out of scope. CJK is not word-segmented by the tokenizer, so a
run of CJK characters forms one token and only prefix-matches as such. Acceptable
for this collection.

#### Indexed content
Each bookmark row has a `search_text` column: the normalized title, a space, then
the normalized domain. The domain comes from the bookmark URL, lowercased with any
leading `www.` stripped, before normalization — so
`https://www.news.ycombinator.com/item?id=1` contributes `news ycombinator com`.
`search_text` is written in the same transaction as its bookmark row.

Excerpts are deliberately not indexed. Adding them later is a change to this
column's construction plus a rebuild.

#### FTS5 index
```
CREATE VIRTUAL TABLE bookmarks_fts USING fts5(
  search_text,
  content='bookmarks',
  content_rowid='id',
  tokenize="unicode61 remove_diacritics 2",
  prefix='2 3'
);
```

External-content mode: the FTS table stores only the index, not a second copy of
the text. It requires the standard `INSERT`/`UPDATE`/`DELETE` triggers on
`bookmarks` to stay in sync; the agent must create all three even though imports
are append-only in normal operation.

`prefix='2 3'` builds prefix indexes for 2- and 3-character prefixes, which covers
the common typing case. Because our normalizer already produces lowercase,
mark-free tokens, the tokenizer settings are belt-and-braces rather than the
primary mechanism.

#### Match semantics
The normalized query is split on spaces into tokens. A bookmark matches if **every**
token is a prefix of **some** token in its `search_text` (order-independent AND).

- `sqlite serv` matches "SQLite for Servers"
- `serv sqlite` also matches — order does not matter
- `routines` does **not** match "Goroutines" — prefixes, not substrings
- `news.ycombinator.com` normalizes to three tokens, all of which must match

AND rather than OR: this is a small personal collection, and each keystroke should
narrow the results.

#### Query construction
Build the MATCH expression by joining tokens as `"tok"*` with ` AND `:

```
"sqlite"* AND "serv"*
```

Each token is wrapped in double quotes so that a token which happens to be an FTS5
keyword (`and`, `or`, `not`, `near`) is treated as a literal string rather than
syntax. Normalization has already removed every character that carries meaning to
the FTS5 query parser — including double quotes, `*`, `^`, `:`, `(`, `)`, `-` — so
the quoted form is safe by construction and needs no escaping. The assembled
expression is bound as a single parameter.

```
SELECT b.*
FROM bookmarks_fts f
JOIN bookmarks b ON b.id = f.rowid
WHERE bookmarks_fts MATCH ?
ORDER BY b.created DESC, b.id DESC
LIMIT 101;
```

`bm25()` and the `rank` column are unused: ordering is strictly chronological to
match the list pages.

### HTTP Caching
All pages and static assets are served with cache headers and support conditional
requests, so an nginx reverse proxy can cache them and revalidate cheaply.

**Prerendered pages** (bookmark lists, search page):
- `ETag` is a hash of the rendered page's bytes, computed once at prerender time
  and stored alongside the page in the snapshot. Deriving it from the bytes rather
  than from a refresh counter means a refresh that finds no new bookmarks leaves
  every ETag unchanged, and caches keep validating instead of re-downloading.
  This is the common case.
- `Cache-Control: public, max-age=300`
- `Last-Modified` is the timestamp of the refresh that produced the snapshot.
- Worst-case staleness for a visitor is therefore the refresh interval plus five
  minutes; if that is too loose, lower `max-age` rather than complicating the
  ETag.

**Search results** (both the snippet endpoint and `/search?q=`): `Cache-Control:
no-store`, no ETag.

**Error responses** (404, 5xx): `Cache-Control: no-store`.

Handle conditional requests and HEAD correctly rather than hand-rolling 304s.

The app does not itself compress responses, so no `Vary` header is required.

### SQLite Best Practices
Care is taken to avoid the bad defaults listed in https://mort.coffee/home/sqlite-editions/ , and to follow best practices outlined in https://kerkour.com/sqlite-for-servers .
### Server Lifecycle
The `http.Server` sets explicit timeouts rather than relying on the zero-value
defaults, which are unbounded: `ReadHeaderTimeout` 10s, `ReadTimeout` 15s,
`WriteTimeout` 30s, `IdleTimeout` 120s. (`ReadHeaderTimeout` in particular is
flagged by `gosec` G112 and will otherwise fail CI lint.)

On `SIGINT` or `SIGTERM`, the server stops accepting connections and calls
`Shutdown` with a 15-second timeout to let in-flight requests finish, then closes
the SQLite database and exits 0. An in-progress bookmarks refresh is cancelled via
context rather than waited on; a partial refresh is harmless because the next
startup refreshes again.
### Logging
This application uses the Golang standard library’s structured logger. It does not log every request (this is nginx’s job), but it does log anything interesting or unusual that occurs during operation (e.g. bookmark refreshes) as well as any errors that occur.

### Build Process & Artifacts
A Makefile is provided that can run:
- lint (golangci-lint, plus any other linters that might be helpful for this application)
- actionlint (for GitHub Actions YAML)
- test (runs the test suite)
- build-docker (builds a Docker image for the current machine)
- clean (cleans `./out`)

GitHub Actions CI workflows are provided that:
- On push or PRs, runs lint, actionlint, and the test suite
- On tags like `vX.Y.Z`, requires lint and the test suite to pass, then builds Docker images for arm64 and amd64 and pushes them to Docker Hub
- Creates a GitHub release
- Updates rolling `vX` and `vX.Y` release tags

The version number is embedded in the binary and the Docker image.

The implementing agent should refer to https://github.com/cdzombak/ebird-rss for an example 
Makefile and GitHub Actions setup that I prefer.

Note that the template is not included in the built artifacts; it is provided by the site operator at a specific location on disk, which is provided to the application via an environment variable.

### Dockerfile
- Use UID/GID 1000 inside the Dockerfile.
- Users are expected to provide 4 mounts: OAuth state file, DB directory, template directory, and images directory). All but the template directory are writable.
- The `/_status` endpoint is used for Docker health checks

The Dockerfile is based on `scratch` to minimize size. Two items require special handling:
- `COPY --from=builder /etc/ssl/certs/ca-certificates.crt`, allowing the app to connect to HTTPS hosts
- `import _ "time/tzdata"` in the Go program, since `scratch` has no timezone database

### Deployment
- Deployment is left to users, not accomplished by an AI agent
- The app is intended to run behind a reverse proxy; the app itself does not handle e.g. TLS termination
- The repo provides an example Docker Compose file suitable for deployment
- The repo provides an example Nginx configuration file, which the user can easily modify to run Nginx as the TLS-terminating reverse proxy in front of the Docker Compose stack. This example Nginx file caches responses as possible and implements response compression.

### OAuth State File
Writes to this file are atomic, so we never overwrite only part of the file and crash or otherwise exit.

### Test Coverage
Tests never touch the network or a real Raindrop account. The Raindrop API is
stubbed with `httptest.Server` returning canned JSON fixtures; the database is a
temporary SQLite file per test. Anything time-dependent takes an injected clock,
and fixtures use fixed timestamps, so golden output is stable.

Coverage percentage is not a goal. The following behaviors are:

**Normalization** — table-driven, exercising the shared normalizer directly:
diacritics (`é`→`e`), the fold table (`ß`→`ss`), apostrophe deletion
(`Dzombak's`→`dzombaks`), punctuation-to-space, collapse/trim, a CJK string
passing through intact, and a query that normalizes to the empty string.

**Search** — against a real FTS5 index built from a small fixture set. The worked
examples in the search spec are the test cases: `sqlite serv` matches, `serv
sqlite` matches, `routines` does not match "Goroutines",
`news.ycombinator.com` matches by domain. Plus: a token that is an FTS5 keyword
(`and`, `near`) is treated literally, results are ordered reverse-chronologically,
the 100-result cap triggers its "refine your search" state at 101 matches, and
short/empty queries return the empty state rather than everything.

**Import and refresh** — pagination across multiple API response pages assembles
the full set; running the same import twice is idempotent (no duplicate rows); the
FTS index matches the bookmarks table after an insert and after an update, which
is the check that the external-content triggers are actually correct; a refresh
that fails partway leaves the previous prerendered snapshot serving; a refresh
that returns no new bookmarks leaves page ETags unchanged.

**Handlers** — via `httptest`, using the `example-template` directory so the tests
double as verification that the shipped example is complete and current:

- `/`, `/1`, `/2` render; `/` and `/1` agree; out-of-range and non-numeric page
  paths 404
- `/search?q=` renders a full page without JavaScript; the snippet endpoint
  renders the same results template and sends `no-store`
- a page request with a matching `If-None-Match` returns 304
- startup fails non-zero against a template directory with a malformed template

**Golden files** for one bookmark list page and one search result set, refreshed
via a `-update` flag. These are change detectors, not correctness proofs — their
job is to make template and markup changes visible in review.

Out of scope: the OAuth login flow, Docker and Compose packaging, and
accessibility, all verified manually.

### README
The README covers:
- A (short) overview of the app
- Instructions on configuring & deploying the app
