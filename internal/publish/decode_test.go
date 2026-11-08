package publish_test

import (
	"context"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestSeriesFieldsDecode(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture) {
		ctx := context.Background()
		svc := f.Service()
		f.SetMode(settings.ModePublic)
		links := []publish.SupportLink{{Label: "Studio site", URL: "https://example.org/tidebreak"}}
		label := map[string]string{"script": "human", "art": "ai_assisted", "lettering": "ai"}
		s := f.ReadableSeries(publishtest.SeriesSpec{Title: "Paper Crows", Synopsis: "Messenger crows carry secrets.",
			Genres: []string{"fantasy", "mystery"}, Tags: []string{"crows"}, Format: "webtoon", AgeRating: publish.RatingTeen,
			Licence: "cc-by", AILabel: label, SupportLinks: links, OriginalLang: "ja", Languages: []string{"ja", "en"},
			Cover: true, FollowerCount: 12, ViewCount: 3400, Schedule: "Weekly, Saturday", OwnerCredit: "J. Okafor"})
		got, err := svc.SeriesBySlug(ctx, publish.Viewer{}, s.Slug)
		mustOK(t, "SeriesBySlug", err)
		want := publish.Series{ID: s.ID, ProjectID: s.ProjectID, OwnerID: s.Owner.ID, Slug: s.Slug, Title: "Paper Crows",
			Synopsis: "Messenger crows carry secrets.", Genres: []string{"fantasy", "mystery"}, Tags: []string{"crows"},
			Format: "webtoon", AgeRating: publish.RatingTeen, Licence: "cc-by", Visibility: publish.VisibilityPublic,
			CoverAssetID: s.CoverAssetID, SupportLinks: links, AILabel: label, OriginalLang: "ja", Languages: []string{"ja", "en"},
			Status: publish.SeriesOngoing, FollowerCount: 12, ViewCount: 3400, Schedule: "Weekly, Saturday",
			Creators: []publish.Creator{{UserID: s.Owner.ID, Role: "owner", CreditName: "J. Okafor"}}}
		if got.PublishedAt == nil || !got.PublishedAt.Equal(*s.PublishedAt) || got.UpdatedAt.IsZero() {
			t.Fatalf("times: published %v (want %v), updated %v", got.PublishedAt, s.PublishedAt, got.UpdatedAt)
		}
		got.PublishedAt, got.UpdatedAt = nil, want.UpdatedAt
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("series =\n%+v\nwant\n%+v", got, want)
		}
		if s.CoverAssetID == "" {
			t.Fatal("cover asset was not created")
		}
	})
}

func TestJSONColumnsDecodeLeniently(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture) {
		ctx := context.Background()
		svc := f.Service()
		f.SetMode(settings.ModePublic)
		s := f.Series(publishtest.SeriesSpec{})
		p := f.Publish(s, publishtest.PublicationSpec{Pages: 2})
		exec := func(q string, args ...any) {
			t.Helper()
			if _, err := ta.Deps.DB.Exec(ctx, q, args...); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		exec(`UPDATE series SET genres = ?, tags = ?, support_links = ?, ai_label = ?, languages = ? WHERE id = ?`,
			`not json`,
			`["b", 3, " ", "b", " a ", null, {"x":1}]`,
			`[{"label":"Shop","url":"javascript:alert(1)"},{"label":" Site ","url":" https://example.org/x "},{"url":"/relative"},
			  "bad",{"label":"FTP","url":"ftp://example.org"},{"url":"https://user:pw@example.org"},{"url":"http://example.net"},
			  {"label":5,"url":"https://example.com"}]`,
			`{"script":"human","art":3,"lettering":" ai ","":"x","extra":null}`,
			``, s.ID)
		exec(`UPDATE chapter_publications SET ai_label = ? WHERE id = ?`, `{"art":`, p.ID)
		exec(`UPDATE publication_pages SET panels = ? WHERE id = ?`, `[
			{"order":2,"alt_text":"second","points":[{"x":1,"y":2}],"x":1,"y":2,"w":3,"h":4,
			 "bubbles":[{"order":2,"kind":"sfx","text":"BOOM"},{"order":1,"kind":"speech","speaker":"Old Wren","text":"Down!"}]},
			"junk",
			{"order":"x"},
			{"order":1,"alt_text":"first"}
		]`, p.Pages[0].ID)
		exec(`UPDATE publication_pages SET panels = ? WHERE id = ?`, `{"not":"a list"}`, p.Pages[1].ID)

		got, err := svc.SeriesByID(ctx, publish.Viewer{}, s.ID)
		mustOK(t, "SeriesByID", err)
		if got.Genres == nil || len(got.Genres) != 0 {
			t.Errorf("genres = %#v, want empty", got.Genres)
		}
		if !slices.Equal(got.Tags, []string{"b", "a"}) {
			t.Errorf("tags = %#v", got.Tags)
		}
		wantLinks := []publish.SupportLink{{Label: "Site", URL: "https://example.org/x"}, {URL: "http://example.net"}}
		if !reflect.DeepEqual(got.SupportLinks, wantLinks) {
			t.Errorf("support links = %#v, want %#v", got.SupportLinks, wantLinks)
		}
		if want := map[string]string{"script": "human", "lettering": "ai"}; !maps.Equal(got.AILabel, want) {
			t.Errorf("ai label = %#v, want %#v", got.AILabel, want)
		}
		if got.Languages == nil || len(got.Languages) != 0 {
			t.Errorf("languages = %#v, want empty", got.Languages)
		}
		b, _ := json.Marshal(got)
		for _, field := range []string{`"genres":[]`, `"languages":[]`, `"support_links":[{`} {
			if !strings.Contains(string(b), field) {
				t.Errorf("JSON %s lacks %s", b, field)
			}
		}

		pub, pages, err := svc.Publication(ctx, publish.Viewer{}, s.ID, p.Number, "")
		mustOK(t, "Publication", err)
		if pub.AILabel == nil || len(pub.AILabel) != 0 {
			t.Errorf("publication ai label = %#v, want empty", pub.AILabel)
		}
		if len(pages) != 2 {
			t.Fatalf("pages = %d", len(pages))
		}
		panels := pages[0].Panels
		if len(panels) != 2 || panels[0].AltText != "first" || panels[1].AltText != "second" {
			t.Fatalf("panels = %+v", panels)
		}
		if panels[0].Points == nil || panels[0].Bubbles == nil {
			t.Errorf("missing lists are not empty: %+v", panels[0])
		}
		if bs := panels[1].Bubbles; len(bs) != 2 || bs[0].Text != "Down!" || bs[0].Speaker != "Old Wren" || bs[1].Text != "BOOM" {
			t.Errorf("bubbles not in reading order: %+v", bs)
		}
		if want := []pagedoc.Point{{X: 1, Y: 2}}; !slices.Equal(panels[1].Points, want) || panels[1].W != 3 || panels[1].H != 4 {
			t.Errorf("geometry = %+v", panels[1])
		}
		if pages[1].Panels == nil || len(pages[1].Panels) != 0 {
			t.Errorf("malformed panels = %#v, want empty", pages[1].Panels)
		}
	})
}
