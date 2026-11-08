package discover

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// Search query bounds. Input beyond them is dropped, never an error.
const (
	// MaxQueryRunes is the longest query kept (the search box allows 200).
	MaxQueryRunes = 200
	// MaxTerms is how many distinct words a query matches on.
	MaxTerms = 8
	// MaxTermRunes truncates very long words.
	MaxTermRunes = 40
	// maxCandidates bounds the series IDs one search considers.
	maxCandidates = 300
)

// Query is a parsed search: the trimmed text and its words. Words contain
// only letters, digits and combining marks, lowercased, so they can never
// carry FTS5 or tsquery operators, quotes or SQL.
type Query struct {
	Text  string
	Terms []string
}

// ParseQuery bounds and tokenises raw search input.
func ParseQuery(raw string) Query {
	raw = strings.ToValidUTF8(strings.TrimSpace(raw), "")
	if utf8.RuneCountInString(raw) > MaxQueryRunes {
		raw = string([]rune(raw)[:MaxQueryRunes])
	}
	q := Query{Text: raw}
	seen := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(raw), func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r))
	}) {
		if r := []rune(w); len(r) > MaxTermRunes {
			w = string(r[:MaxTermRunes])
		}
		if seen[w] {
			continue
		}
		seen[w] = true
		q.Terms = append(q.Terms, w)
		if len(q.Terms) == MaxTerms {
			break
		}
	}
	return q
}

// Empty reports whether the query has no words (browse mode).
func (q Query) Empty() bool { return len(q.Terms) == 0 }

// matchTerms are the words full-text search matches on: any of them, ranked
// so series matching more words come first (the artboard lists "keeper"
// matches for "lighthouse keeper"). A one-letter Latin word next to longer
// words is skipped, since as a prefix it would match nearly everything.
func (q Query) matchTerms() []string {
	if len(q.Terms) < 2 {
		return q.Terms
	}
	var out []string
	for _, t := range q.Terms {
		if utf8.RuneCountInString(t) == 1 && t[0] < utf8.RuneSelf {
			continue
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return q.Terms
	}
	return out
}

// ftsSQLite is the FTS5 MATCH expression: every word as a quoted prefix
// ("word"*), any word matching.
func (q Query) ftsSQLite() string {
	terms := q.matchTerms()
	parts := make([]string, len(terms))
	for i, t := range terms {
		parts[i] = `"` + t + `"*`
	}
	return strings.Join(parts, " OR ")
}

// ftsPostgres is the to_tsquery expression: word:* joined with |.
func (q Query) ftsPostgres() string {
	terms := q.matchTerms()
	parts := make([]string, len(terms))
	for i, t := range terms {
		parts[i] = t + ":*"
	}
	return strings.Join(parts, " | ")
}

// likePattern escapes a word for LIKE … ESCAPE '\'.
func likePattern(term string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(term) + "%"
}

// needsSubstring reports whether a word should also be matched inside longer
// runs: CJK text has no spaces, so both full-text indexes see a whole phrase
// as one token and only match it from its start.
func needsSubstring(term string) bool {
	for _, r := range term {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			return true
		}
	}
	return false
}

// listedVisibilities narrows candidates before publish.Service decides
// access: only public and members series are ever listed.
const candidateFilter = `s.status <> 'draft' AND s.published_at IS NOT NULL AND s.visibility IN ('public', 'members')`

