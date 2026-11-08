// Package adminapi is the small, dependency-free part of the admin module that
// other modules use (ADR 0013 §1–§2): the content rules (age ratings the
// server allows, blocked words), the instance-settings keys that record a
// takedown, and the optional hook the update module provides for the G5 header.
//
// It imports only internal/settings, so publish and social can enforce the
// rules without importing the admin module (which imports them).
package adminapi

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

// Age ratings, as stored on series (publish.Rating*).
const (
	RatingAll    = "all"
	RatingTeen   = "teen"
	RatingMature = "mature"
	RatingAdult  = "adult"
)

// RatingAllowed reports whether the content rules let a series carry rating.
// All-ages and teen series are always allowed; mature and adult series need
// their switch. Unknown ratings are refused.
func RatingAllowed(c settings.Content, rating string) bool {
	switch rating {
	case RatingAll, RatingTeen:
		return true
	case RatingMature:
		return c.AllowMature
	case RatingAdult:
		return c.AllowAdult
	}
	return false
}

// DisallowedRatings lists the ratings the content rules do not allow (empty
// when mature and adult series are both allowed).
func DisallowedRatings(c settings.Content) []string {
	var out []string
	if !c.AllowMature {
		out = append(out, RatingMature)
	}
	if !c.AllowAdult {
		out = append(out, RatingAdult)
	}
	return out
}

// DefaultRating is the rating a new series starts with: the admin's default
// when the rules allow it, otherwise all ages.
func DefaultRating(c settings.Content) string {
	if c.DefaultAgeRating != "" && RatingAllowed(c, c.DefaultAgeRating) {
		return c.DefaultAgeRating
	}
	return RatingAll
}

// ---- blocked words --------------------------------------------------------

// Limits for the blocked words list.
const (
	MaxBlockedWords     = 500
	MaxBlockedWordRunes = 64
)

// Errors returned by ParseBlockedWords.
var (
	ErrTooManyWords = errors.New("adminapi: too many blocked words")
	ErrWordTooLong  = errors.New("adminapi: blocked word too long")
)

// ParseBlockedWords reads the admin's list: one word or phrase per line (or
// separated by commas), trimmed, inner spaces collapsed, duplicates removed
// case-insensitively, in the order given.
func ParseBlockedWords(raw string) ([]string, error) {
	split := strings.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' || r == '、' || r == '，' })
	out := make([]string, 0, len(split))
	seen := map[string]bool{}
	for _, w := range split {
		w = strings.Join(strings.Fields(w), " ")
		if w == "" {
			continue
		}
		if utf8.RuneCountInString(w) > MaxBlockedWordRunes {
			return nil, ErrWordTooLong
		}
		k := fold(w)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, w)
	}
	if len(out) > MaxBlockedWords {
		return nil, ErrTooManyWords
	}
	return out, nil
}

// Blocked returns the first entry of words that text contains as a whole word
// or phrase, ignoring case, full-width forms and runs of white space. A match
// counts as whole when the characters around it are not letters or digits;
// next to Chinese, Japanese and other scripts written without spaces any
// position is a word boundary, so a blocked word inside a Japanese sentence
// still matches.
func Blocked(words []string, text string) (string, bool) {
	if len(words) == 0 || strings.TrimSpace(text) == "" {
		return "", false
	}
	hay := fold(text)
	for _, w := range words {
		needle := fold(w)
		if needle == "" {
			continue
		}
		if containsWord(hay, needle) {
			return w, true
		}
	}
	return "", false
}

// BlockedAny returns the first blocked entry found in any of texts.
func BlockedAny(words []string, texts ...string) (string, bool) {
	for _, t := range texts {
		if w, ok := Blocked(words, t); ok {
			return w, true
		}
	}
	return "", false
}

// fold lowercases s, maps full-width ASCII to ASCII and collapses white space.
func fold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		if r >= 0xFF01 && r <= 0xFF5E {
			r -= 0xFEE0
		} else if r == 0x3000 {
			r = ' '
		}
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

