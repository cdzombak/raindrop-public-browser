# raindrop-public-browser

A small web application that presents a public, paginated, searchable browser
for [Raindrop.io](https://raindrop.io) bookmarks tagged `_public` (the tag is
configurable).

- Bookmarks are synced from the Raindrop API on a schedule and stored in
  SQLite; list pages, the search page, and `sitemap.xml` are prerendered to
  memory and served from memory.
- Search matches bookmark titles and domains with normalized, order-independent
  prefix matching (FTS5), live as you type — degrading to a plain HTML form
  without JavaScript.
- Cover images are downloaded, verified, and served locally with immutable
  cache headers.
- Output is fully operator-templated; see [TEMPLATES.md](TEMPLATES.md) and the
  complete working example in [`example-template/`](example-template/).
- Designed to run behind a reverse proxy (TLS termination, response caching
  and compression are the proxy's job; an example nginx config is provided).

## Publishing a bookmark is one-way

**Tagging a bookmark `_public` cannot be undone from Raindrop.** The sync only
ever adds and updates rows, never deletes them, so untagging a bookmark — or
deleting the raindrop outright — leaves it on the public site indefinitely.

To retract one, untag it in Raindrop *first*, then delete its row:

```sh
sqlite3 "$DB_DIR/bookmarks.db" 'DELETE FROM bookmarks WHERE id = <raindrop id>;'
```

In the other order, the next refresh finds the tag and puts the row back. No
restart is needed — a delete trigger keeps the search index in sync, and pages
are re-prerendered every refresh, so it disappears within one
`REFRESH_INTERVAL_MINUTES`. The cover image stays in `IMAGES_DIR` until you
delete that too.

## CLI

- `login` — run the interactive Raindrop OAuth flow and write the state file.
- `serve` — run the web server.
- `healthcheck` — probe the running server's `/_status`; exit 0 if it is up, 1
  if not. Used by the Docker health check.
- `version`, and `help` (also `-h`, `--help`).

## Configuration

All configuration is via environment variables.

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `OAUTH_STATE_FILE` | `login`, `serve` | — | JSON file holding the OAuth credentials. Written 0600 by `login`, and rewritten atomically as tokens refresh. |
| `DB_DIR` | `serve` | — | Directory for the SQLite database — a directory, not a `.db` file, so WAL works. Created if missing. |
| `TEMPLATE_DIR` | `serve` | — | Directory containing the templates. Loaded *and* test-rendered once at startup; anything missing, unparseable, or broken at render time exits non-zero. |
| `IMAGES_DIR` | `serve` | — | Directory for cover images. Created if missing. Grows without bound; no garbage collection. |
| `LISTEN_ADDR` | no | `:8080` | Listen address and port, e.g. `127.0.0.1:8080`. |
| `BASE_URL` | no | `http://localhost:8080` | External base URL, used for the sitemap and canonical links. Set this in production. Must be absolute (`https://bookmarks.example.com`) and name the site root: a path, query string, or fragment is rejected. Give the app a hostname or subdomain of its own. |
| `REFRESH_INTERVAL_MINUTES` | no | `15` | Minutes between refreshes from the Raindrop API. One also runs at startup. |
| `PUBLIC_TAG` | no | `_public` | The Raindrop tag that marks a bookmark public. |
| `PER_PAGE` | no | `10` | Bookmarks per list page. |
| `DATE_FORMAT` | no | `January 2, 2006` | Date display format, as a [Go layout string](https://pkg.go.dev/time#pkg-constants). |
| `DISPLAY_TIMEZONE` | no | `UTC` | Timezone for displayed dates, e.g. `America/Detroit`. |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, or `error`; an unrecognized value warns and falls back to `info` rather than refusing to start. Requests are never logged, at any level — that is the reverse proxy's job. |
| `RAINDROP_CLIENT_ID` | first `login` | — | Raindrop OAuth app client ID. Stored in the state file, so later logins do not need it again. |
| `RAINDROP_CLIENT_SECRET` | first `login` | — | Raindrop OAuth app client secret. Likewise. |
| `OAUTH_REDIRECT_URI` | no | `http://localhost:8080/oauth` | Must exactly match the redirect URI configured in your Raindrop app. |
| `RAINDROP_PUBLIC_BROWSER_IN_DOCKER` | no | — | Set by the Dockerfile. Makes the `login` callback server bind `0.0.0.0` instead of loopback, so a published container port can reach it. |

## Setup

### 1. Create a Raindrop OAuth app

At <https://app.raindrop.io/settings/integrations>, create an app with redirect
URI `http://localhost:8080/oauth` (or whatever you'll pass as
`OAUTH_REDIRECT_URI`). Note the client ID and secret.

### 2. Log in

```sh
export RAINDROP_CLIENT_ID=... RAINDROP_CLIENT_SECRET=...
export OAUTH_STATE_FILE=./data/oauth-state.json
raindrop-public-browser login
```

Open the printed URL and authorize; the state file is written.

Via Docker, where the published port carries the OAuth callback into the
container:

```sh
docker run --rm -it -p 8080:8080 \
  -e RAINDROP_CLIENT_ID -e RAINDROP_CLIENT_SECRET \
  -e OAUTH_STATE_FILE=/data/oauth-state.json \
  -v "$PWD/data:/data" \
  cdzombak/raindrop-public-browser:1 login
```

### 3. Serve

```sh
export OAUTH_STATE_FILE=./data/oauth-state.json
export DB_DIR=./data/db IMAGES_DIR=./data/images
export TEMPLATE_DIR=./example-template
export BASE_URL=https://bookmarks.example.com
raindrop-public-browser serve
```

The app prerenders pages from the existing database, starts serving, then
refreshes. If the refresh token has expired it logs an error and keeps serving
what it has, with `/_status` reporting `last_refresh_ok: false`.

## Deployment

Examples are provided for [Docker Compose](deploy/docker-compose.yml) and for
an [nginx](deploy/nginx.conf) reverse proxy handling TLS, caching, and
compression.

The container runs as UID/GID 1000 and expects two mounts: a writable data
directory (holding the OAuth state file, the DB directory, and the images
directory) and the template directory, read-only.

Mount the *directory* containing the OAuth state file, never the file itself.
The app replaces that file by renaming a temp file over it, which cannot be
done to a bind-mounted file — every token refresh would fail.

## Templates

Point `TEMPLATE_DIR` at a directory containing `list.html.tmpl`,
`search.html.tmpl`, and `results.html.tmpl`; restart the app to pick up edits.
See [TEMPLATES.md](TEMPLATES.md) for the full contract and
[`example-template/`](example-template/) for a working example. A full-featured
template styled to match dzombak.com is maintained separately in
[cdzombak/bookmarks-template-dzombakdotcom](https://github.com/cdzombak/bookmarks-template-dzombakdotcom).

## URLs

| Path | Description |
| --- | --- |
| `/`, `/1`, `/2`, … | Bookmark list pages, reverse-chronological. `/` serves page 1 and declares `/1` canonical. |
| `/search` | Search page; `/search?q=…` works without JavaScript. Blank queries (`?q=`, `?q=%20`) redirect here, so the cacheable page has exactly one URL. Disallowed in `robots.txt`. |
| `/search/results?q=…` | Server-rendered HTML fragment for the live-search JavaScript. |
| `/covers/…` | Downloaded cover images, immutably cached. |
| `/sitemap.xml` | Regenerated after every refresh. |
| `/robots.txt` | |
| `/_status` | `{"up": true, "last_refresh_ok": …}`, for monitoring. `last_refresh_ok` is true at startup until the first refresh completes. |

## Development

Requires Go (see `go.mod` for the version) and, optionally,
[golangci-lint](https://golangci-lint.run),
[actionlint](https://github.com/rhysd/actionlint), and Docker.

```sh
make test          # go test -race ./...
make lint          # golangci-lint
make actionlint    # lint GitHub Actions workflows
make build         # build to ./out, version stamped from git
make build-docker  # build a Docker image for the current machine
make clean         # remove ./out
```

Golden-file tests capture rendered pages. After an intentional template or
markup change, refresh them and review the diff:

```sh
go test ./internal/render/ -run Golden -update
```

## License

MIT; see [LICENSE](LICENSE).
