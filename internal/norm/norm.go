// Package norm implements the shared text normalizer used for both
// index-time and query-time search text. Any divergence between the two
// produces silently unmatchable records, so all callers must go through
// Normalize.
package norm

import (
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// foldTable covers characters that Unicode NFKD does not decompose.
var foldTable = map[rune]string{
	'ß': "ss", 'ẞ': "ss",
	'ø': "o", 'Ø': "o",
	'đ': "d", 'Đ': "d",
	'ł': "l", 'Ł': "l",
	'æ': "ae", 'Æ': "ae",
	'œ': "oe", 'Œ': "oe",
	'þ': "th", 'Þ': "th",
	'ð': "d", 'Ð': "d",
}

// Normalize applies the canonical normalization pipeline:
// NFKD, fold table, strip combining marks, lowercase, delete apostrophes,
// replace non-letter/digit runes with spaces, collapse whitespace.
func Normalize(s string) string {
	s = norm.NFKD.String(s)

	var folded strings.Builder
	folded.Grow(len(s))
	for _, r := range s {
		if rep, ok := foldTable[r]; ok {
			folded.WriteString(rep)
			continue
		}
		folded.WriteRune(r)
	}
	s = folded.String()

	var out strings.Builder
	out.Grow(len(s))
	lastSpace := true // trims leading spaces
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Mn, r):
			// strip combining marks: e + ́ -> e
		case r == '\'' || r == '’' || r == '`':
			// deleted outright, no replacement: "Dzombak's" -> "dzombaks"
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			out.WriteRune(unicode.ToLower(r))
			lastSpace = false
		default:
			if !lastSpace {
				out.WriteRune(' ')
				lastSpace = true
			}
		}
	}
	return strings.TrimRight(out.String(), " ")
}

// Tokens normalizes s and splits it on spaces. Returns nil if nothing
// survives normalization.
func Tokens(s string) []string {
	n := Normalize(s)
	if n == "" {
		return nil
	}
	return strings.Split(n, " ")
}

// Domain extracts the search domain from a bookmark URL: the hostname,
// lowercased, with a single leading "www." stripped. Returns "" if the URL
// does not parse or has no host.
func Domain(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	h := strings.ToLower(u.Hostname())
	h = strings.TrimPrefix(h, "www.")
	return h
}

// SearchText builds the indexed search_text for a bookmark: the normalized
// title and domain joined by a space, or whichever one is non-empty.
func SearchText(title, rawURL string) string {
	t := Normalize(title)
	d := Normalize(Domain(rawURL))
	switch {
	case t == "":
		return d
	case d == "":
		return t
	default:
		return t + " " + d
	}
}

// MatchExpr builds an FTS5 MATCH expression from a raw user query:
// each normalized token becomes a quoted prefix term, joined with AND.
// Returns "" if the query normalizes to nothing.
func MatchExpr(query string) string {
	toks := Tokens(query)
	if len(toks) == 0 {
		return ""
	}
	parts := make([]string, len(toks))
	for i, t := range toks {
		parts[i] = `"` + t + `"*`
	}
	return strings.Join(parts, " AND ")
}
