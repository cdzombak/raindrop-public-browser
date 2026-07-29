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

## CLI

The binary has three subcommands:

- `login` — runs the interactive Raindrop OAuth flow and writes the OAuth
  state file.
- `serve` — runs the web server.
- `healthcheck` — probes the running server's `/_status` endpoint; exits 0 if
  the server is up, 1 otherwise. (Used by the Docker health check.)

## Configuration

All configuration is via environment variables:

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `OAUTH_STATE_FILE` | yes | — | Path to the JSON file storing OAuth credentials. Created by `login` with 0600 permissions; written back (atomically) as tokens are refreshed. |
| `DB_DIR` | for `serve` | — | Directory for the SQLite database (a directory, not a `.db` file, allowing for WAL). Created if missing. |
| `TEMPLATE_DIR` | for `serve` | — | Directory containing the templates. Loaded once at startup; if any template is missing or fails to parse, the app exits non-zero. |
| `IMAGES_DIR` | for `serve` | — | Directory where cover images are stored. Grows without bound; no garbage collection. |
| `LISTEN_ADDR` | no | `:8080` | Listen address and port, e.g. `127.0.0.1:8080`. |
| `BASE_URL` | no | `http://localhost:8080` | External base URL of the site (no trailing slash); used for the sitemap and canonical URLs. Set this in production. |
| `REFRESH_INTERVAL_MINUTES` | no | `15` | Minutes between bookmark refreshes from the Raindrop API. A refresh also runs at startup. |
| `PUBLIC_TAG` | no | `_public` | The Raindrop tag that marks a bookmark as public. |
| `PER_PAGE` | no | `10` | Bookmarks per list page. |
| `DATE_FORMAT` | no | `January 2, 2006` | Bookmark date display format, as a [Go date format string](https://pkg.go.dev/time#pkg-constants). |
| `DISPLAY_TIMEZONE` | no | `UTC` | Timezone for date display, e.g. `America/Detroit`. |
| `RAINDROP_CLIENT_ID` | for `login` | — | Raindrop OAuth app client ID. |
| `RAINDROP_CLIENT_SECRET` | for `login` | — | Raindrop OAuth app client secret. |
| `OAUTH_REDIRECT_URI` | no | `http://localhost:8080/oauth` | OAuth redirect URI; must exactly match the redirect URI configured in your Raindrop app. |
| `RAINDROP_PUBLIC_BROWSER_IN_DOCKER` | no | — | Set by the Dockerfile. When set, the `login` callback server binds `0.0.0.0` instead of loopback so a published container port can reach it. |

## Setup

### 1. Create a Raindrop OAuth app

At <https://app.raindrop.io/settings/integrations>, create an app. Set its
redirect URI to `http://localhost:8080/oauth` (or whatever you'll pass as
`OAUTH_REDIRECT_URI`). Note the client ID and secret.

### 2. Log in

```sh
export RAINDROP_CLIENT_ID=... RAINDROP_CLIENT_SECRET=...
export OAUTH_STATE_FILE=./data/oauth-state.json
raindrop-public-browser login
```

Open the printed URL, authorize, and the state file is written. Client
credentials are stored in the state file, so re-login doesn't require them
again.

Via Docker (the published port carries the OAuth callback into the
container):

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

On startup the app prerenders pages from the existing database, starts
serving, then kicks off a refresh. If the refresh token has expired, the app
logs an error and keeps serving existing content; `/_status` reports
`last_refresh_ok: false`.

## Deployment

An example Docker Compose file is provided in
[`deploy/docker-compose.yml`](deploy/docker-compose.yml), and an example
nginx reverse-proxy configuration (TLS termination, response caching and
compression) in [`deploy/nginx.conf`](deploy/nginx.conf).

The container runs as UID/GID 1000 and expects four mounts: the OAuth state
file, the DB directory, the template directory (read-only), and the images
directory.

## Templates

The site's entire HTML output is operator-customizable. Point `TEMPLATE_DIR`
at a directory containing `list.html.tmpl`, `search.html.tmpl`, and
`results.html.tmpl`. Templates are loaded once at startup; restart the app to
pick up edits. See [TEMPLATES.md](TEMPLATES.md) for the full contract, and
[`example-template/`](example-template/) for a complete working example
(styled to match dzombak.com).

## URLs

| Path | Description |
| --- | --- |
| `/`, `/1`, `/2`, … | Bookmark list pages, reverse-chronological. `/` serves page 1 and declares `/1` canonical. |
| `/search` | Search page; `/search?q=…` works without JavaScript. Disallowed in `robots.txt`. |
| `/search/results?q=…` | Server-rendered HTML fragment used by the live-search JavaScript. |
| `/covers/…` | Downloaded cover images (immutable cache headers). |
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

Golden-file tests capture rendered pages; refresh them after intentional
template/markup changes with `go test ./internal/... -update`.

## License

GPL-3.0; see [LICENSE](LICENSE).