// candidates returns series IDs matching q, best first: full-text matches of
// any word over title, synopsis, tags and genres (the 0006 index, ranked by
// bm25 or ts_rank), then series whose creator's name contains every word,
// then substring matches for CJK words. The caller
// filters them through publish.Service.ListSeries; nothing here decides
// what a viewer may see.
func candidates(ctx context.Context, d *db.DB, q Query) ([]string, error) {
	if q.Empty() {
		return nil, nil
	}
	var ranked []string
	var err error
	if d.Dialect() == db.Postgres {
		const doc = `to_tsvector('simple', s.title || ' ' || s.synopsis || ' ' || s.tags || ' ' || s.genres)`
		err = d.Select(ctx, &ranked, `SELECT s.id FROM series s WHERE `+doc+` @@ to_tsquery('simple', ?) AND `+candidateFilter+`
			ORDER BY ts_rank(`+doc+`, to_tsquery('simple', ?)) DESC, s.published_at DESC, s.id DESC LIMIT ?`,
			q.ftsPostgres(), q.ftsPostgres(), maxCandidates)
	} else {
		err = d.Select(ctx, &ranked, `SELECT s.id FROM series_fts f JOIN series s ON s.rowid = f.rowid
			WHERE series_fts MATCH ? AND `+candidateFilter+` ORDER BY f.rank, s.published_at DESC, s.id DESC LIMIT ?`,
			q.ftsSQLite(), maxCandidates)
	}
	if err != nil {
		return nil, fmt.Errorf("discover: full-text search: %w", err)
	}

	creators, err := creatorMatches(ctx, d, q)
	if err != nil {
		return nil, err
	}
	ranked = append(ranked, creators...)

	var sub []string
	for _, t := range q.Terms {
		if needsSubstring(t) {
			sub = append(sub, t)
		}
	}
	if len(sub) > 0 {
		where, args := []string{}, []any{}
		for _, t := range sub {
			where = append(where, `(LOWER(s.title) LIKE ? ESCAPE '\' OR LOWER(s.synopsis) LIKE ? ESCAPE '\' OR LOWER(s.tags) LIKE ? ESCAPE '\')`)
			p := likePattern(t)
			args = append(args, p, p, p)
		}
		var ids []string
		if err := d.Select(ctx, &ids, `SELECT s.id FROM series s WHERE (`+strings.Join(where, " OR ")+`) AND `+candidateFilter+`
			ORDER BY s.published_at DESC, s.id DESC LIMIT ?`, append(args, maxCandidates)...); err != nil {
			return nil, fmt.Errorf("discover: substring search: %w", err)
		}
		ranked = append(ranked, ids...)
	}
	return dedupe(ranked, maxCandidates), nil
}

// creatorMatches finds series whose credited creators' names contain every
// word (credit name, display name or username).
func creatorMatches(ctx context.Context, d *db.DB, q Query) ([]string, error) {
	where, args := []string{}, []any{}
	for _, t := range q.Terms {
		where = append(where, `(LOWER(sc.credit_name) LIKE ? ESCAPE '\' OR LOWER(u.display_name) LIKE ? ESCAPE '\' OR LOWER(u.username) LIKE ? ESCAPE '\')`)
		p := likePattern(t)
		args = append(args, p, p, p)
	}
	var ids []string
	if err := d.Select(ctx, &ids, `SELECT s.id FROM series s WHERE `+candidateFilter+` AND EXISTS (
			SELECT 1 FROM series_creators sc JOIN users u ON u.id = sc.user_id
			WHERE sc.series_id = s.id AND `+strings.Join(where, " AND ")+`)
		ORDER BY s.published_at DESC, s.id DESC LIMIT ?`, append(args, maxCandidates)...); err != nil {
		return nil, fmt.Errorf("discover: creator search: %w", err)
	}
	return ids, nil
}

// creatorNameMatches lists the user IDs among owners whose name contains
// every word of q.
func creatorNameMatches(q Query, names map[string]creator) map[string]bool {
	out := map[string]bool{}
	for id, c := range names {
		hay := strings.ToLower(c.Name + " " + c.Username)
		ok := true
		for _, t := range q.Terms {
			if !strings.Contains(hay, t) {
				ok = false
				break
			}
		}
		if ok {
			out[id] = true
		}
	}
	return out
}

func dedupe(ids []string, limit int) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
		if len(out) == limit {
			break
		}
	}
	return out
}
