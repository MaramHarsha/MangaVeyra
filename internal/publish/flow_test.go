package publish_test

import (
	"bytes"
	"context"
	"image/png"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/compose"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestPublishFreezesChapterInEachLanguage(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, pf *publishtest.Fixture) {
		ctx := context.Background()
		pf.SetMode(settings.ModePublic)
		f := newChapter(t, ta, false)
		svc := pf.Service()
		renderer := core.MustUse[compose.Renderer](ta.Deps)
		store := assets.From(ta.Deps)

		page := f.client.Get(f.base + "/publish")
		if page.StatusCode != http.StatusOK {
			t.Fatalf("E1 GET = %d %s", page.StatusCode, page.Body)
		}
		for _, want := range []string{"Publish chapter 1", "Tidebreak Courier · The Lighthouse Debt · 2 pages", "Choose from pages",
			"Generate cover", "AI label shown to readers", "Visibility", "Publish now", "Alt-text",
			"Rin sprints along the seawall. Setting: Sea Gate, dawn. Characters: Rin Asagiri. Rin Asagiri: “Keep moving!” Sound effect: SPLASH"} {
			if !bytes.Contains(page.Body, []byte(want)) {
				t.Fatalf("E1 lacks %q", want)
			}
		}

		for _, lang := range []string{"en", "ja"} {
			j := f.publish(t, f.publishForm(lang))
			if j.State != jobs.StateDone {
				t.Fatalf("%s publish job: %s %+v", lang, j.State, j.Error)
			}
			s, err := svc.SeriesByID(ctx, publish.Viewer{}, seriesID(t, ta, f.project.ID))
			if err != nil {
				t.Fatalf("%s: series not readable: %v", lang, err)
			}
			if s.AgeRating != publish.RatingTeen || s.Status != publish.SeriesOngoing || s.Title != "Tidebreak Courier" || !slices.Contains(s.Languages, lang) {
				t.Fatalf("%s: series = %+v", lang, s)
			}
			pub, pages, err := svc.Publication(ctx, publish.Viewer{}, s.ID, 1, lang)
			if err != nil {
				t.Fatalf("%s: Publication: %v", lang, err)
			}
			if pub.Title != "The Lighthouse Debt" || pub.PageCount != 2 || len(pages) != 2 || pub.ChapterID != f.chapter.ID ||
				pub.AILabel["script"] != "ai" || pub.AILabel["art"] != "human" {
				t.Fatalf("%s: publication %+v, %d pages", lang, pub, len(pages))
			}
			for i, fp := range pages {
				doc := f.pages[i]
				// The frozen image is exactly the page rendered in that language.
				data, a, err := store.Read(ctx, fp.AssetID, 0)
				if err != nil {
					t.Fatalf("%s page %d asset: %v", lang, i+1, err)
				}
				want, err := renderer.PNG(ctx, doc, compose.Options{Lang: lang, Scale: 1})
				if err != nil || !bytes.Equal(data, want) {
					t.Fatalf("%s page %d image differs from the %s render (%v)", lang, i+1, lang, err)
				}
				img, err := png.Decode(bytes.NewReader(data))
				if err != nil || img.Bounds().Dx() != pageW || img.Bounds().Dy() != pageH || fp.Width != pageW || fp.Height != pageH ||
					a.Kind != "publication_page" || a.ContentType != "image/png" {
					t.Fatalf("%s page %d: %v %+v", lang, i+1, err, a)
				}
				if fp.Number != i+1 || len(fp.Panels) != 2 {
					t.Fatalf("%s page %d guided: %+v", lang, i+1, fp)
				}
				for k, gp := range fp.Panels {
					r := pagedoc.Bounds(doc.Panels[k].Points)
					if gp.Order != k+1 || !reflect.DeepEqual(gp.Points, doc.Panels[k].Points) || gp.X != r.X || gp.Y != r.Y || gp.W != r.W || gp.H != r.H {
						t.Fatalf("%s page %d panel %d geometry %+v, want %+v", lang, i+1, k, gp, doc.Panels[k].Points)
					}
				}
				top, bottom := fp.Panels[0].Bubbles, fp.Panels[1].Bubbles
				wantTop := []publish.GuidedBubble{{Order: 1, Kind: "speech", Speaker: "Rin Asagiri", Text: "Keep moving!"}, {Order: 2, Kind: "sfx", Text: "SPLASH"}}
				wantBottom := []publish.GuidedBubble{{Order: 1, Kind: "caption", Text: "Dawn at the Sea Gate."}}
				if lang == "ja" {
					wantTop[0].Text, wantTop[1].Text, wantBottom[0].Text = "進め！", "ザバーン", "海門の夜明け。"
				}
				if !reflect.DeepEqual(top, wantTop) || !reflect.DeepEqual(bottom, wantBottom) {
					t.Fatalf("%s page %d bubbles = %+v / %+v", lang, i+1, top, bottom)
				}
				if alt := fp.Panels[1].AltText; !strings.Contains(alt, "Kaito") || fp.AltText == "" {
					t.Fatalf("%s page %d alt-text %q / %q", lang, i+1, alt, fp.AltText)
				}
			}
		}
		// Japanese alt-text uses Japanese connecting words.
		_, ja, _ := svc.Publication(ctx, publish.Viewer{}, seriesID(t, ta, f.project.ID), 1, "ja")
		if alt := ja[0].Panels[0].AltText; !strings.Contains(alt, "場所：Sea Gate、dawn。") || !strings.Contains(alt, "Rin Asagiri「進め！」") || !strings.Contains(alt, "効果音：ザバーン") {
			t.Fatalf("ja alt-text %q", alt)
		}

		// Published page (E1b).
		live := f.client.Get(f.base + "/published?lang=en")
		if live.StatusCode != http.StatusOK {
			t.Fatalf("published = %d", live.StatusCode)
		}
		for _, want := range []string{"DOOON!", "Chapter 1 is live", "English", "View series page", "Quests and XP"} {
			if !bytes.Contains(live.Body, []byte(want)) {
				t.Fatalf("published page lacks %q", want)
			}
		}

		// Readers' copies stay frozen while the creator keeps editing.
		exec(t, ta, `UPDATE bubble_texts SET text = 'Changed!' WHERE lang = 'en'`)
		_, again, err := svc.Publication(ctx, publish.Viewer{}, seriesID(t, ta, f.project.ID), 1, "en")
		if err != nil || again[0].Panels[0].Bubbles[0].Text != "Keep moving!" {
			t.Fatalf("frozen copy changed: %+v %v", again, err)
		}
	})
}

