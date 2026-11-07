package studioui_test

import (
	"bytes"
	"context"
	"html"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/authui"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func postWizardFields(t *testing.T, client *testutil.Client, fields map[string]string) *testutil.Response {
	t.Helper()
	values := map[string]string{
		"title": "Salt Road Letters", "chapter_title": "Low Water", "mode": "premise",
		"premise": "A courier who cannot read maps carries the letters nobody else will deliver.",
		"style":   "seinen_bw", "format": "manga_rtl", "language": "en", "pages_target": "12",
	}
	for k, v := range fields {
		values[k] = v
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for key, value := range values {
		if err := mw.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return client.Do(http.MethodPost, "/studio/new/premise", &body, mw.FormDataContentType())
}

func mustContain(t *testing.T, body string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func mustNotContain(t *testing.T, body string, unwanted ...string) {
	t.Helper()
	for _, s := range unwanted {
		if strings.Contains(body, s) {
			t.Errorf("page contains %q", s)
		}
	}
}

var stepSectionRe = regexp.MustCompile(`<section class="mv-wizard__step" id="mv-step-([a-z]+)"[^>]*>`)

// The wizard is one complete form: without JavaScript all five steps render,
// in order, none hidden, and every field the create handler parses is there.
func TestWizardStepsAndCompleteNoJSForm(t *testing.T) {
	ta := testutil.NewTestApp(t)
	_, client := ta.UserClient(t, auth.RoleCreator)

	page := client.Get("/studio/new/premise")
	if page.StatusCode != http.StatusOK {
		t.Fatalf("wizard = %d", page.StatusCode)
	}
	body := string(page.Body)
	var steps []string
	for _, m := range stepSectionRe.FindAllStringSubmatch(body, -1) {
		steps = append(steps, m[1])
		if strings.Contains(m[0], " hidden") {
			t.Errorf("step %s is hidden without JavaScript", m[1])
		}
	}
	if strings.Join(steps, ",") != "premise,genre,style,format,generate" {
		t.Fatalf("steps = %v", steps)
	}
	mustContain(t, body,
		`action="/studio/new/premise"`, `enctype="multipart/form-data"`, `data-current="premise"`,
		"Step 1 of 5", "Step 4 of 5", "What’s the story?", "How will it be read?", "Ready to generate?",
		`href="#mv-step-format"`, `aria-current="step"`, `href="/studio"`,
		`name="title"`, `name="mode" value="premise"`, `name="mode" value="adapt"`, `name="premise"`,
		`name="source_text"`, `name="source_file"`, `name="page_size"`, `name="pages_target"`,
		`name="chapter_title"`, `name="language"`,
		"Manga · right to left", "Comic · left to right", "Vertical webtoon", "mv-fmt__page", "mv-fmt__strip",
		"B5 · 182 × 257 mm", "Pages per chapter",
		"Your manga so far", "What Generate will make", "mv-route", `data-summary="genres"`,
		`data-wizard-submit`, ">Generate<",
	)
	for _, genre := range authui.Genres {
		mustContain(t, body, `name="genres" value="`+genre+`"`)
	}
	for _, style := range authui.ArtStyles {
		mustContain(t, body, `name="style" value="`+style.Value+`"`)
	}
	for _, format := range []string{"manga_rtl", "comic_ltr", "webtoon"} {
		mustContain(t, body, `name="format" value="`+format+`"`)
	}
	// "Chapters to plan" is designed but not built: disabled, Soon, never posted.
	chapters := regexp.MustCompile(`<input[^>]*id="chapters_plan"[^>]*>`).FindString(body)
	if !strings.Contains(chapters, "disabled") || !strings.Contains(chapters, `name=""`) {
		t.Errorf("chapters to plan control = %s", chapters)
	}
	mustContain(t, body, "Chapters to plan", `<span class="mv-soon">Soon</span>`)
	// Back / Next exist for the script; without it only Generate shows.
	mustContain(t, body, `data-wizard-back hidden`, `data-wizard-next hidden`, `data-premise-tries hidden`)
	mustNotContain(t, body, "Gate A", "studioui.", "bw_screentone")

	if got := client.Get("/studio/new/format"); got.StatusCode != http.StatusOK || !strings.Contains(string(got.Body), `data-current="format"`) {
		t.Fatalf("format step = %d", got.StatusCode)
	}
	if got := client.Get("/studio/new/bogus"); got.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown step = %d, want 404", got.StatusCode)
	}
	if got := client.Get("/studio/new"); got.StatusCode != http.StatusSeeOther || got.Header.Get("Location") != "/studio/new/premise" {
		t.Fatalf("/studio/new = %d %q", got.StatusCode, got.Header.Get("Location"))
	}
}

// A rejected submit reopens the step that holds the first error and links to
// every step with an error; the values the user entered are kept.
func TestWizardValidationReopensStep(t *testing.T) {
	ta := testutil.NewTestApp(t)
	_, client := ta.UserClient(t, auth.RoleCreator)
	for _, tc := range []struct {
		name   string
		fields map[string]string
		step   string
	}{
		{"missing title", map[string]string{"title": ""}, "premise"},
		{"unknown genre", map[string]string{"genres": "adventure"}, "genre"},
		{"legacy style", map[string]string{"style": "bw_screentone"}, "style"},
		{"bad page size", map[string]string{"page_size": "letter"}, "format"},
		{"bad pages", map[string]string{"pages_target": "0"}, "format"},
		{"missing chapter", map[string]string{"chapter_title": ""}, "generate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := postWizardFields(t, client, tc.fields)
			body := string(r.Body)
			if r.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d", r.StatusCode)
			}
			mustContain(t, body, `data-current="`+tc.step+`"`, `data-step-link="`+tc.step+`"`, "Review these steps:",
				">A courier who cannot read maps carries the letters nobody else will deliver.</textarea>")
		})
	}
	r := postWizardFields(t, client, map[string]string{"title": "", "format": "scroll"})
	body := string(r.Body)
	mustContain(t, body, `data-current="premise"`, `href="#mv-step-premise" data-step-link="premise"`,
		`href="#mv-step-format" data-step-link="format"`)
}

