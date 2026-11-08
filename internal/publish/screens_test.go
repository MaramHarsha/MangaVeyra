package publish_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestAltTextDraftIsDeterministic(t *testing.T) {
	ctx := context.Background()
	bubbles := []publish.GuidedBubble{
		{Order: 1, Kind: "speech", Speaker: "Old Wren", Text: "  The   debt is\npaid. "},
		{Order: 2, Kind: "thought", Text: "Is it?"},
		{Order: 3, Kind: "caption", Text: "Later."},
		{Order: 4, Kind: "sfx", Text: "KRAK"},
		{Order: 5, Kind: "speech", Speaker: "Sora Vell", Text: " "},
	}
	got := publish.DraftAltTextForTest(ctx, "en", " Old Wren hands over the brass key ", "Lighthouse", "night", []string{"Old Wren", "Sora Vell"}, bubbles)
	want := "Old Wren hands over the brass key. Setting: Lighthouse, night. Characters: Old Wren, Sora Vell. " +
		"Old Wren: “The debt is paid.” “Is it?” Caption: Later. Sound effect: KRAK"
	if got != want {
		t.Fatalf("draft =\n%q\nwant\n%q", got, want)
	}
	for range 3 {
		if again := publish.DraftAltTextForTest(ctx, "en", " Old Wren hands over the brass key ", "Lighthouse", "night", []string{"Old Wren", "Sora Vell"}, bubbles); again != got {
			t.Fatal("draft is not deterministic")
		}
	}
	if ja := publish.DraftAltTextForTest(ctx, "ja", "鍵を渡す。", "灯台", "", []string{"Old Wren"}, bubbles[:1]); ja != "鍵を渡す。 場所：灯台。 登場人物：Old Wren。 Old Wren「The debt is paid.」" {
		t.Fatalf("ja draft %q", ja)
	}
	if empty := publish.DraftAltTextForTest(ctx, "en", "", "", "", nil, nil); empty != "" {
		t.Fatalf("empty draft %q", empty)
	}
	long := publish.DraftAltTextForTest(ctx, "en", strings.Repeat("Rin runs. ", 300), "", "", nil, nil)
	if n := len([]rune(long)); n > 1000 || !strings.HasSuffix(long, "…") {
		t.Fatalf("long draft is %d runes", n)
	}
}

