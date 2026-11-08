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

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/reader/agegate"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
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
		_, a := e.member()
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
			Cache string `json:"cache"`
			Pages []struct {
				Src string `json:"src"`
			} `json:"pages"`
			ReaderURL string `json:"reader_url"`
		}
		start.JSON(t, &man)
		if man.Cache != "mv-offline-"+ch.ID || man.Download.Status != "pending" || len(man.Pages) != 4 || man.ReaderURL != chapterPath(s, ch) || man.Download.ExpiresAt == 0 {
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
			t.Fatal("following without social.Follows does not show its empty state")
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

		f := e.provideFollows()
		f.byID[u.ID] = []string{hidden.ID, orchard.ID}
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

// Reading events go through analytics.Recorder when one is registered: the
// server records the open, the island's batches carry page, complete and
// drop. Without a recorder the endpoint answers 204 and stores nothing.
func TestReadingEvents(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		s := e.f.Series(publishtest.SeriesSpec{})
		ch := e.f.Publish(s, publishtest.PublicationSpec{Pages: 2})
		anon := e.ta.NewClient()
		batch := map[string]any{"events": []map[string]any{{"series_id": s.ID, "publication_id": ch.ID, "kind": "page", "page": 1, "ms": 4000}}}
		wantStatus(t, "events without recorder", anon.PostJSON("/api/v1/reader/events", batch), http.StatusNoContent)

		rec := e.provideRecorder()
		wantStatus(t, "open", anon.Get(chapterPath(s, ch)), http.StatusOK)
		rec.wait(t, 1)
		batch["events"] = []map[string]any{
			{"series_id": s.ID, "publication_id": ch.ID, "kind": "page", "page": 1, "ms": 4000},
			{"series_id": s.ID, "publication_id": ch.ID, "kind": "complete", "page": 2},
			{"series_id": s.ID, "publication_id": ch.ID, "kind": "bogus", "page": 2},
		}
		wantStatus(t, "batch", anon.PostJSON("/api/v1/reader/events", batch), http.StatusNoContent)
		rec.wait(t, 3)
		kinds := rec.kinds()
		if strings.Join(kinds, ",") != "open,page,complete" {
			t.Fatalf("recorded %v", kinds)
		}
		rec.mu.Lock()
		sessions := map[string]bool{}
		for _, ev := range rec.events {
			sessions[ev.session] = true
			if ev.viewer.UserID != "" {
				t.Error("anonymous event carried a user")
			}
		}
		rec.mu.Unlock()
		if len(sessions) != 1 || sessions[""] {
			t.Fatalf("anonymous events used sessions %v", sessions)
		}
	})
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
