package discover

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseQueryKeepsOnlyWords(t *testing.T) {
	cases := map[string][]string{
		"":                               nil,
		"   ":                            nil,
		"Lighthouse  KEEPER":             {"lighthouse", "keeper"},
		`" OR 1=1 --`:                    {"or", "1"},
		`'); DROP TABLE series; --`:      {"drop", "table", "series"},
		`title:* NEAR(a b) "x"* ^y`:      {"title", "near", "a", "b", "x", "y"},
		`rin & !kaito | (sora) <-> wren`: {"rin", "kaito", "sora", "wren"},
		"東京の灯台守":                         {"東京の灯台守"},
		"café   naïve":                   {"café", "naïve"},
		"a\x00b\xff":                     {"a", "b"},
		"keeper keeper KEEPER":           {"keeper"},
	}
	for in, want := range cases {
		got := ParseQuery(in).Terms
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ParseQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseQueryBounds(t *testing.T) {
	long := strings.Repeat("ab ", 500)
	q := ParseQuery(long)
	if len([]rune(q.Text)) > MaxQueryRunes {
		t.Errorf("text kept %d runes", len([]rune(q.Text)))
	}
	if len(ParseQuery("a b c d e f g h i j k l").Terms) != MaxTerms {
		t.Error("terms are not capped")
	}
	word := ParseQuery(strings.Repeat("x", 500)).Terms[0]
	if len(word) != MaxTermRunes {
		t.Errorf("term length = %d", len(word))
	}
}

func TestFullTextExpressionsCarryNoOperators(t *testing.T) {
	q := ParseQuery(`lighthouse" OR "keeper:* & a`)
	if got := q.ftsSQLite(); got != `"lighthouse"* OR "or"* OR "keeper"*` {
		t.Errorf("FTS5 = %s", got)
	}
	if got := q.ftsPostgres(); got != `lighthouse:* | or:* | keeper:*` {
		t.Errorf("tsquery = %s", got)
	}
	if got := ParseQuery("a").ftsSQLite(); got != `"a"*` {
		t.Errorf("a lone one-letter word is kept: %s", got)
	}
	if got := likePattern(`50%_off\`); got != `%50\%\_off\\%` {
		t.Errorf("like = %s", got)
	}
}

func TestSnippetMarksWords(t *testing.T) {
	text := "Rin Asagiri runs parcels across a flooded city where the lighthouse keeper owes the city a debt nobody remembers making."
	var b strings.Builder
	var marks []string
	for _, s := range Snippet(text, []string{"keeper"}, 28) {
		b.WriteString(s.Text)
		if s.Mark {
			marks = append(marks, s.Text)
		}
	}
	if got := b.String(); got != "“…city where the lighthouse keeper owes the city a debt…”" {
		t.Errorf("snippet = %q", got)
	}
	if !reflect.DeepEqual(marks, []string{"keeper"}) {
		t.Errorf("marks = %q", marks)
	}
	if s := Snippet("Short tale.", []string{"absent"}, 28); len(s) != 3 || s[1].Text != "Short tale." || s[1].Mark {
		t.Errorf("no-match snippet = %+v", s)
	}
	if Snippet("", []string{"x"}, 28) != nil {
		t.Error("empty synopsis gives no snippet")
	}
}
