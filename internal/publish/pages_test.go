package publish_test

import (
	"bytes"
	"context"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestPublicationsByLanguageAndNumber(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture) {
		ctx := context.Background()
		svc := f.Service()
		f.SetMode(settings.ModePublic)
		v := publish.Viewer{}
		s := f.Series(publishtest.SeriesSpec{OriginalLang: "ja"})
		var ja []publishtest.Publication
		for _, n := range []float64{10, 2, 1.5, 1} {
			ja = append(ja, f.Publish(s, publishtest.PublicationSpec{Number: n, Pages: 1}))
		}
		label := map[string]string{"script": "human", "art": "ai", "lettering": "ai_assisted"}
		en1 := f.Publish(s, publishtest.PublicationSpec{ChapterID: ja[3].ChapterID, Lang: "en", Number: 1, Title: "The Courier",
			AILabel: label, Pages: 3, Width: 200, Height: 300})
		f.Publish(s, publishtest.PublicationSpec{ChapterID: ja[2].ChapterID, Lang: "en", Number: 2, Status: publish.PublicationScheduled})

		numbers := func(list []publish.Publication) []float64 {
			var out []float64
			for _, p := range list {
				out = append(out, p.Number)
			}
			return out
		}
		list, err := svc.Publications(ctx, v, s.ID, "")
		mustOK(t, "Publications original", err)
		if got := numbers(list); !slices.Equal(got, []float64{1, 1.5, 2, 10}) {
			t.Fatalf("original language numbers = %v", got)
		}
		for _, p := range list {
			if p.Lang != "ja" || p.Status != publish.PublicationPublished || p.PublishedAt == nil || p.PageCount != 1 || p.SeriesID != s.ID {
				t.Errorf("publication not decoded: %+v", p)
			}
		}
		list, err = svc.Publications(ctx, v, s.ID, " en ")
		mustOK(t, "Publications en", err)
		if len(list) != 1 || list[0].ID != en1.ID {
			t.Fatalf("en publications = %+v", list)
		}
		list, err = svc.Publications(ctx, v, s.ID, "fr")
		if err != nil || list == nil || len(list) != 0 {
			t.Fatalf("fr publications = %#v, %v; want an empty list", list, err)
		}

		pub, pages, err := svc.Publication(ctx, v, s.ID, 1, "en")
		mustOK(t, "Publication", err)
		if pub.ID != en1.ID || pub.ChapterID != ja[3].ChapterID || pub.Title != "The Courier" || pub.PageCount != 3 ||
			!reflect.DeepEqual(pub.AILabel, label) || pub.Visibility != publish.VisibilityPublic {
			t.Fatalf("publication = %+v", pub)
		}
		if len(pages) != 3 {
			t.Fatalf("pages = %d", len(pages))
		}
		for i, pg := range pages {
			want := en1.Pages[i]
			if pg.Number != i+1 || pg.ID != want.ID || pg.AssetID != want.AssetID || pg.PublicationID != en1.ID ||
				pg.Width != 200 || pg.Height != 300 || pg.AltText != publishtest.PageAltText(i+1) {
				t.Errorf("page %d = %+v", i, pg)
			}
			if !reflect.DeepEqual(pg.Panels, publishtest.GuidedPanels(200, 300)) {
				t.Errorf("page %d panels = %+v", i, pg.Panels)
			}
		}
		if _, _, err := svc.Publication(ctx, v, s.ID, 1.5, ""); err != nil {
			t.Errorf("Publication 1.5: %v", err)
		}
		_, _, err = svc.Publication(ctx, v, s.ID, 2, "en")
		wantNotFound(t, "scheduled en chapter", err)
		for _, n := range []float64{math.NaN(), math.Inf(1), 3} {
			_, _, err = svc.Publication(ctx, v, s.ID, n, "")
			wantNotFound(t, "number", err)
		}

		// Pages come back by number, not by insertion order.
		exec(t, ta, `UPDATE publication_pages SET number = number + 10 WHERE publication_id = ?`, en1.ID)
		exec(t, ta, `UPDATE publication_pages SET number = 14 - number WHERE publication_id = ?`, en1.ID)
		_, pages, err = svc.Publication(ctx, v, s.ID, 1, "en")
		mustOK(t, "Publication reordered", err)
		if len(pages) != 3 || pages[0].ID != en1.Pages[2].ID || pages[2].ID != en1.Pages[0].ID || pages[0].Number != 1 {
			t.Fatalf("reordered pages = %+v", pages)
		}

		// Two chapters published under one number: readers get the most recent.
		older := f.Publish(s, publishtest.PublicationSpec{Number: 5, PublishedAt: publishtest.Ago(3 * time.Hour)})
		newer := f.Publish(s, publishtest.PublicationSpec{Number: 5, PublishedAt: publishtest.Ago(2 * time.Hour)})
		got, _, err := svc.Publication(ctx, v, s.ID, 5, "")
		mustOK(t, "Publication 5", err)
		if got.ID != newer.ID {
			t.Errorf("duplicate number returned %s, want newer %s (older %s)", got.ID, newer.ID, older.ID)
		}
	})
}

