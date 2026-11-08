package reader_test

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/analytics"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/reader/agegate"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

var propsRe = regexp.MustCompile(`data-island="reader" data-props="([^"]*)"`)

// props decodes the reader island's boot JSON from a page.
func props(t *testing.T, body []byte) map[string]any {
	t.Helper()
	m := propsRe.FindSubmatch(body)
	if m == nil {
		t.Fatalf("no reader island in page: %.400s", body)
	}
	out := map[string]any{}
	if err := json.Unmarshal([]byte(html.UnescapeString(string(m[1]))), &out); err != nil {
		t.Fatalf("decode props: %v", err)
	}
	return out
}

// Access goes through publish.Service: anonymous visitors read public
// all-ages chapters only, members also read members-only series, private
// series stay limited to project members and mature series need the
// once-per-series confirmation.
func TestReaderAccessMatrix(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		public := e.f.Series(publishtest.SeriesSpec{Title: "Tidebreak Courier"})
		pubCh := e.f.Publish(public, publishtest.PublicationSpec{Pages: 3})
		members := e.f.Series(publishtest.SeriesSpec{Visibility: publish.VisibilityMembers})
		memCh := e.f.Publish(members, publishtest.PublicationSpec{})
		private := e.f.Series(publishtest.SeriesSpec{Visibility: publish.VisibilityPrivate})
		privCh := e.f.Publish(private, publishtest.PublicationSpec{})
		mature := e.f.Series(publishtest.SeriesSpec{AgeRating: publish.RatingMature})
		matCh := e.f.Publish(mature, publishtest.PublicationSpec{})
		scheduled := e.f.Publish(public, publishtest.PublicationSpec{Status: publish.PublicationScheduled})

		anon := e.ta.NewClient()
		wantStatus(t, "anonymous public", anon.Get(chapterPath(public, pubCh)), http.StatusOK)
		wantStatus(t, "anonymous members", anon.Get(chapterPath(members, memCh)), http.StatusNotFound)
		wantStatus(t, "anonymous private", anon.Get(chapterPath(private, privCh)), http.StatusNotFound)
		wantStatus(t, "anonymous mature", anon.Get(chapterPath(mature, matCh)), http.StatusNotFound)
		wantStatus(t, "scheduled chapter", anon.Get(chapterPath(public, scheduled)), http.StatusNotFound)
		wantStatus(t, "unknown series", anon.Get("/s/no-such-series/1"), http.StatusNotFound)
		wantStatus(t, "bad chapter", anon.Get("/s/"+public.Slug+"/abc"), http.StatusNotFound)

		_, member := e.member()
		wantStatus(t, "member public", member.Get(chapterPath(public, pubCh)), http.StatusOK)
		wantStatus(t, "member members", member.Get(chapterPath(members, memCh)), http.StatusOK)
		wantStatus(t, "member private", member.Get(chapterPath(private, privCh)), http.StatusNotFound)

		owner := e.ta.Login(t, private.Owner.Username)
		wantStatus(t, "owner private preview", owner.Get(chapterPath(private, privCh)), http.StatusOK)

		// Mature: the member sees the confirmation step until they confirm.
		gate := member.Get(chapterPath(mature, matCh))
		wantStatus(t, "mature gate", gate, http.StatusOK)
		if strings.Contains(string(gate.Body), `data-island="reader"`) || !strings.Contains(string(gate.Body), agegate.ConfirmPath(mature.Slug)) {
			t.Fatal("mature series did not show the age confirmation")
		}
		if len(matCh.Pages) == 0 || strings.Contains(string(gate.Body), "/media/") {
			t.Fatal("age confirmation leaked page images")
		}
		// The page API and writes stay closed until the confirmation exists.
		wantStatus(t, "mature api before confirm", member.Get("/api/v1/reader/series/"+mature.ID+"/chapters/1"), http.StatusNotFound)
		got := form(member, agegate.ConfirmPath(mature.Slug), url.Values{"next": {chapterPath(mature, matCh)}})
		wantStatus(t, "confirm age", got, http.StatusSeeOther)
		if loc := got.Header.Get("Location"); loc != chapterPath(mature, matCh) {
			t.Fatalf("confirm redirect = %q", loc)
		}
		wantStatus(t, "mature after confirm", member.Get(chapterPath(mature, matCh)), http.StatusOK)
		if !strings.Contains(string(member.Get(chapterPath(mature, matCh)).Body), `data-island="reader"`) {
			t.Fatal("confirmed reader did not get the chapter")
		}
		// A confirmation is per reader and per series.
		_, other := e.member()
		if strings.Contains(string(other.Get(chapterPath(mature, matCh)).Body), `data-island="reader"`) {
			t.Fatal("another member skipped the confirmation")
		}
		// Anonymous visitors cannot confirm, and an open redirect is refused.
		wantStatus(t, "anonymous confirm", form(anon, agegate.ConfirmPath(mature.Slug), url.Values{}), http.StatusUnauthorized)
		evil := form(member, agegate.ConfirmPath(mature.Slug), url.Values{"next": {"https://evil.example/x"}})
		if loc := evil.Header.Get("Location"); strings.Contains(loc, "evil") {
			t.Fatalf("open redirect to %q", loc)
		}

		// The gate service answers the same question for other modules.
		g := core.MustUse[agegate.Gate](e.ta.Deps)
		s, err := e.f.Service().SeriesByID(context.Background(), publishtest.Viewer(nil), public.ID)
		if err != nil {
			t.Fatal(err)
		}
		if need, err := g.Required(context.Background(), publish.Viewer{}, s); err != nil || need {
			t.Fatalf("all-ages series needs confirmation: %v %v", need, err)
		}
	})
}