func TestWizardMapsPageSizeAndOnboardingStyle(t *testing.T) {
	ta := testutil.NewTestApp(t)
	ctx := context.Background()
	creator, client := ta.UserClient(t, auth.RoleCreator)

	for _, tc := range []struct {
		title, format, size, wantSize string
		width, height                 int
	}{
		{"Paged A4", "comic_ltr", "a4", "a4", 1600, 2263},
		{"Paged B5", "manga_rtl", "", "b5", 1600, 2400},
		{"Long Strip", "webtoon", "a5", "webtoon", 1600, 2400},
	} {
		fields := map[string]string{"title": tc.title, "format": tc.format}
		if tc.size != "" {
			fields["page_size"] = tc.size
		}
		if r := postWizardFields(t, client, fields); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("%s: create = %d %.600s", tc.title, r.StatusCode, r.Body)
		}
		var row struct {
			PageSize string `db:"page_size"`
			Width    int    `db:"page_width"`
			Height   int    `db:"page_height"`
		}
		if err := ta.Deps.DB.Get(ctx, &row, `SELECT page_size, page_width, page_height FROM projects WHERE title = ?`, tc.title); err != nil {
			t.Fatal(err)
		}
		if row.PageSize != tc.wantSize || row.Width != tc.width || row.Height != tc.height {
			t.Errorf("%s: page = %+v", tc.title, row)
		}
	}

	if _, err := ta.Deps.DB.Exec(ctx, `UPDATE users SET onboarding = ? WHERE id = ?`, `{"art_style":"chibi"}`, creator.ID); err != nil {
		t.Fatal(err)
	}
	body := string(client.Get("/studio/new/style").Body)
	mustContain(t, body, `name="style" value="chibi" checked`, `data-summary="style">Chibi · 4-koma<`)
}