func seriesID(t *testing.T, ta *testutil.TestApp, projectID string) string {
	t.Helper()
	var id string
	if err := ta.Deps.DB.Get(context.Background(), &id, `SELECT id FROM series WHERE project_id = ?`, projectID); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPublishRefusesUnletteredLanguage(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, pf *publishtest.Fixture) {
		f := newChapter(t, ta, true)
		page := f.client.Get(f.base + "/publish?lang=ja")
		if !bytes.Contains(page.Body, []byte("Japanese · 5 bubbles not lettered")) {
			t.Fatalf("missing-language option not shown")
		}
		resp := postForm(f.client, f.base+"/publish", f.publishForm("ja"))
		if resp.StatusCode != http.StatusUnprocessableEntity || !bytes.Contains(resp.Body, []byte("Some bubbles have no text in this language")) {
			t.Fatalf("unlettered publish = %d", resp.StatusCode)
		}
		var n int
		if err := ta.Deps.DB.Get(context.Background(), &n, `SELECT COUNT(*) FROM jobs WHERE type = ?`, publish.JobType); err != nil || n != 0 {
			t.Fatalf("jobs queued = %d %v", n, err)
		}
		// The AI label can be raised, never lowered below the recorded history (script is AI here).
		resp = postForm(f.client, f.base+"/publish", f.publishForm("en", "ai_script", "human"))
		if resp.StatusCode != http.StatusUnprocessableEntity || !bytes.Contains(resp.Body, []byte("never less")) {
			t.Fatalf("lowered AI label = %d", resp.StatusCode)
		}
		// A schedule must be in the future.
		resp = postForm(f.client, f.base+"/publish", f.publishForm("en", "when", "schedule", "date", "2020-01-01", "time", "10:00"))
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("past schedule = %d", resp.StatusCode)
		}
		_ = time.Second
	})
}