// On personal and private servers anonymous visitors get the sign-in page,
// never a hint about what exists.
func TestReaderSendsAnonymousToSignInOnClosedServers(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		s := e.f.Series(publishtest.SeriesSpec{})
		ch := e.f.Publish(s, publishtest.PublicationSpec{})
		for _, mode := range []string{settings.ModePersonal, settings.ModePrivate} {
			e.f.SetMode(mode)
			for _, path := range []string{chapterPath(s, ch), "/s/missing-series/1"} {
				got := e.ta.NewClient().Get(path)
				if got.StatusCode != http.StatusSeeOther || !strings.HasPrefix(got.Header.Get("Location"), "/login") {
					t.Fatalf("%s %s: %d %q", mode, path, got.StatusCode, got.Header.Get("Location"))
				}
			}
			_, member := e.member()
			wantStatus(t, mode+" member", member.Get(chapterPath(s, ch)), http.StatusOK)
		}
	})
}

// Without JavaScript the chapter is complete: stacked page images with
// their alt-text and the panels' alt-text and dialogue for screen readers.
func TestReaderPageWorksWithoutJavaScript(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		s := e.f.Series(publishtest.SeriesSpec{Title: "Low Tide Radio", AILabel: map[string]string{"script": "human", "art": "ai_assisted", "lettering": "human"}})
		ch := e.f.Publish(s, publishtest.PublicationSpec{Pages: 3, Title: "The Lighthouse Debt",
			AILabel: map[string]string{"script": "human", "art": "ai_assisted", "lettering": "human"}})
		next := e.f.Publish(s, publishtest.PublicationSpec{})
		got := e.ta.NewClient().Get(chapterPath(s, ch))
		wantStatus(t, "reader", got, http.StatusOK)
		body := string(got.Body)
		if n := strings.Count(body, `class="mv-rd__img"`); n != 3 {
			t.Fatalf("stacked pages = %d, want 3", n)
		}
		for _, want := range []string{
			html.EscapeString(publishtest.PageAltText(1)),
			html.EscapeString("Rin Asagiri sprints along the flooded seawall with a parcel under one arm."),
			html.EscapeString("Rin Asagiri: Keep moving!"),
			"Sound: SPLASH",
			"Ch. 1 · The Lighthouse Debt",
			"Low Tide Radio",
			"AI-assisted art", "Human script",
			`href="` + chapterPath(s, next) + `"`,
			`src="/media/`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("no-JS reader is missing %q", want)
			}
		}
		if got.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("reader page cache-control = %q", got.Header.Get("Cache-Control"))
		}
		p := props(t, got.Body)
		pages, _ := p["pages"].([]any)
		if len(pages) != 3 {
			t.Fatalf("island pages = %d", len(pages))
		}
		first := pages[0].(map[string]any)
		if !strings.HasPrefix(first["src"].(string), "/media/") || len(first["panels"].([]any)) != 2 {
			t.Fatalf("island page = %v", first)
		}
		if p["prefs"].(map[string]any)["direction"] != "rtl" {
			t.Fatalf("manga direction = %v", p["prefs"])
		}
	})
}