// B3 offers every manga content language with a localized name, defaults to
// the interface language and stores the chosen tag; anything else is refused.
func TestWizardOffersEveryContentLanguage(t *testing.T) {
	ta := testutil.NewTestApp(t)
	ctx := context.Background()
	_, client := ta.UserClient(t, auth.RoleCreator)
	names := map[string][2]string{
		"en": {"English", "英語"}, "ja": {"Japanese", "日本語"}, "ko": {"Korean", "韓国語"},
		"zh-Hans": {"Chinese (Simplified)", "中国語（簡体字）"}, "zh-Hant": {"Chinese (Traditional)", "中国語（繁体字）"},
		"es": {"Spanish", "スペイン語"}, "fr": {"French", "フランス語"}, "de": {"German", "ドイツ語"},
		"pt": {"Portuguese", "ポルトガル語"}, "hi": {"Hindi", "ヒンディー語"},
	}
	selectRe := regexp.MustCompile(`(?s)<select[^>]*name="language"[^>]*>(.*?)</select>`)
	optionRe := regexp.MustCompile(`<option value="([^"]+)"( selected)?>([^<]+)</option>`)
	for i, locale := range []string{"en", "ja"} {
		client.Header.Set("Accept-Language", locale)
		page := client.Get("/studio/new/generate")
		m := selectRe.FindStringSubmatch(string(page.Body))
		if page.StatusCode != http.StatusOK || m == nil {
			t.Fatalf("%s wizard = %d, language select found %v", locale, page.StatusCode, m != nil)
		}
		var order []string
		for _, o := range optionRe.FindAllStringSubmatch(m[1], -1) {
			order = append(order, o[1])
			if want := names[o[1]][i]; html.UnescapeString(o[3]) != want {
				t.Errorf("%s label for %s = %q, want %q", locale, o[1], o[3], want)
			}
			if (o[2] != "") != (o[1] == locale) {
				t.Errorf("%s: option %s selected=%v", locale, o[1], o[2] != "")
			}
		}
		if got := strings.Join(order, ","); got != "en,ja,ko,zh-Hans,zh-Hant,es,fr,de,pt,hi" {
			t.Errorf("%s options = %s", locale, got)
		}
	}
	client.Header.Del("Accept-Language")
	for _, lang := range []string{"ko", "zh-Hant", "hi"} {
		title := "Salt Road " + lang
		if r := postWizardFields(t, client, map[string]string{"title": title, "language": lang}); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("%s create = %d %.400s", lang, r.StatusCode, r.Body)
		}
		var stored string
		if err := ta.Deps.DB.Get(ctx, &stored, `SELECT language FROM projects WHERE title = ?`, title); err != nil || stored != lang {
			t.Fatalf("%s stored %q %v", lang, stored, err)
		}
	}
	r := postWizardFields(t, client, map[string]string{"title": "Unknown Tongue", "language": "xx"})
	if r.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(r.Body), `data-current="generate"`) {
		t.Fatalf("unknown language = %d", r.StatusCode)
	}
}

// htmlForm collects what a browser would submit from the first form whose
// opening tag contains marker: enabled inputs, textareas and selected options.
func htmlForm(t *testing.T, page, marker string) url.Values {
	t.Helper()
	start := strings.Index(page, marker)
	if start < 0 {
		t.Fatalf("form %q not found", marker)
	}
	start = strings.LastIndex(page[:start], "<form")
	end := strings.Index(page[start:], "</form>")
	form := page[start : start+end]
	attr := func(tag, name string) (string, bool) {
		m := regexp.MustCompile(`\s` + name + `(?:="([^"]*)")?[\s/>]`).FindStringSubmatch(tag)
		if m == nil {
			return "", false
		}
		return html.UnescapeString(m[1]), true
	}
	values := url.Values{}
	for _, tag := range regexp.MustCompile(`<input\b[^>]*>`).FindAllString(form, -1) {
		name, _ := attr(tag, "name")
		typ, _ := attr(tag, "type")
		if _, disabled := attr(tag, "disabled"); name == "" || disabled || typ == "submit" || typ == "button" || typ == "file" {
			continue
		}
		value, _ := attr(tag, "value")
		values.Add(name, value)
	}
	for _, m := range regexp.MustCompile(`(?s)<textarea\b([^>]*)>(.*?)</textarea>`).FindAllStringSubmatch(form, -1) {
		name, _ := attr(m[1]+" ", "name")
		values.Add(name, html.UnescapeString(m[2]))
	}
	for _, m := range regexp.MustCompile(`(?s)<select\b([^>]*)>(.*?)</select>`).FindAllStringSubmatch(form, -1) {
		name, _ := attr(m[1]+" ", "name")
		if _, disabled := attr(m[1]+" ", "disabled"); disabled {
			continue
		}
		if sel := regexp.MustCompile(`<option value="([^"]*)" selected`).FindStringSubmatch(m[2]); sel != nil {
			values.Add(name, html.UnescapeString(sel[1]))
		}
	}
	return values
}