func TestSeriesSettings(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, pf *publishtest.Fixture) {
		ctx := context.Background()
		f := newChapter(t, ta, false)
		path := "/studio/p/" + f.project.ID + "/series"
		page := f.client.Get(path)
		if page.StatusCode != http.StatusOK || !contains(page.Body, "Series settings") || !contains(page.Body, `value="tidebreak-courier"`) ||
			!contains(page.Body, "This project has no series yet") {
			t.Fatalf("new series page = %d", page.StatusCode)
		}
		// Invalid input is shown again with the errors and the values.
		bad := seriesForm("No Spaces!", "link_url_0", "javascript:alert(1)", "licence", "gpl", "title", "")
		resp := postForm(f.client, path, bad)
		for _, want := range []string{"Use 3–60 lowercase letters", "an http or https address", "Choose one of the options", "Enter a title"} {
			if resp.StatusCode != http.StatusUnprocessableEntity || !contains(resp.Body, want) {
				t.Fatalf("invalid series = %d, lacks %q", resp.StatusCode, want)
			}
		}
		if n := count(t, ta, `SELECT COUNT(*) FROM series`); n != 0 {
			t.Fatalf("invalid form created %d series", n)
		}
		if resp := postForm(f.client, path, seriesForm("tidebreak-courier")); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("create = %d %s", resp.StatusCode, resp.Body)
		}
		var s struct {
			Slug, Title, Licence, Visibility, Status, Genres, SupportLinks string
		}
		if err := ta.Deps.DB.Get(ctx, &s, `SELECT slug AS "slug", title AS "title", licence AS "licence", visibility AS "visibility",
			status AS "status", genres AS "genres", support_links AS "supportlinks" FROM series WHERE project_id = ?`, f.project.ID); err != nil {
			t.Fatal(err)
		}
		var links []publish.SupportLink
		_ = json.Unmarshal([]byte(s.SupportLinks), &links)
		if s.Slug != "tidebreak-courier" || s.Licence != "cc-by" || s.Genres != `["adventure","mystery"]` || len(links) != 1 || links[0].URL != "https://example.org/tidebreak" {
			t.Fatalf("saved series %+v", s)
		}
		// Not published yet: readers cannot see it.
		if _, err := pf.Service().SeriesBySlug(ctx, publish.Viewer{UserID: ta.Admin.ID}, "tidebreak-courier"); err == nil {
			t.Fatal("unpublished series readable")
		}
		// A stale form and a taken address are refused.
		edit := f.client.Get(path)
		rev := regexp.MustCompile(`name="revision" value="([^"]+)"`).FindSubmatch(edit.Body)
		if rev == nil {
			t.Fatal("no revision field")
		}
		if resp := postForm(f.client, path, seriesForm("tidebreak-courier", "revision", "2001-01-01T00:00:00Z")); resp.StatusCode != http.StatusUnprocessableEntity || !contains(resp.Body, "Someone saved the series meanwhile") {
			t.Fatalf("stale save = %d", resp.StatusCode)
		}
		other := newChapter(t, ta, false)
		if resp := postForm(other.client, "/studio/p/"+other.project.ID+"/series", seriesForm("tidebreak-courier")); resp.StatusCode != http.StatusUnprocessableEntity || !contains(resp.Body, "Another series already uses this address") {
			t.Fatalf("taken slug = %d", resp.StatusCode)
		}
		if resp := postForm(f.client, path, seriesForm("salt-city-courier", "revision", string(rev[1]), "visibility", "members")); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("edit = %d %s", resp.StatusCode, resp.Body)
		}
		// E1 warns when the series is stricter than the chapter.
		if j := f.publish(t, f.publishForm("en", "visibility", publish.VisibilityPublic)); j.State != "done" {
			t.Fatalf("publish %+v", j.Error)
		}
		if page := f.client.Get(f.base + "/publish"); !contains(page.Body, "The series is Members") {
			t.Fatal("E1 lacks the stricter-series notice")
		}
		viewer := ta.CreateUser(t, "", auth.RoleCreator)
		pf.AddMember(f.project.ID, viewer.ID, studio.RoleViewer)
		if got := ta.Login(t, viewer.Username).Get(path).StatusCode; got != http.StatusForbidden {
			t.Fatalf("viewer series page = %d", got)
		}
	})
}

func TestScreensAreCompleteInBothLanguages(t *testing.T) {
	ta := testutil.NewTestApp(t)
	f := newChapter(t, ta, false)
	for _, lang := range []string{"en", "ja"} {
		f.client.Header.Set("Accept-Language", lang)
		for _, path := range []string{f.base + "/publish", "/studio/p/" + f.project.ID + "/series"} {
			resp := f.client.Get(path)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s %s = %d", lang, path, resp.StatusCode)
			}
			checkPage(t, lang+" "+path, resp.Body)
			checkLinks(t, ta, lang+" "+path, resp.Body)
		}
	}
	f.client.Header.Set("Accept-Language", "en")
	if j := f.publish(t, f.publishForm("en")); j.State != "done" {
		t.Fatalf("publish %+v", j.Error)
	}
	for _, lang := range []string{"en", "ja"} {
		f.client.Header.Set("Accept-Language", lang)
		resp := f.client.Get(f.base + "/published?lang=en")
		checkPage(t, lang+" published", resp.Body)
		checkLinks(t, ta, lang+" published", resp.Body)
		checkLinks(t, ta, lang+" live E1", f.client.Get(f.base+"/publish").Body)
		if lang == "ja" && !contains(resp.Body, "第1話を公開しました") {
			t.Fatal("ja published page not translated")
		}
	}
	f.client.Header.Set("Accept-Language", "en")
	// Unbuilt parts are visible in the Soon state; the reader routes are not served yet, so their links are Soon too.
	body := f.client.Get(f.base + "/publish").Body
	if n := bytes.Count(body, []byte(`class="mv-soon"`)); n < 3 {
		t.Fatalf("E1 shows %d Soon tags", n)
	}
	for _, name := range []string{"offline", "comments"} {
		if !regexp.MustCompile(`id="pub-`+name+`"[^>]*disabled`).Match(body) && !regexp.MustCompile(`disabled[^>]*id="pub-`+name+`"`).Match(body) {
			t.Fatalf("%s control is not disabled", name)
		}
	}
}