// The chapter API refreshes signed URLs through the same access rules.
func TestChapterAPIRefreshesSignedURLs(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		s := e.f.Series(publishtest.SeriesSpec{})
		ch := e.f.Publish(s, publishtest.PublicationSpec{Pages: 2})
		members := e.f.Series(publishtest.SeriesSpec{Visibility: publish.VisibilityMembers})
		e.f.Publish(members, publishtest.PublicationSpec{})
		anon := e.ta.NewClient()
		got := anon.Get("/api/v1/reader/series/" + s.ID + "/chapters/1")
		wantStatus(t, "chapter api", got, http.StatusOK)
		var out struct {
			PublicationID string `json:"publication_id"`
			Pages         []struct {
				Src    string `json:"src"`
				Panels []any  `json:"panels"`
			} `json:"pages"`
		}
		got.JSON(t, &out)
		if out.PublicationID != ch.ID || len(out.Pages) != 2 || !strings.HasPrefix(out.Pages[0].Src, "/media/") {
			t.Fatalf("chapter api = %+v", out)
		}
		img := anon.Get(out.Pages[0].Src)
		wantStatus(t, "signed page image", img, http.StatusOK)
		wantStatus(t, "members chapter api", anon.Get("/api/v1/reader/series/"+members.ID+"/chapters/1"), http.StatusNotFound)
	})
}