func containsWord(hay, needle string) bool {
	first, _ := utf8.DecodeRuneInString(needle)
	last, _ := utf8.DecodeLastRuneInString(needle)
	for from := 0; from <= len(hay)-len(needle); {
		i := strings.Index(hay[from:], needle)
		if i < 0 {
			return false
		}
		start := from + i
		end := start + len(needle)
		before, _ := utf8.DecodeLastRuneInString(hay[:start])
		after, _ := utf8.DecodeRuneInString(hay[end:])
		if boundary(before, first, start == 0) && boundary(after, last, end == len(hay)) {
			return true
		}
		_, size := utf8.DecodeRuneInString(hay[start:])
		from = start + size
	}
	return false
}

// boundary reports whether neighbour (the rune next to a match whose edge
// rune is edge) separates words.
func boundary(neighbour, edge rune, atEnd bool) bool {
	if atEnd || !wordRune(neighbour) || !wordRune(edge) {
		return true
	}
	return spaceless(neighbour) || spaceless(edge)
}

func wordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) }

func spaceless(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Thai, unicode.Lao, unicode.Khmer,
		unicode.Myanmar, unicode.Tibetan)
}

// ---- takedowns --------------------------------------------------------------

// A takedown is recorded as one module-owned instance setting (settings
// GetKey/SetKey contract) per taken-down series or chapter. publish.Service
// hides content whose key exists from everyone outside its project; lifting the
// takedown deletes the key. Nothing else is changed, so nothing is lost.
const (
	TakedownPrefix        = "admin.takedown."
	SeriesTakedownPrefix  = TakedownPrefix + "series."
	ChapterTakedownPrefix = TakedownPrefix + "chapter."
)

// Takedown target kinds.
const (
	TakedownSeries  = "series"
	TakedownChapter = "chapter"
)

// TakedownKey is the instance-settings key of a takedown. A chapter takedown
// names the Studio chapter, so every language of it is hidden.
func TakedownKey(kind, id string) string {
	if kind == TakedownChapter {
		return ChapterTakedownPrefix + id
	}
	return SeriesTakedownPrefix + id
}

// Takedown is the value stored under a takedown key.
type Takedown struct {
	Kind     string `json:"kind"`
	TargetID string `json:"target_id"`
	SeriesID string `json:"series_id"`
	ReportID string `json:"report_id,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Note     string `json:"note,omitempty"`
	By       string `json:"by"`
	At       string `json:"at"`
}

// ---- update hook ------------------------------------------------------------

// UpdateNotice is optional: the update module provides it with
// core.Provide[adminapi.UpdateNotice] so the G5 header can offer
// "Update to x". Without it the header links to /admin/update (shown as Soon
// until that page exists).
type UpdateNotice interface {
	// AvailableUpdate returns the version an update would install, if one is known.
	AvailableUpdate(ctx context.Context) (version string, ok bool)
}

// ---- maintenance ownership ----------------------------------------------------

// Operations that can own maintenance mode (MaintenanceHeldError.What).
const (
	HoldUpdate  = "update"
	HoldRestore = "restore"
)

// MaintenanceGuard is optional: the backup module provides it with
// core.Provide[adminapi.MaintenanceGuard]. An update and a restore turn
// maintenance mode on and own it while they run (ADR 0013 §3): the G5 toggle
// stores the admin's setting through the guard, which refuses while one of
// them runs, in the same transaction that writes the setting.
type MaintenanceGuard interface {
	// SetMaintenance stores m as the maintenance setting, or returns a
	// *MaintenanceHeldError naming the operation that owns maintenance mode.
	SetMaintenance(ctx context.Context, by string, m settings.Maintenance) error
}

// MaintenanceHeldError refuses a maintenance change while an update or a
// restore owns maintenance mode.
type MaintenanceHeldError struct {
	// What is HoldUpdate or HoldRestore.
	What string
	// Since is when the operation took maintenance mode.
	Since time.Time
}

func (e *MaintenanceHeldError) Error() string {
	return "adminapi: maintenance mode is held by a running " + e.What
}