// C1 renders the tree, the compact panel table, the read-only prose view, the
// Soon controls and labelled cells; the rendered form saves round trip.
func TestStoryEditorPanelsProseSoonAndSaveRoundTrip(t *testing.T) {
	forEachStory(t, func(t *testing.T, f storyFixture) {
		ctx := context.Background()
		doc, err := f.studio.GetScript(ctx, f.chapterID)
		if err != nil {
			t.Fatal(err)
		}
		chapter, err := f.studio.GetChapter(ctx, f.chapterID)
		if err != nil {
			t.Fatal(err)
		}
		// Give the first panel a line and a sound effect so their cells render.
		doc.Scenes[0].Panels[0].Dialogue = []studio.ScriptDialogue{{Speaker: "Rin", Kind: "speech", Text: "Then I'll take the roof!"}}
		doc.Scenes[0].Panels[0].SFX = []studio.ScriptSFX{{Text: "WHOOSH", Note: "wind"}}
		owner := studio.Actor{SiteRole: auth.RoleCreator}
		if err := f.ta.Deps.DB.Get(ctx, &owner.UserID, `SELECT owner_id FROM projects WHERE id = ?`, f.projectID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.studio.EditScript(ctx, studio.EditScriptInput{Actor: owner, ProjectID: f.projectID, ChapterID: f.chapterID,
			Doc: doc, ExpectedUpdatedAt: chapter.UpdatedAt}); err != nil {
			t.Fatalf("seed dialogue: %v", err)
		}
		scene, panel := doc.Scenes[0], doc.Scenes[0].Panels[0]

		page := f.client.Get(f.storyURL)
		body := string(page.Body)
		if page.StatusCode != http.StatusOK {
			t.Fatalf("story = %d", page.StatusCode)
		}
		mustContain(t, body,
			// tree
			`data-scene-link`, `href="#scene-`+scene.ID+`"`, `data-prose-href="#prose-`+scene.ID+`"`, "Ch. 1 · The Quiet Bell",
			// centre
			"Harbor Ghosts · Ch. 1", "Ch. 1 · Scene 1", `role="columnheader"`, ">Panel<", ">Camera · emotion<",
			">Action · dialogue<", ">SFX<", `id="scene-`+scene.ID+`"`,
			`href="`+f.storyURL+`?view=prose"`, ">Prose<", ">Panels<",
			// Soon: Make pages from script and the Script assistant
			"Make pages from script", "Script assistant", `id="mv-c1-ask"`, "Ask about this scene…",
			// prose view (hidden by default)
			`data-view-panel="prose" hidden`, `data-mirror="panel.`+panel.ID+`.action"`,
			// labelled dialogue and SFX cells
			`for="speaker-`+panel.ID+`-0"`, `for="kind-`+panel.ID+`-0"`, `for="line-`+panel.ID+`-0"`,
			`for="sfx-`+panel.ID+`-0"`, `for="sfx-note-`+panel.ID+`-0"`,
			"Panel "+panel.Label+" · Dialogue 1 · Speaker", "Panel "+panel.Label+" · Sound effect 1 · Lettering",
		)
		mustContain(t, body, `action="`+f.storyURL+`/pages"`, `name="revision"`)
		ask := regexp.MustCompile(`<input[^>]*id="mv-c1-ask"[^>]*>`).FindString(body)
		if !strings.Contains(ask, "disabled") {
			t.Errorf("assistant input = %s", ask)
		}
		if strings.Count(body, `<span class="mv-soon">Soon</span>`) < 2 { // assistant chip and input
			t.Errorf("Soon tags = %d", strings.Count(body, `<span class="mv-soon">Soon</span>`))
		}

		prose := string(f.client.Get(f.storyURL + "?view=prose").Body)
		mustContain(t, prose, `<article class="mv-c1__prose" data-view-panel="prose" aria-label`, `data-view-panel="panels" hidden`,
			`href="#prose-`+scene.ID+`"`, "Then I&#39;ll take the roof!", ">WHOOSH<")

		// Round trip: post exactly what the rendered Panels form submits.
		values := htmlForm(t, body, `data-view-panel="panels"`)
		for _, name := range []string{"revision", "chapter_summary", "scene." + scene.ID + ".title",
			"panel." + panel.ID + ".page_number", "panel." + panel.ID + ".dialogue.0.kind", "panel." + panel.ID + ".sfx.0.note"} {
			if !values.Has(name) {
				t.Fatalf("rendered form lacks %s", name)
			}
		}
		values.Set("panel."+panel.ID+".dialogue.0.text", "She actually jumped…")
		values.Set("panel."+panel.ID+".sfx.0.text", "THUD")
		values.Set("scene."+scene.ID+".title", "Rooftop chase")
		saved := postForm(f.client, f.storyURL+"/script", values)
		if saved.StatusCode != http.StatusSeeOther {
			t.Fatalf("round-trip save = %d: %.1500s", saved.StatusCode, saved.Body)
		}
		after, err := f.studio.GetScript(ctx, f.chapterID)
		if err != nil {
			t.Fatal(err)
		}
		got := after.Scenes[0].Panels[0]
		if after.Scenes[0].Title != "Rooftop chase" || got.Dialogue[0].Text != "She actually jumped…" ||
			got.Dialogue[0].Speaker != "Rin" || got.Dialogue[0].Kind != "speech" || got.SFX[0].Text != "THUD" ||
			got.SFX[0].Note != "wind" || got.Action != panel.Action || after.ChapterSummary != doc.ChapterSummary {
			t.Fatalf("round trip changed the wrong fields: %#v / %q", got, after.Scenes[0].Title)
		}
	})
}

// Readers see the same three panes read-only: no save, cells readonly, and
// the Soon controls still in place.
func TestStoryEditorReadOnlyForViewer(t *testing.T) {
	forEachStory(t, func(t *testing.T, f storyFixture) {
		ctx := context.Background()
		reader, readerClient := f.ta.UserClient(t, auth.RoleReader)
		if _, err := f.ta.Deps.DB.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role, created_at) VALUES (?, ?, 'viewer', ?)`,
			f.projectID, reader.ID, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		body := string(readerClient.Get(f.storyURL).Body)
		mustContain(t, body, "This manuscript is read-only.", "Script assistant", "Make pages from script")
		mustNotContain(t, body, ">Save changes<")
		title := regexp.MustCompile(`<input[^>]*id="scene-title-input-[^"]+"[^>]*>`).FindString(body)
		if !strings.Contains(title, "readonly") {
			t.Errorf("viewer scene title = %s", title)
		}
	})
}