// Progress, per-series settings, bookmarks and downloads belong to one
// member: another member's rows are 404, and nothing is written for a
// chapter the member may not read.
func TestReaderRowsAreScopedToTheirOwner(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		s := e.f.Series(publishtest.SeriesSpec{})
		ch := e.f.Publish(s, publishtest.PublicationSpec{Pages: 4})
		private := e.f.Series(publishtest.SeriesSpec{Visibility: publish.VisibilityPrivate})
		privCh := e.f.Publish(private, publishtest.PublicationSpec{})
		ua, a := e.member()
		_, b := e.member()

		// Progress.
		wantStatus(t, "progress", a.SendJSON(http.MethodPut, "/api/v1/reader/progress",
			map[string]any{"series_id": s.ID, "publication_id": ch.ID, "page": 3, "percent": 0.75}), http.StatusOK)
		wantStatus(t, "progress unreadable", a.SendJSON(http.MethodPut, "/api/v1/reader/progress",
			map[string]any{"series_id": private.ID, "publication_id": privCh.ID, "page": 1}), http.StatusNotFound)
		wantStatus(t, "progress mismatched series", a.SendJSON(http.MethodPut, "/api/v1/reader/progress",
			map[string]any{"series_id": s.ID, "publication_id": privCh.ID, "page": 1}), http.StatusNotFound)
		wantStatus(t, "progress invalid", a.SendJSON(http.MethodPut, "/api/v1/reader/progress",
			map[string]any{"series_id": s.ID, "publication_id": ch.ID, "page": 0}), http.StatusUnprocessableEntity)
		wantStatus(t, "anonymous progress", e.ta.NewClient().SendJSON(http.MethodPut, "/api/v1/reader/progress",
			map[string]any{"series_id": s.ID, "publication_id": ch.ID, "page": 1}), http.StatusUnauthorized)
		if p := props(t, a.Get(chapterPath(s, ch)).Body); p["start_page"] != float64(3) {
			t.Fatalf("resume page = %v", p["start_page"])
		}
		if p := props(t, b.Get(chapterPath(s, ch)).Body); p["start_page"] != float64(1) || p["progress"] != nil {
			t.Fatalf("other member sees progress: %v", p["progress"])
		}
		// Completion is sticky.
		a.SendJSON(http.MethodPut, "/api/v1/reader/progress", map[string]any{"series_id": s.ID, "publication_id": ch.ID, "page": 4, "percent": 1, "completed": true})
		got := a.SendJSON(http.MethodPut, "/api/v1/reader/progress", map[string]any{"series_id": s.ID, "publication_id": ch.ID, "page": 1, "percent": 0.25})
		if !got.Map(t)["completed"].(bool) {
			t.Fatal("rereading cleared completion")
		}

		// Per-series settings.
		wantStatus(t, "prefs", a.SendJSON(http.MethodPut, "/api/v1/reader/prefs/"+s.ID,
			map[string]any{"mode": "guided", "direction": "ltr", "background": "paper", "zoom": 1.5}), http.StatusOK)
		wantStatus(t, "prefs invalid", a.SendJSON(http.MethodPut, "/api/v1/reader/prefs/"+s.ID,
			map[string]any{"mode": "sideways", "direction": "ltr", "background": "paper", "zoom": 1}), http.StatusUnprocessableEntity)
		wantStatus(t, "prefs unreadable", a.SendJSON(http.MethodPut, "/api/v1/reader/prefs/"+private.ID,
			map[string]any{"mode": "paged", "direction": "rtl", "background": "ink", "zoom": 1}), http.StatusNotFound)
		pa := props(t, a.Get(chapterPath(s, ch)).Body)["prefs"].(map[string]any)
		if pa["mode"] != "guided" || pa["direction"] != "ltr" || pa["background"] != "paper" || pa["zoom"] != 1.5 {
			t.Fatalf("stored prefs = %v", pa)
		}
		if pb := props(t, b.Get(chapterPath(s, ch)).Body)["prefs"].(map[string]any); pb["mode"] == "guided" {
			t.Fatal("prefs leaked to another member")
		}

		// Bookmarks.
		add := a.PostJSON("/api/v1/reader/bookmarks", map[string]any{"series_id": s.ID, "publication_id": ch.ID, "page": 2, "panel": 1})
		wantStatus(t, "bookmark", add, http.StatusCreated)
		id := add.Map(t)["id"].(string)
		wantStatus(t, "bookmark again", a.PostJSON("/api/v1/reader/bookmarks",
			map[string]any{"series_id": s.ID, "publication_id": ch.ID, "page": 2, "panel": 1}), http.StatusOK)
		wantStatus(t, "bookmark unreadable", a.PostJSON("/api/v1/reader/bookmarks",
			map[string]any{"series_id": private.ID, "publication_id": privCh.ID, "page": 1}), http.StatusNotFound)
		list := b.Get("/api/v1/reader/bookmarks?series=" + s.ID + "&publication=" + ch.ID)
		wantStatus(t, "other bookmarks", list, http.StatusOK)
		if strings.Contains(string(list.Body), id) {
			t.Fatal("another member listed the bookmark")
		}
		wantStatus(t, "delete other's bookmark", b.Do(http.MethodDelete, "/api/v1/reader/bookmarks/"+id, nil, ""), http.StatusNotFound)
		wantStatus(t, "remove other's bookmark form", form(b, "/library/bookmarks/"+id+"/delete", url.Values{}), http.StatusNotFound)
		if page := a.Get("/library/bookmarks"); !strings.Contains(string(page.Body), id) {
			t.Fatal("library does not list the bookmark")
		}
		if page := b.Get("/library/bookmarks"); strings.Contains(string(page.Body), id) {
			t.Fatal("another member's library lists the bookmark")
		}
		wantStatus(t, "delete bookmark", a.Do(http.MethodDelete, "/api/v1/reader/bookmarks/"+id, nil, ""), http.StatusNoContent)
		wantStatus(t, "delete bookmark twice", a.Do(http.MethodDelete, "/api/v1/reader/bookmarks/"+id, nil, ""), http.StatusNotFound)

		// Downloads.
		start := a.PostJSON("/api/v1/reader/downloads", map[string]any{"series_id": s.ID, "publication_id": ch.ID})
		wantStatus(t, "start download", start, http.StatusOK)
		var man struct {
			Download struct {
				ID        string `json:"id"`
				Status    string `json:"status"`
				ExpiresAt int64  `json:"expires_at"`
			} `json:"download"`
			Owner string `json:"owner"`
			Cache string `json:"cache"`
			Pages []struct {
				Src string `json:"src"`
			} `json:"pages"`
			ReaderURL string `json:"reader_url"`
		}
		start.JSON(t, &man)
		if man.Owner != ua.ID || man.Cache != "mv-offline-"+ch.ID || man.Download.Status != "pending" || len(man.Pages) != 4 || man.ReaderURL != chapterPath(s, ch) || man.Download.ExpiresAt == 0 {
			t.Fatalf("download manifest = %+v", man)
		}
		did := man.Download.ID
		wantStatus(t, "finish other's download", b.SendJSON(http.MethodPut, "/api/v1/reader/downloads/"+did, map[string]any{"bytes": 10}), http.StatusNotFound)
		wantStatus(t, "delete other's download", b.Do(http.MethodDelete, "/api/v1/reader/downloads/"+did, nil, ""), http.StatusNotFound)
		wantStatus(t, "finish download", a.SendJSON(http.MethodPut, "/api/v1/reader/downloads/"+did, map[string]any{"bytes": 4096}), http.StatusOK)
		wantStatus(t, "download unreadable", a.PostJSON("/api/v1/reader/downloads", map[string]any{"series_id": private.ID, "publication_id": privCh.ID}), http.StatusNotFound)
		wantStatus(t, "anonymous download", e.ta.NewClient().PostJSON("/api/v1/reader/downloads", map[string]any{"series_id": s.ID, "publication_id": ch.ID}), http.StatusUnauthorized)
		if l := b.Get("/api/v1/reader/downloads"); strings.Contains(string(l.Body), did) {
			t.Fatal("another member lists the download")
		}
		lib := string(a.Get("/library/downloads").Body)
		if !strings.Contains(lib, `data-remove-download="`+did+`"`) || !strings.Contains(lib, "1 chapter · 4 KB") {
			t.Fatalf("library downloads tab: %.2000s", lib)
		}
		if strings.Contains(string(b.Get("/library/downloads").Body), did) {
			t.Fatal("another member's library lists the download")
		}
		cleared := a.Do(http.MethodDelete, "/api/v1/reader/downloads", nil, "")
		wantStatus(t, "clear downloads", cleared, http.StatusOK)
		if cleared.Map(t)["removed"] != float64(1) {
			t.Fatalf("cleared = %s", cleared.Body)
		}
	})
}

