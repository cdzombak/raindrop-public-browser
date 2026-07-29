# Templates

The application renders every HTML page from operator-supplied templates loaded
at startup from the directory named by the template-directory environment
variable. Templates are Go [`html/template`](https://pkg.go.dev/html/template)
files. They are parsed once at startup and never reloaded; restart the app to
pick up edits. If any of the three required files is missing or fails to parse,
the app logs the error and exits non-zero rather than serving broken pages.

A complete, working example lives in [`example-template/`](example-template/).
It exercises every template file and every documented field.

## The three required files

| File | Rendered for | Data |
| --- | --- | --- |
| `list.html.tmpl` | `/`, `/1`, `/2`, … — one bookmark list page | [`render.ListData`](#listdata) |
| `search.html.tmpl` | `/search` and `/search?q=…` — the full search page | [`render.SearchData`](#searchdata) |
| `results.html.tmpl` | the search-results region, both inside the search page and alone at `/search/results?q=…` | [`render.ResultsData`](#resultsdata) |

All three names are required exactly as spelled. Extra files in the directory
are ignored — the loader reads only these three.

## How the templates are parsed

`render.Load` creates one `*template.Template` set and parses the three files
into it, each associated under its own file name:

```go
tpl := template.New("root")
for _, name := range []string{"list.html.tmpl", "search.html.tmpl", "results.html.tmpl"} {
    tpl.New(name).Parse(...)
}
```

Two consequences matter when you write your own templates:

1. **They share one namespace.** Any `{{define "…"}}` in one file is callable
   from the other two. The example uses this for the shared header, footer,
   stylesheet and bookmark-card markup (see [Shared
   definitions](#shared-definitions-in-the-example)). Names are global across
   the set, so pick distinctive ones.
2. **`results.html.tmpl` is included by name.** `search.html.tmpl` embeds the
   results region with

   ```gotemplate
   {{template "results.html.tmpl" .Results}}
   ```

   The same template is executed on its own for the snippet endpoint. That is
   deliberate: the no-JS page and the JavaScript-updated page cannot drift,
   because there is exactly one definition of what a result looks like.

Pages are rendered with `ExecuteTemplate(w, "<file name>", data)`, so the
top-level (non-`define`) content of each file is what gets written.

### There are no custom template functions

No `FuncMap` is installed. Only the standard `html/template` builtins are
available: `and`, `call`, `eq`, `ge`, `gt`, `html`, `index`, `js`, `le`, `len`,
`lt`, `ne`, `not`, `or`, `print`, `printf`, `println`, `slice`, `urlquery`, and
the actions `if` / `range` / `with` / `template` / `block`.

### Escaping

Every interpolated value — including the user's search query echoed back in the
no-results state — relies on `html/template`'s contextual autoescaping. **No
template data is passed as `template.HTML`, `template.JS`, `template.CSS` or
`template.URL`, and your templates must not introduce any.** A bookmark title
containing `<script>` is escaped as text; a query containing `"` is escaped
correctly inside an attribute value.

Note that `html/template` strips comments from `<style>` and `<script>` element
bodies when it renders them. Comments in the template source survive in the file
but do not appear in the served HTML. This is harmless, and it is why the
example's CSS and JS look uncommented when you view source.

## Data reference

### `Bookmark`

One bookmark, used by both list pages and search results.

| Field | Type | Notes |
| --- | --- | --- |
| `.Title` | `string` | The bookmark's title. May be empty; the example falls back to the URL so the link never has an empty accessible name. |
| `.URL` | `string` | The target URL. Outbound links must use `rel="noopener noreferrer"` — **not** `nofollow`. |
| `.Excerpt` | `string` | Raindrop's excerpt. Often empty; guard with `{{if .Excerpt}}`. |
| `.Domain` | `string` | The URL's host, lowercased with a leading `www.` stripped. |
| `.CoverPath` | `string` | Site-relative cover URL, e.g. `/covers/123-abcd1234.jpg`. **Empty when the bookmark has no stored cover — the template must then omit the `<img>` entirely rather than substituting a placeholder.** |
| `.Date` | `string` | The creation date, already formatted and localised per the app's date-format and timezone configuration. Render it as text; do not parse it. |

Cover images are decorative: render them with `alt=""` and `loading="lazy"`.
Their intrinsic dimensions are unknown at render time (they are fetched from
arbitrary third-party hosts), so the template must reserve space with an
aspect-ratio box or explicit `width`/`height` so a late-loading cover cannot
shift text under the pointer or the keyboard focus ring.

### `PageLink`

One numbered entry in the pagination control.

| Field | Type | Notes |
| --- | --- | --- |
| `.Num` | `int` | The page number, 1-based. |
| `.URL` | `string` | Root-relative page URL, e.g. `/3`. |
| `.Current` | `bool` | True for the page being rendered. Give that link `aria-current="page"`. |

### `ListData`

Passed to `list.html.tmpl`.

| Field | Type | Notes |
| --- | --- | --- |
| `.Bookmarks` | `[]Bookmark` | This page's bookmarks, newest first. Empty on the empty-state page. |
| `.Empty` | `bool` | True when no bookmarks have been imported at all. Page 1 is still rendered; show only the empty-state message. |
| `.Page` | `int` | This page's number, 1-based. |
| `.TotalPages` | `int` | Always at least 1. |
| `.PrevURL` | `string` | `/N-1`, or `""` on the first page. Omit the control entirely when empty. |
| `.NextURL` | `string` | `/N+1`, or `""` on the last page. Omit the control entirely when empty. |
| `.Pages` | `[]PageLink` | Every page, in order, for numbered links. |
| `.CanonicalURL` | `string` | Absolute canonical URL of this page. Must appear as `<link rel="canonical">` — `/` and `/1` render the same content, so this is what keeps them from being treated as duplicates. |
| `.BaseURL` | `string` | The site's absolute base URL, no trailing slash. |
| `.Version` | `string` | The application version. |

### `ResultsData`

Passed to `results.html.tmpl`, and reachable as `.Results` from
`search.html.tmpl`.

| Field | Type | Notes |
| --- | --- | --- |
| `.Query` | `string` | The query as typed, trimmed. Safe to echo — autoescaping handles it. |
| `.Queried` | `bool` | **False** for the initial, empty, whitespace-only, too-short, and normalizes-to-nothing states: show the "Start typing to search…" prompt. **True** once a search actually ran. |
| `.Bookmarks` | `[]Bookmark` | Matches, newest first. Empty with `.Queried` true means "no results". |
| `.Count` | `int` | `len(.Bookmarks)`. |
| `.Truncated` | `bool` | True when more than the 100-result cap matched and the list was cut. There is no pagination for search. |
| `.StatusText` | `string` | The pre-composed outcome announcement — `"12 results for goroutines"`, `"1 result for x"`, `"No results for xyzzy"`, `"More than 100 results for go"`. Empty when `.Queried` is false. See [The `role="status"` mechanism](#the-rolestatus-mechanism). |

The three states a `results.html.tmpl` must handle:

| State | Condition | What to render |
| --- | --- | --- |
| Prompt | `not .Queried` | "Start typing to search…" |
| No results | `.Queried` and no `.Bookmarks` | A distinct region naming the query |
| Results | `.Queried` and `.Bookmarks` | The bookmark list, plus a "refine your search" note when `.Truncated` |

`results.html.tmpl` **must be a fragment** — no `<html>`, `<head>` or `<body>` —
because it is served on its own from the snippet endpoint.

### `SearchData`

Passed to `search.html.tmpl`.

| Field | Type | Notes |
| --- | --- | --- |
| `.Results` | `ResultsData` | The results state for this render. `EmptyResults()` (all zero values) for the prerendered `/search` page; a real result set for `/search?q=…`. |
| `.BaseURL` | `string` | The site's absolute base URL, no trailing slash. |
| `.Version` | `string` | The application version. |

## The snippet endpoint

```
GET /search/results?q=<query>
```

| | |
| --- | --- |
| Response body | `results.html.tmpl` rendered alone, i.e. an HTML fragment |
| Content-Type | `text/html; charset=utf-8` |
| Cache-Control | `no-store` (no `ETag`) |
| Status | `200` for every query, including empty, too-short, and no-match |

Empty, whitespace-only, shorter-than-two-characters, and
normalizes-to-nothing queries all return the prompt state with HTTP 200. The
endpoint never returns all bookmarks and never redirects.

`robots.txt` disallows `/search`, and `search.html.tmpl` sets
`<meta name="robots" content="noindex">`.

## The JavaScript enhancement

`/search` works with JavaScript disabled: the form is a plain
`<form action="/search" method="get">` with `<input type="search" name="q">`,
and submitting it renders the whole page server-side with results. Everything
below is layered on top of that and never replaces it.

The example's inline script:

1. Binds to `input` events on `#search-input` and debounces **300 ms** (the
   spec's floor; raising it is fine, lowering it is not — it is also what keeps
   status announcements from queueing up on every keystroke).
2. Trims the value. If it is **shorter than 2 characters**, it fires **no
   request**: it restores the prompt state locally, cancels anything in flight,
   and clears `aria-busy`.
3. Otherwise sets `aria-busy="true"` on `#search-results` and fetches
   `/search/results?q=` + `encodeURIComponent(q)`.
4. Replaces `#search-results`'s `innerHTML` with the response, copies the
   response's status text into the live region, and removes `aria-busy`.
5. **Never moves focus.** The input is outside `#search-results`, and nothing
   calls `focus()`.
6. **Discards stale responses.** Each request takes a monotonically increasing
   sequence number and an `AbortController`; a response whose number is no
   longer current is dropped, so out-of-order replies can never overwrite newer
   results.
7. **Never calls `preventDefault()` on submit.** Pressing Enter performs the
   normal GET to `/search?q=…` and reloads the page with server-rendered
   results. That is correct behaviour, not a bug.
8. Degrades to nothing if `fetch` is unavailable or the expected elements are
   missing — the plain form remains.

The prompt-state markup used in step 2 is captured from the server render when
the page loads without a query (`results.html.tmpl` marks it with
`data-search-empty`). A short hard-coded fallback in the script covers the case
where the page was loaded as `/search?q=…` instead. If you change the prompt
markup, update that fallback string too.

## The `role="status"` mechanism

Announcing every result on every keystroke is unusable. Instead:

- `search.html.tmpl` renders **one persistent, visually hidden**
  `<div role="status" id="search-status">`, **outside** `#search-results` so it
  is never replaced. Replacing a live region's element defeats it.
- Its text is seeded server-side from `.Results.StatusText`, so the no-JS round
  trip works.
- `results.html.tmpl` ends every state with a hidden carrier element,
  `<div hidden data-search-status>{{.StatusText}}</div>`.
- After each swap the script copies that element's text into `#search-status`.
  Only the outcome is announced: "12 results for goroutines", "No results for
  xyzzy".
- `#search-results` itself is **not** a live region.

The results region is **not** a combobox or listbox. Do not add
`role="combobox"`, `aria-expanded`, `aria-activedescendant`, or
`aria-autocomplete`. It is a labelled page region whose contents are replaced;
the example gives it `role="region"` and `aria-labelledby="search-label"`, which
points at the search input's real `<label>`.

## Accessibility requirements

These are binding on any template, not just the example. Target is WCAG 2.2
Level AA.

**Bookmark list pages**

- All of a page's bookmarks are one `<ul>`; each bookmark is one `<li>`.
- The bookmark **title is the link**. The card is not a wrapping anchor.
- Cover images are decorative: `alt=""`, plus `loading="lazy"` and a reserved
  aspect-ratio box.
- Outbound links carry `rel="noopener noreferrer"`, never `nofollow`.

**Pagination**

- Wrapped in `<nav aria-label="Pagination">`.
- The current page carries `aria-current="page"`.
- Every control has a text accessible name — "Previous page", not a bare `«`.
  The example gives numbered links a visually hidden "Page " prefix so their
  names read as "Page 3".
- Unavailable previous/next controls are **omitted entirely**, never rendered as
  `aria-disabled` links.

**Search**

- A real `<label>` for the `<input type="search">`. Placeholder text is not a
  label.
- The form works without JavaScript.
- Focus never leaves the input on update; `aria-busy="true"` while a request is
  in flight.
- Debounce ≥ 300 ms.

## Styling, and what the example does

Static assets are **out of scope for this application** — it serves no CSS,
fonts or images of its own. A template is free to reference assets hosted
anywhere by URL.

The shipped example is deliberately minimal and self-contained: system fonts,
no external assets, and a small embedded `<style>` block in `list.html.tmpl`
(shared with the search page via `{{template "page-css"}}`). Dark mode is
handled by redefining the palette's CSS custom properties under
`@media (prefers-color-scheme: dark)`; the layout is a single centered column
that reflows for phones. All colour pairs in both modes meet WCAG 2.2 AA
contrast, focus indicators are explicit `:focus-visible` outlines, and
pagination targets are at least 44×44 px.

A full-featured template styled to match dzombak.com (hotlinked site CSS,
Ghost bookmark-card markup, the site's header and footer) lives in
[cdzombak/bookmarks-template-dzombakdotcom](https://github.com/cdzombak/bookmarks-template-dzombakdotcom).

### Shared definitions in the example

Defined in `list.html.tmpl`, used from the other files:

| Name | Data | Purpose |
| --- | --- | --- |
| `page-css` | none | The `<style>` block |
| `bookmark-item` | `Bookmark` | One `<li>` bookmark entry, used by the list page **and** by `results.html.tmpl` so the two cannot drift |

If you replace `list.html.tmpl` wholesale, either keep these definitions or
update the other two files.