// B1 project cards say "1 chapter" with a translated status, and carry the
// status and reading-format badges.
func TestDashboardCardMetaAndBadges(t *testing.T) {
	ta := testutil.NewTestApp(t)
	_, client := ta.UserClient(t, auth.RoleCreator)
	if r := postWizardFields(t, client, map[string]string{"title": "Glass Orchard", "format": "webtoon"}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("create = %d", r.StatusCode)
	}
	body := string(client.Get("/studio").Body)
	mustContain(t, body, "1 chapter · Draft", `<span class="mv-badge">Draft</span>`, `<span class="mv-badge mv-badge--muted">Webtoon</span>`)
	mustNotContain(t, body, "chapters · draft", "1 chapters")

	studioService := core.MustUse[studio.Service](ta.Deps)
	var projectID string
	if err := ta.Deps.DB.Get(context.Background(), &projectID, `SELECT id FROM projects WHERE title = ?`, "Glass Orchard"); err != nil {
		t.Fatal(err)
	}
	var ownerID string
	if err := ta.Deps.DB.Get(context.Background(), &ownerID, `SELECT owner_id FROM projects WHERE id = ?`, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := studioService.CreateChapter(context.Background(), studio.Actor{UserID: ownerID, SiteRole: auth.RoleCreator}, projectID, "Second Bloom"); err != nil {
		t.Fatal(err)
	}
	mustContain(t, string(client.Get("/studio").Body), "2 chapters · Draft")
}