func TestPageImageURL(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture) {
		ctx := context.Background()
		svc := f.Service()
		f.SetMode(settings.ModePublic)
		s := f.Series(publishtest.SeriesSpec{Cover: true})
		p := f.Publish(s, publishtest.PublicationSpec{Pages: 2})
		other := f.Publish(s, publishtest.PublicationSpec{Pages: 1})
		page := publish.PublicationPage{ID: p.Pages[0].ID, PublicationID: p.ID, AssetID: p.Pages[0].AssetID}
		anon := publish.Viewer{}

		u, err := svc.PageImageURL(ctx, anon, page, 0)
		mustOK(t, "PageImageURL", err)
		if !strings.HasPrefix(u, "/media/") || !strings.Contains(u, "exp=") || !strings.Contains(u, "sig=") {
			t.Fatalf("url = %q", u)
		}
		// The signed URL serves exactly the frozen page image.
		want, _, err := assets.From(ta.Deps).Read(ctx, p.Pages[0].AssetID, 0)
		mustOK(t, "read asset", err)
		resp := ta.NewClient().Get(u)
		if resp.StatusCode != http.StatusOK || !bytes.Equal(resp.Body, want) || resp.Header.Get("Content-Type") != "image/png" {
			t.Fatalf("GET %s = %d %q (%d bytes)", u, resp.StatusCode, resp.Header.Get("Content-Type"), len(resp.Body))
		}
		// Only the page ID matters; the caller's copy may omit the rest.
		if _, err := svc.PageImageURL(ctx, anon, publish.PublicationPage{ID: page.ID}, 0); err != nil {
			t.Errorf("PageImageURL by id: %v", err)
		}

		// URLs are short-lived whatever the caller asks for.
		for ttl, max := range map[time.Duration]time.Duration{
			0:              publish.DefaultPageURLTTL * 5 / 4,
			time.Minute:    time.Minute * 5 / 4,
			24 * time.Hour: publish.MaxPageURLTTL * 5 / 4,
		} {
			u, err := svc.PageImageURL(ctx, anon, page, ttl)
			mustOK(t, "PageImageURL ttl", err)
			if left := time.Until(expiry(t, u)); left <= 0 || left > max+time.Second {
				t.Errorf("ttl %v: URL valid for %v, want at most %v", ttl, left, max)
			}
		}

		// A forged page never signs another asset.
		for name, forged := range map[string]publish.PublicationPage{
			"cover asset":       {ID: page.ID, AssetID: s.CoverAssetID},
			"other page asset":  {ID: page.ID, AssetID: other.Pages[0].AssetID},
			"other publication": {ID: page.ID, PublicationID: other.ID},
			"unknown page":      {ID: "01hzzzzzzzzzzzzzzzzzzzzzzz", PublicationID: p.ID, AssetID: page.AssetID},
		} {
			u, err := svc.PageImageURL(ctx, anon, forged, 0)
			if u != "" {
				t.Errorf("%s: signed %q", name, u)
			}
			wantNotFound(t, name, err)
		}

		// Access is re-checked on every call.
		reader := f.User(auth.RoleReader)
		projectViewer := f.User(auth.RoleReader)
		f.AddMember(s.ProjectID, projectViewer.ID, studio.RoleViewer)
		urlFor := func(v publish.Viewer) error { _, err := svc.PageImageURL(ctx, v, page, 0); return err }
		exec(t, ta, `UPDATE series SET visibility = ? WHERE id = ?`, publish.VisibilityMembers, s.ID)
		wantNotFound(t, "members series, anonymous", urlFor(anon))
		mustOK(t, "members series, member", urlFor(publishtest.Viewer(reader)))
		exec(t, ta, `UPDATE series SET visibility = ? WHERE id = ?`, publish.VisibilityPrivate, s.ID)
		wantNotFound(t, "private series, member", urlFor(publishtest.Viewer(reader)))
		wantNotFound(t, "private series, admin", urlFor(publishtest.Viewer(ta.Admin)))
		mustOK(t, "private series, project member", urlFor(publishtest.Viewer(projectViewer)))
		exec(t, ta, `UPDATE series SET visibility = ?, age_rating = ? WHERE id = ?`, publish.VisibilityPublic, publish.RatingMature, s.ID)
		wantNotFound(t, "mature series, anonymous", urlFor(anon))
		mustOK(t, "mature series, member", urlFor(publishtest.Viewer(reader)))
		exec(t, ta, `UPDATE series SET age_rating = ? WHERE id = ?`, publish.RatingAll, s.ID)
		f.SetMode(settings.ModePrivate)
		wantNotFound(t, "private server, anonymous", urlFor(anon))
		f.SetMode(settings.ModePublic)
		mustOK(t, "restored", urlFor(anon))
		exec(t, ta, `UPDATE chapter_publications SET status = ?, unpublished_at = published_at WHERE id = ?`, publish.PublicationUnpublished, p.ID)
		wantNotFound(t, "unpublished, anonymous", urlFor(anon))
		wantNotFound(t, "unpublished, project member", urlFor(publishtest.Viewer(projectViewer)))
		wantNotFound(t, "unpublished, owner", urlFor(publishtest.Viewer(s.Owner)))

		// A page whose image was deleted has nothing to sign.
		mustOK(t, "other page", func() error {
			_, err := svc.PageImageURL(ctx, anon, publish.PublicationPage{ID: other.Pages[0].ID}, 0)
			return err
		}())
		mustOK(t, "delete asset", assets.From(ta.Deps).Delete(ctx, other.Pages[0].AssetID))
		_, err = svc.PageImageURL(ctx, anon, publish.PublicationPage{ID: other.Pages[0].ID}, 0)
		wantNotFound(t, "deleted asset", err)
	})
}

func exec(t *testing.T, ta *testutil.TestApp, q string, args ...any) {
	t.Helper()
	if _, err := ta.Deps.DB.Exec(context.Background(), q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func expiry(t *testing.T, raw string) time.Time {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	exp, err := strconv.ParseInt(u.Query().Get("exp"), 10, 64)
	if err != nil {
		t.Fatalf("exp in %q: %v", raw, err)
	}
	return time.Unix(exp, 0)
}
