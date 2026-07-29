package norm

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"diacritics", "café", "cafe"},
		{"diacritics uppercase", "Café", "cafe"},
		{"fold ss", "straße", "strasse"},
		{"fold o slash", "Øresund", "oresund"},
		{"fold ae", "Ægir", "aegir"},
		{"apostrophe ascii", "Dzombak's", "dzombaks"},
		{"apostrophe curly", "Dzombak’s", "dzombaks"},
		{"backtick", "Dzombak`s", "dzombaks"},
		{"punctuation commas periods", "a, b. c", "a b c"},
		{"punctuation slash colon", "a/b:c", "a b c"},
		{"ampersand", "rock & roll", "rock roll"},
		{"em dash", "foo—bar", "foo bar"},
		{"emoji", "hello \U0001F600 world", "hello world"},
		{"collapse and trim whitespace", "  a    b   c  ", "a b c"},
		{"cjk passthrough", "日本語", "日本語"},
		{"normalizes to empty", "!!! ---", ""},
		{"lowercasing", "SQLite FOR Servers", "sqlite for servers"},
		{"mixed fold uppercase", "STRASSE VS STRAßE", "strasse vs strasse"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Normalize(c.in)
			if got != c.want {
				t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestTokens(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"simple", "sqlite serv", []string{"sqlite", "serv"}},
		{"empty", "", nil},
		{"punctuation only", "!!! ---", nil},
		{"whitespace only", "   ", nil},
		{"single token", "hello", []string{"hello"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Tokens(c.in)
			if !equalStrSlices(got, c.want) {
				t.Errorf("Tokens(%q) = %#v, want %#v", c.in, got, c.want)
			}
		})
	}
}

func equalStrSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDomain(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"strips www", "https://www.news.ycombinator.com/item?id=1", "news.ycombinator.com"},
		{"no www", "https://example.com/path", "example.com"},
		{"lowercases", "https://WWW.Example.COM/path", "example.com"},
		{"bare url invalid", "not a url \x7f", ""},
		{"no host", "not-a-url-at-all", ""},
		{"http scheme", "http://example.org", "example.org"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Domain(c.in)
			if got != c.want {
				t.Errorf("Domain(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestSearchText(t *testing.T) {
	cases := []struct {
		name  string
		title string
		url   string
		want  string
	}{
		{
			"title and domain",
			"SQLite for Servers",
			"https://www.news.ycombinator.com/item?id=1",
			"sqlite for servers news ycombinator com",
		},
		{"empty title", "", "https://www.example.com", "example com"},
		{"empty domain", "Just a Title", "not a url \x7f", "just a title"},
		{"both empty", "", "not a url \x7f", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SearchText(c.title, c.url)
			if got != c.want {
				t.Errorf("SearchText(%q, %q) = %q, want %q", c.title, c.url, got, c.want)
			}
		})
	}
}

func TestMatchExpr(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"two tokens", "sqlite serv", `"sqlite"* AND "serv"*`},
		{"empty query", "", ""},
		{"punctuation only", "!!! ---", ""},
		{"single token", "sqlite", `"sqlite"*`},
		{"fts keyword", "and near", `"and"* AND "near"*`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := MatchExpr(c.in)
			if got != c.want {
				t.Errorf("MatchExpr(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