// A download recorded while the chapter was readable expires once access
// is gone, and the library stops showing it.
func TestDownloadExpiresWhenAccessIsRevoked(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		s := e.f.Series(publishtest.SeriesSpec{Visibility: publish.VisibilityMembers, Title: "Paper Crows"})
		ch := e.f.Publish(s, publishtest.PublicationSpec{})
		_, a := e.member()
		start := a.PostJSON("/api/v1/reader/downloads", map[string]any{"series_id": s.ID, "publication_id": ch.ID})
		wantStatus(t, "start", start, http.StatusOK)
		id := start.Map(t)["download"].(map[string]any)["id"].(string)
		wantStatus(t, "finish", a.SendJSON(http.MethodPut, "/api/v1/reader/downloads/"+id, map[string]any{"bytes": 1}), http.StatusOK)
		if _, err := e.ta.Deps.DB.Exec(context.Background(), `UPDATE series SET visibility = ? WHERE id = ?`, publish.VisibilityPrivate, s.ID); err != nil {
			t.Fatal(err)
		}
		list := a.Get("/api/v1/reader/downloads")
		var out struct {
			Items []struct {
				ID       string `json:"id"`
				Status   string `json:"status"`
				Readable bool   `json:"readable"`
				Cache    string `json:"cache"`
			} `json:"items"`
		}
		list.JSON(t, &out)
		if len(out.Items) != 1 || out.Items[0].Readable || out.Items[0].Status != "expired" || out.Items[0].Cache != "mv-offline-"+ch.ID {
			t.Fatalf("revoked download = %+v", out.Items)
		}
		lib := string(a.Get("/library/downloads").Body)
		if strings.Contains(lib, "Paper Crows") || !strings.Contains(lib, ch.ID) {
			t.Fatalf("revoked download shown, or its cache not handed to the island: %.1500s", lib)
		}
	})
}

// The library is for members; it lists followed series through
// social.Follows and publish.Service, and history through read_progress.
func TestLibrary(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		got := e.ta.NewClient().Get("/library/following")
		if got.StatusCode != http.StatusSeeOther || !strings.HasPrefix(got.Header.Get("Location"), "/login") {
			t.Fatalf("anonymous library: %d %q", got.StatusCode, got.Header.Get("Location"))
		}
		u, c := e.member()
		wantStatus(t, "library index", c.Get("/library"), http.StatusSeeOther)
		wantStatus(t, "unknown tab", c.Get("/library/settings"), http.StatusNotFound)
		empty := string(c.Get("/library/following").Body)
		if !strings.Contains(empty, "Nothing followed yet") {
			t.Fatal("following without follows does not show its empty state")
		}

		orchard := e.f.Series(publishtest.SeriesSpec{Title: "Glass Orchard", Cover: true})
		ch1 := e.f.Publish(orchard, publishtest.PublicationSpec{Pages: 4})
		e.f.Publish(orchard, publishtest.PublicationSpec{Pages: 4})
		hidden := e.f.Series(publishtest.SeriesSpec{Title: "Ninth Bell", Visibility: publish.VisibilityPrivate})
		e.f.Publish(hidden, publishtest.PublicationSpec{})
		c.SendJSON(http.MethodPut, "/api/v1/reader/progress", map[string]any{"series_id": orchard.ID, "publication_id": ch1.ID, "page": 2, "percent": 0.5})

		hist := string(c.Get("/library/history").Body)
		if !strings.Contains(hist, "Glass Orchard") || !strings.Contains(hist, "Ch. 1 · page 2 of 4") {
			t.Fatalf("history: %.2000s", hist)
		}

		// The private series cannot be followed (or listed) by this member.
		e.follow(c, u, hidden, orchard)
		page := string(c.Get("/library/following").Body)
		for _, want := range []string{"Glass Orchard", "Following 1", "Continue reading", "Ch. 1 · page 2 of 4", `src="/media/`} {
			if !strings.Contains(page, want) {
				t.Errorf("following tab missing %q", want)
			}
		}
		if strings.Contains(page, "Ninth Bell") {
			t.Fatal("following lists a series the member may not read")
		}
		if !strings.Contains(page, "New ch.") {
			t.Error("unread newer chapter is not marked new")
		}
	})
}

