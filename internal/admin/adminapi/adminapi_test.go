package adminapi

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

func TestBlockedWholeWordsIgnoringCase(t *testing.T) {
	words := []string{"Gloomweed", "salt fog", "鉄"}
	cases := []struct {
		text string
		want string
	}{
		{"A gloomweed harvest", "Gloomweed"},
		{"GLOOMWEED!", "Gloomweed"},
		{"(gloomweed)", "Gloomweed"},
		{"gloomweeds everywhere", ""}, // not a whole word
		{"ungloomweed", ""},           // not a whole word
		{"the salt   fog rolls in", "salt fog"},
		{"the saltfog rolls in", ""},
		{"ＧＬＯＯＭＷＥＥＤ", "Gloomweed"}, // full-width forms
		{"鉄の鯉", "鉄"},               // no spaces in Japanese: any position is a boundary
		{"Harbour ghosts", ""},
		{"", ""},
	}
	for _, c := range cases {
		got, ok := Blocked(words, c.text)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("Blocked(%q) = %q, %v; want %q", c.text, got, ok, c.want)
		}
	}
	if _, ok := Blocked(nil, "anything"); ok {
		t.Error("no words blocks nothing")
	}
	if w, ok := BlockedAny(words, "fine title", "a synopsis with gloomweed"); !ok || w != "Gloomweed" {
		t.Errorf("BlockedAny = %q, %v", w, ok)
	}
}

func TestParseBlockedWords(t *testing.T) {
	got, err := ParseBlockedWords("gloomweed\n  salt   fog , Gloomweed\r\n\n鉄、錆")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"gloomweed", "salt fog", "鉄", "錆"}; !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if _, err := ParseBlockedWords(strings.Repeat("x", MaxBlockedWordRunes+1)); !errors.Is(err, ErrWordTooLong) {
		t.Fatalf("long word: %v", err)
	}
	many := make([]string, MaxBlockedWords+1)
	for i := range many {
		many[i] = "w" + strings.Repeat("a", i%50) + string(rune('a'+i%26)) + string(rune('a'+i/26%26))
	}
	if _, err := ParseBlockedWords(strings.Join(many, "\n")); !errors.Is(err, ErrTooManyWords) {
		t.Fatalf("too many: %v", err)
	}
}

func TestRatings(t *testing.T) {
	off := settings.Content{DefaultAgeRating: RatingAdult}
	for _, r := range []string{RatingAll, RatingTeen} {
		if !RatingAllowed(off, r) {
			t.Errorf("%s must always be allowed", r)
		}
	}
	if RatingAllowed(off, RatingMature) || RatingAllowed(off, RatingAdult) || RatingAllowed(off, "unknown") {
		t.Error("mature and adult need their switches")
	}
	if got := DisallowedRatings(off); !slices.Equal(got, []string{RatingMature, RatingAdult}) {
		t.Errorf("disallowed = %v", got)
	}
	if DefaultRating(off) != RatingAll {
		t.Error("a disallowed default falls back to all ages")
	}
	on := settings.Content{AllowMature: true, AllowAdult: true, DefaultAgeRating: RatingMature}
	if !RatingAllowed(on, RatingAdult) || len(DisallowedRatings(on)) != 0 || DefaultRating(on) != RatingMature {
		t.Error("switches on allow mature and adult")
	}
	if TakedownKey(TakedownChapter, "c1") != "admin.takedown.chapter.c1" || TakedownKey(TakedownSeries, "s1") != "admin.takedown.series.s1" {
		t.Error("takedown keys")
	}
}