var (
	linkTarget = regexp.MustCompile(`\s(?:href|hx-get|src)="(/[^"]*)"`)
	postTarget = regexp.MustCompile(`\s(?:action|formaction|hx-post)="(/[^"]*)"`)
)

// checkLinks fails for any link, form action or button formaction this
// server does not serve (unbuilt destinations must be in the Soon state).
func checkLinks(t *testing.T, ta *testutil.TestApp, name string, body []byte) {
	t.Helper()
	if bytes.Contains(body, []byte(`href=""`)) {
		t.Fatalf("%s has an empty link", name)
	}
	check := func(method string, re *regexp.Regexp) {
		for _, m := range re.FindAllSubmatch(body, -1) {
			path := strings.ReplaceAll(string(m[1]), "&amp;", "&")
			if i := strings.IndexAny(path, "?#"); i >= 0 {
				path = path[:i]
			}
			if strings.HasPrefix(path, "/static/") || strings.HasPrefix(path, "/media/") {
				continue
			}
			if !ta.Router.Serves(method, path) {
				t.Errorf("%s links to %s %s, which this server does not serve", name, method, path)
			}
		}
	}
	check(http.MethodGet, linkTarget)
	check(http.MethodPost, postTarget)
}

func checkPage(t *testing.T, name string, body []byte) {
	t.Helper()
	if bytes.Contains(body, []byte("publish.")) && regexp.MustCompile(`>[^<]*publish\.[a-z_]+`).Match(body) {
		t.Fatalf("%s shows an untranslated key", name)
	}
	if bytes.Contains(body, []byte(" style=")) {
		t.Fatalf("%s has inline styles", name)
	}
}

// The publish catalogues have the same keys in English and Japanese and no
// payment words (editioncheck scans every catalogue the same way).
func TestPublishCataloguesMatch(t *testing.T) {
	load := func(lang string) map[string]string {
		b, err := os.ReadFile(filepath.Join("..", "i18n", "locales", lang, "publish.json"))
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	en, ja := load("en"), load("ja")
	base := func(k string) string { return strings.TrimSuffix(strings.TrimSuffix(k, ".one"), ".other") }
	enKeys, jaKeys := []string{}, []string{}
	for k := range en {
		enKeys = append(enKeys, base(k))
	}
	for k := range ja {
		jaKeys = append(jaKeys, base(k))
	}
	slices.Sort(enKeys)
	slices.Sort(jaKeys)
	if !slices.Equal(slices.Compact(enKeys), slices.Compact(jaKeys)) {
		t.Fatalf("en and ja publish catalogues differ")
	}
	forbidden := regexp.MustCompile(`(?i)\b(price|prices|pricing|checkout|credits?|wallet|tips?|tipping|subscriptions?|subscribe|payouts?|upgrade to|buy now|purchase)\b`)
	for _, cat := range []map[string]string{en, ja} {
		for k, v := range cat {
			if forbidden.MatchString(v) {
				t.Fatalf("%s uses a payment word: %q", k, v)
			}
		}
	}
}