// Reading events go through analytics.Recorder: the reader records the open
// when it renders a chapter (without delaying the page), and the island's
// page, complete and drop batches go to the analytics module's endpoint with
// the same first-party reading session. Without a recorder nothing is recorded
// and the island is told not to send events.
func TestReadingEvents(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		s := e.f.Series(publishtest.SeriesSpec{})
		ch := e.f.Publish(s, publishtest.PublicationSpec{Pages: 2})
		anon := e.ta.NewClient()
		if _, ok := core.Use[analytics.Recorder](e.ta.Deps); !ok {
			got := anon.Get(chapterPath(s, ch))
			if props(t, got.Body)["events"] != false {
				t.Fatal("island sends events without a recorder")
			}
			rec := e.provideRecorder()
			anon.Get(chapterPath(s, ch))
			rec.wait(t, 1)
			if k := rec.kinds(); k[0] != "open" || rec.events[0].session == "" || rec.events[0].viewer.UserID != "" {
				t.Fatalf("recorded %+v", rec.events)
			}
			return
		}
		// The analytics module is wired: the open lands in read_events.
		page := anon.Get(chapterPath(s, ch))
		p := props(t, page.Body)
		if p["events"] != true || p["api"].(map[string]any)["events"] != "/api/v1/analytics/events" {
			t.Fatalf("island events = %v %v", p["events"], p["api"])
		}
		count := func(kind string) int {
			var n int
			if err := e.ta.Deps.DB.Get(context.Background(), &n, `SELECT COUNT(*) FROM read_events WHERE publication_id = ? AND kind = ?`, ch.ID, kind); err != nil {
				t.Fatal(err)
			}
			return n
		}
		deadline := time.Now().Add(5 * time.Second)
		for count("open") == 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if count("open") != 1 {
			t.Fatal("the reader did not record the open")
		}
		// The island's batch shares the session cookie the page set.
		batch := map[string]any{"events": []map[string]any{
			{"series_id": s.ID, "publication_id": ch.ID, "kind": "page", "page": 1, "ms": 4000},
			{"series_id": s.ID, "publication_id": ch.ID, "kind": "complete", "page": 2},
		}}
		got := anon.PostJSON("/api/v1/analytics/events", batch)
		if got.StatusCode >= 300 {
			t.Fatalf("batch: %d %s", got.StatusCode, got.Body)
		}
		if count("page") != 1 || count("complete") != 1 {
			t.Fatalf("batch not recorded: page=%d complete=%d", count("page"), count("complete"))
		}
		var sessions int
		if err := e.ta.Deps.DB.Get(context.Background(), &sessions, `SELECT COUNT(DISTINCT session_hash) FROM read_events WHERE publication_id = ?`, ch.ID); err != nil {
			t.Fatal(err)
		}
		if sessions != 1 {
			t.Fatalf("open and batch used %d session hashes", sessions)
		}
	})
}

// A client that drops the reading-session cookie (a curl loop on the chapter
// URL) cannot add reads without limit: every new anonymous session, the one
// the server-rendered open starts included, goes through the per-address
// new-session limit, and a session the client made up is no session. A
// limited request still gets the whole chapter; it only is not counted.
func TestAnonymousOpensAreLimitedPerAddress(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		if _, ok := core.Use[analytics.Recorder](e.ta.Deps); !ok {
			t.Skip("needs the analytics module's recorder")
		}
		s := e.f.Series(publishtest.SeriesSpec{ViewCount: 5})
		ch := e.f.Publish(s, publishtest.PublicationSpec{Pages: 2})
		c := e.ta.NewClient()
		c.Client.Jar = nil
		minted := 0
		get := func(what string) {
			got := c.Get(chapterPath(s, ch))
			wantStatus(t, what, got, http.StatusOK)
			if !strings.Contains(string(got.Body), `class="mv-rd__img"`) {
				t.Fatalf("%s: the chapter was not rendered", what)
			}
			for _, sc := range got.Header.Values("Set-Cookie") {
				if strings.HasPrefix(sc, analytics.SessionCookie+"=") {
					minted++
				}
			}
		}
		for range analytics.NewSessionBurst + 5 {
			get("cookieless reader")
		}
		if minted != analytics.NewSessionBurst {
			t.Fatalf("new reading sessions = %d, want the burst of %d", minted, analytics.NewSessionBurst)
		}
		for i := range 3 {
			c.Header.Set("Cookie", analytics.SessionCookie+"="+strings.Repeat(string(rune('k'+i)), 43))
			get("self-made session")
		}
		if minted != analytics.NewSessionBurst {
			t.Fatalf("a self-made session cookie started %d sessions", minted-analytics.NewSessionBurst)
		}
		opens := func() int {
			var n int
			if err := e.ta.Deps.DB.Get(context.Background(), &n, `SELECT COUNT(*) FROM read_events WHERE publication_id = ? AND kind = 'open'`, ch.ID); err != nil {
				t.Fatal(err)
			}
			return n
		}
		deadline := time.Now().Add(5 * time.Second)
		for opens() < minted && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(200 * time.Millisecond) // let any stray recording land
		if n := opens(); n != minted {
			t.Fatalf("opens = %d, want %d", n, minted)
		}
		got, err := e.f.Service().SeriesByID(context.Background(), publish.Viewer{}, s.ID)
		if err != nil || got.ViewCount != 5+int64(minted) {
			t.Fatalf("view_count = %d, %v; want %d", got.ViewCount, err, 5+minted)
		}
	})
}

// An unlisted chapter is read by its link but never listed (ADR 0012 §1):
// its reader page renders, prev/next come from the neighbouring listed
// chapters by number (or are absent), the Episodes list and the chapter-end
// card leave it out, listed chapters step over it, and the library's
// "New ch." never points at it. A member who read, bookmarked or downloaded
// it by its link still finds it in the library, and the download is not
// revoked. publish.Service's Publications lists unlisted chapters for project
// members (the owner below) and leaves them out for everyone else (the
// anonymous visitor and the member), so both cases are covered.
func TestUnlistedChapterIsReadByLinkOnly(t *testing.T) {
	each(t, func(t *testing.T, e *env) { checkUnlistedChapter(t, e) })
}

func checkUnlistedChapter(t *testing.T, e *env) {
	const behaviour = "unlisted"
	s := e.f.Series(publishtest.SeriesSpec{Title: "Paper Crows"})
	ch1 := e.f.Publish(s, publishtest.PublicationSpec{Pages: 2})
	bonus := e.f.Publish(s, publishtest.PublicationSpec{Pages: 2, Visibility: publish.VisibilityUnlisted, Title: "Harbour bonus"})
	ch3 := e.f.Publish(s, publishtest.PublicationSpec{Pages: 2})
	extra := e.f.Publish(s, publishtest.PublicationSpec{Pages: 2, Visibility: publish.VisibilityUnlisted, Title: "Lantern extra"})
	u, member := e.member()
	owner := e.ta.Login(t, s.Owner.Username)

	id := func(v any) string {
		if m, ok := v.(map[string]any); ok {
			s, _ := m["id"].(string)
			return s
		}
		return ""
	}
	for name, c := range map[string]*testutil.Client{"anonymous": e.ta.NewClient(), "member": member, "project owner": owner} {
		name = behaviour + ", " + name
		check := func(what string, ch publishtest.Publication, prev, next string) {
			t.Helper()
			got := c.Get(chapterPath(s, ch))
			wantStatus(t, name+" "+what, got, http.StatusOK)
			p := props(t, got.Body)
			if id(p["prev"]) != prev || id(p["next"]) != next {
				t.Errorf("%s %s: prev %q next %q, want %q and %q", name, what, id(p["prev"]), id(p["next"]), prev, next)
			}
			var listed []string
			for _, raw := range p["chapters"].([]any) {
				c := raw.(map[string]any)
				listed = append(listed, c["id"].(string))
				if c["current"] == true && c["id"] != ch.ID {
					t.Errorf("%s %s: %v is marked current", name, what, c["id"])
				}
			}
			if strings.Join(listed, ",") != ch1.ID+","+ch3.ID {
				t.Errorf("%s %s: episodes %v, want only the listed chapters", name, what, listed)
			}
			body := string(got.Body)
			for _, unlisted := range []publishtest.Publication{bonus, extra} {
				if unlisted.ID != ch.ID && strings.Contains(body, `href="`+chapterPath(s, unlisted)+`"`) {
					t.Errorf("%s %s: links to unlisted chapter %v", name, what, unlisted.Number)
				}
			}
		}
		check("unlisted between", bonus, ch1.ID, ch3.ID)
		check("unlisted last", extra, ch3.ID, "")
		check("first listed", ch1, "", ch3.ID)
		check("last listed", ch3, ch1.ID, "")
	}

	// The library offers the newest listed chapter, not the unlisted extra.
	e.follow(member, u, s)
	page := string(member.Get("/library/following").Body)
	if !strings.Contains(page, "Ch. 3 · new") || !strings.Contains(page, `href="`+chapterPath(s, ch3)+`"`) {
		t.Errorf("%s: following does not offer the newest listed chapter: %.1500s", behaviour, page)
	}
	if strings.Contains(page, chapterPath(s, extra)) || strings.Contains(page, "Ch. 4") {
		t.Errorf("%s: following points at an unlisted chapter", behaviour)
	}

	// Reading, bookmarking and downloading the unlisted chapter by its link
	// keeps it in the member's library.
	wantStatus(t, behaviour+" progress", member.SendJSON(http.MethodPut, "/api/v1/reader/progress",
		map[string]any{"series_id": s.ID, "publication_id": bonus.ID, "page": 2, "percent": 1, "completed": true}), http.StatusOK)
	wantStatus(t, behaviour+" bookmark", member.PostJSON("/api/v1/reader/bookmarks",
		map[string]any{"series_id": s.ID, "publication_id": bonus.ID, "page": 2}), http.StatusCreated)
	start := member.PostJSON("/api/v1/reader/downloads", map[string]any{"series_id": s.ID, "publication_id": bonus.ID})
	wantStatus(t, behaviour+" download unlisted", start, http.StatusOK)
	var man struct {
		Download  struct{ ID string } `json:"download"`
		ReaderURL string              `json:"reader_url"`
	}
	start.JSON(t, &man)
	if man.ReaderURL != chapterPath(s, bonus) {
		t.Errorf("%s: download reader_url = %q", behaviour, man.ReaderURL)
	}
	wantStatus(t, behaviour+" finish download", member.SendJSON(http.MethodPut, "/api/v1/reader/downloads/"+man.Download.ID,
		map[string]any{"bytes": 2048}), http.StatusOK)
	bonusHref := `href="` + chapterPath(s, bonus)
	for _, tab := range []string{"history", "bookmarks", "downloads"} {
		if lib := string(member.Get("/library/" + tab).Body); !strings.Contains(lib, bonusHref) {
			t.Errorf("%s: library %s lost the chapter read by its link", behaviour, tab)
		}
	}
	var list struct {
		Items []struct {
			ID       string `json:"id"`
			Status   string `json:"status"`
			Readable bool   `json:"readable"`
		} `json:"items"`
	}
	member.Get("/api/v1/reader/downloads").JSON(t, &list)
	if len(list.Items) != 1 || list.Items[0].Status != "ready" || !list.Items[0].Readable {
		t.Errorf("%s: unlisted download = %+v, want ready and readable", behaviour, list.Items)
	}
}

// A webtoon series opens in Scroll mode with vertical direction and the
// Scroll/Guided switch of E4.
func TestWebtoonDefaults(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		s := e.f.Series(publishtest.SeriesSpec{Format: "webtoon", Title: "Low Tide Radio"})
		ch := e.f.Publish(s, publishtest.PublicationSpec{Title: "Static"})
		got := e.ta.NewClient().Get(chapterPath(s, ch))
		wantStatus(t, "webtoon", got, http.StatusOK)
		p := props(t, got.Body)
		prefs := p["prefs"].(map[string]any)
		if prefs["mode"] != "webtoon" || prefs["direction"] != "vertical" {
			t.Fatalf("webtoon prefs = %v", prefs)
		}
		if !strings.Contains(string(got.Body), "Ep. 1 · Static") {
			t.Fatal("webtoon chapter label")
		}
	})
}
