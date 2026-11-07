package export

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

func TestExportScreenStatesLocalesAndFixture(t *testing.T) {
	for _, lang := range []string{"en", "ja"} {
		for _, state := range []string{"form", "empty", "queued", "running", "done", "failed", "canceled", "invalid"} {
			t.Run(lang+"/"+state, func(t *testing.T) {
				ctx := i18n.WithLocale(context.Background(), lang)
				p := pagedoc.New("page", "project", "chapter", 1, 1600, 2400, "rtl")
				v := exportView{Project: studio.Project{ID: "project", Title: "Tidebreak Courier", PageSize: "B5"}, Chapter: studio.Chapter{ID: "chapter", Number: 8, Title: "The Lighthouse Debt"}, Pages: []pagestore.PageSummary{{ID: "page", Number: 7}}, Page: p, Request: Request{Format: FormatPDF, TrimID: "b5", Lang: lang, DPI: 300, CreditsPage: true}, PageMode: "all", Base: "/export", Back: "/editor", Languages: []ui.Option{{Value: "en", Label: "English"}, {Value: "ja", Label: "日本語"}}}
				if state == "empty" {
					v.Pages = nil
					v.Page = pagedoc.Page{}
				}
				if state == "invalid" {
					v.Error = i18n.T(ctx, "exportui.invalid")
				}
				if state != "form" && state != "empty" && state != "invalid" {
					v.Job = &jobs.Job{ID: "export-job", State: state, Progress: .4}
					if state == "done" {
						v.Result = &Result{URL: "/download", Filename: "chapter.pdf"}
					}
				}
				chrome := &layout.Chrome{Lang: lang, Dir: "ltr", Theme: "dark", Title: "Export", InstanceName: "MangaVeyra", Dev: true, Prefs: settings.DefaultUserPrefs()}
				var b bytes.Buffer
				if err := exportScreen(chrome, v).Render(ctx, &b); err != nil {
					t.Fatal(err)
				}
				html := b.String()
				if strings.Contains(html, `disabled="false"`) || strings.Contains(html, `checked="false"`) {
					t.Fatal("boolean HTML attributes must be omitted when false")
				}
				for _, needed := range []string{"novalidate", "name=\"format\"", "name=\"pages\"", "name=\"lang\"", "name=\"credits_page\"", "value=\"cbz\"", "inert", "aria-labelledby=\"chapter-export-title\""} {
					if !strings.Contains(html, needed) {
						t.Fatalf("missing %s", needed)
					}
				}
				if strings.Contains(html, "exportui.") {
					t.Fatal("untranslated catalogue key")
				}
				if state == "done" && (!strings.Contains(html, "download=\"chapter.pdf\"") || !strings.Contains(html, i18n.T(ctx, "exportui.download"))) {
					t.Fatal("download missing")
				}
				if (state == "queued" || state == "running") && !strings.Contains(html, "hx-trigger=\"load delay:2s\"") {
					t.Fatal("active job not refreshed")
				}
				if dir := os.Getenv("MV_RENDER_QA_DIR"); dir != "" {
					if err := os.MkdirAll(dir, 0750); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, "export-"+lang+"-"+state+".html"), b.Bytes(), 0600); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}

// d1View is the Tidebreak Courier fixture behind the E2 modal: three pages, a
// first panel with every layer kind and two bubbles with an English original
// and a Japanese translation.
func d1View(lang string) exportView {
	ctx := i18n.WithLocale(context.Background(), lang)
	score := .96
	p := pagedoc.New("page-1", "project", "chapter", 1, 1600, 2400, "rtl")
	p.Panels = []pagedoc.Panel{{ID: "panel-1", Points: pagedoc.RectPoints(pagedoc.Rect{X: 10, Y: 10, W: 1580, H: 1180}), Layers: []pagedoc.Layer{
		{ID: "bg", Kind: pagedoc.LayerBackground, Name: "Background", Visible: true, Provenance: pagedoc.ProvenanceAI},
		{ID: "rin", Kind: pagedoc.LayerCharacter, Name: "Rin Asagiri", Visible: true, Provenance: pagedoc.ProvenanceAI, LockScore: &score},
		{ID: "fx", Kind: pagedoc.LayerFX, Name: "FX · speed lines", Visible: false, Provenance: pagedoc.ProvenanceAI},
		{ID: "letters", Kind: pagedoc.LayerLettering, Name: "Lettering", Visible: true, Locked: true, Provenance: pagedoc.ProvenanceHuman},
	}}}
	text := map[string]pagedoc.BubbleText{"en": {Text: "The ferry won't wait.", Source: "human", Status: "approved"}, "ja": {Text: "フェリーは待たない。", Source: "human", Status: "approved"}}
	p.Bubbles = []pagedoc.Bubble{{ID: "b1", PanelID: "panel-1", Texts: text}, {ID: "b2", PanelID: "panel-1", Texts: text}, {ID: "b3", PanelID: "elsewhere", Texts: text}}
	original := i18n.T(ctx, "exportui.original", "language", i18n.T(ctx, "exportui.language.en"))
	return exportView{
		Project: studio.Project{ID: "project", Title: "Tidebreak Courier", PageSize: "b5", Format: "manga_rtl", PageWidth: 1600, PageHeight: 2400, Language: "en"},
		Chapter: studio.Chapter{ID: "chapter", Number: 8, Title: "The Lighthouse Debt"},
		Pages:   []pagestore.PageSummary{{ID: "page-1", Number: 1}, {ID: "page-2", Number: 2, Locked: true}, {ID: "page-3", Number: 3}},
		Page:    p, PageMode: "all", Base: "/export", Back: "/editor",
		Request:   Request{Format: FormatPDF, TrimID: "b5", Lang: "en", CreditsPage: true},
		Languages: []ui.Option{{Value: "en", Label: original}, {Value: "ja", Label: i18n.T(ctx, "exportui.language.ja")}},
	}
}

func renderExport(t *testing.T, lang string, c *layout.Chrome, v exportView) string {
	t.Helper()
	if c == nil {
		c = &layout.Chrome{Lang: lang, Dir: "ltr", Theme: "dark", Title: "Export", InstanceName: "MangaVeyra", Prefs: settings.DefaultUserPrefs()}
	}
	var b bytes.Buffer
	if err := exportScreen(c, v).Render(i18n.WithLocale(context.Background(), lang), &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// section returns the markup from the first start marker to the next end marker.
func section(t *testing.T, html, start, end string) string {
	t.Helper()
	i := strings.Index(html, start)
	if i < 0 {
		t.Fatalf("missing %q", start)
	}
	j := strings.Index(html[i:], end)
	if j < 0 {
		t.Fatalf("missing %q after %q", end, start)
	}
	return html[i : i+j]
}

// inOrder fails unless every needle appears in html, in the given order.
func inOrder(t *testing.T, html string, needles ...string) {
	t.Helper()
	at := 0
	for _, n := range needles {
		i := strings.Index(html[at:], n)
		if i < 0 {
			t.Fatalf("%q missing or out of order (after offset %d)", n, at)
		}
		at += i + len(n)
	}
}

func TestExportBackdropIsTheD1EditorShell(t *testing.T) {
	for _, lang := range []string{"en", "ja"} {
		t.Run(lang, func(t *testing.T) {
			ctx := i18n.WithLocale(context.Background(), lang)
			c := &layout.Chrome{Lang: lang, Dir: "ltr", Theme: "dark", Title: "Export", InstanceName: "MangaVeyra", ServerHost: "studio.local", Prefs: settings.DefaultUserPrefs(),
				User:  &httpx.User{ID: "u1", Username: "jo", DisplayName: "Jo Oda"},
				Meter: &ui.MeterProps{Href: "/queue", Load: .68, Text: "2 queued", Title: "2 queued"}}
			html := renderExport(t, lang, c, d1View(lang))
			shell := section(t, html, `class="mv-exportui__editor"`, `class="mv-exportui__scrim"`)
			if !strings.Contains(shell, `aria-hidden="true" inert`) {
				t.Fatal("the editor behind the modal must be inert and hidden from assistive technology")
			}
			for _, absent := range []string{"ti-arrow-left", "ti-stack-2", "1600 × 2400", "<button", "<input", "<select", "Properties", "プロパティ", ">b5<"} {
				if strings.Contains(shell, absent) {
					t.Fatalf("shell still contains %q", absent)
				}
			}
			// Top bar: logo, "project > Ch. 8 · title ⌄", undo/redo, Pages current, presence,
			// compute, history, zoom, Preview, Export, Publish (Soon).
			bar := section(t, shell, `class="mv-exportui__bar"`, `</header>`)
			chapter := i18n.T(ctx, "exportui.chapter_title", "number", 8, "title", "The Lighthouse Debt")
			inOrder(t, bar, "/static/brand/mark-color-on-dark-gate-blue.svg", `<span>Tidebreak Courier</span>`, "ti-chevron-right", "<strong>"+chapter+"</strong>", "ti-chevron-down",
				"ti-arrow-back-up", "ti-arrow-forward-up",
				`<span class="mv-seg__item" aria-current="true">`+i18n.T(ctx, "exportui.pages")+`</span>`, i18n.T(ctx, "exportui.lettering"), i18n.T(ctx, "exportui.translate"), i18n.T(ctx, "exportui.animate"),
				"ti-users", "2 queued", "ti-history", "ti-zoom-in", "100%",
				`<span class="mv-btn mv-btn--quiet mv-btn--sm">`, "ti-eye", i18n.T(ctx, "exportui.preview"),
				`<span class="mv-btn mv-btn--quiet mv-btn--sm">`, "ti-download", i18n.T(ctx, "exportui.export"),
				`<span class="mv-btn mv-btn--primary mv-btn--sm is-soon">`, i18n.T(ctx, "exportui.publish"), `<span class="mv-soon">`+i18n.T(ctx, "ui.soon")+`</span>`)
			if strings.Count(bar, `aria-current="true"`) != 1 {
				t.Fatal("exactly one editor mode (Pages) is current")
			}
			// Left rail: filmstrip with the current page and lock badge, then layer rows top first.
			rail := section(t, shell, `class="mv-exportui__rail"`, `</aside>`)
			inOrder(t, rail, i18n.T(ctx, "exportui.pages"), "ti-layout-grid-add", "ti-dots",
				`mv-exportui__thumb is-current`, `/export/preview/page-1?lang=en`, `<figcaption>1</figcaption>`,
				`/export/preview/page-2?lang=en`, `mv-exportui__thumb-lock`, `<figcaption>2</figcaption>`, `<figcaption>3</figcaption>`,
				i18n.T(ctx, "exportui.layers_panel", "page", 1, "panel", 1), "ti-plus")
			rows := strings.Split(section(t, rail, `class="mv-exportui__layers"`, `</section>`), `<div class="mv-exportui__layer`)[1:]
			if len(rows) != 4 {
				t.Fatalf("layer rows: %d", len(rows))
			}
			want := []struct{ icon, name, meta, eye, lock string }{
				{"ti-message-circle", i18n.T(ctx, "exportui.layer.lettering"), i18n.T(ctx, "exportui.bubble_count", "count", 2), "ti-eye", `is-locked"><i class="ti ti-lock"`},
				{"ti-bolt", "FX · speed lines", i18n.T(ctx, "exportui.provenance.ai"), "ti-eye-off", "ti-lock-open"},
				{"ti-user", "Rin Asagiri", i18n.T(ctx, "exportui.lock_score", "score", 96), "ti-eye", "ti-lock-open"},
				{"ti-photo", i18n.T(ctx, "exportui.layer.background"), i18n.T(ctx, "exportui.provenance.ai"), "ti-eye", "ti-lock-open"},
			}
			for i, w := range want {
				inOrder(t, rows[i], w.icon, `<span class="mv-exportui__layer-name">`+w.name+`</span>`, "<small>"+w.meta+"</small>", w.eye+`"`, w.lock)
			}
			if !strings.HasPrefix(rows[3], ` is-selected"`) || strings.Contains(strings.Join(rows[:3], ""), "is-selected") {
				t.Fatal("the editor's default layer (first art layer) is the only selected row")
			}
			// Canvas proof, empty inspector (as drawn in E2) and the D1 status bar.
			inOrder(t, shell, `class="mv-exportui__canvas"`, `<img src="/export/preview/page-1?lang=en" data-export-preview width="520" height="780"`)
			if !strings.Contains(shell, `<aside class="mv-exportui__inspector"></aside>`) {
				t.Fatal("the inspector behind the modal is empty in E2")
			}
			status := section(t, shell, `class="mv-exportui__status"`, `</footer>`)
			inOrder(t, status, i18n.T(ctx, "exportui.page_of", "number", 1, "count", 3), i18n.T(ctx, "exportui.direction.rtl"), i18n.T(ctx, "exportui.page_size.b5"),
				"ti-circle-check", i18n.T(ctx, "exportui.saved"), `class="mv-exportui__host"`, i18n.T(ctx, "exportui.self_host", "host", "studio.local"))
			if lang == "en" {
				inOrder(t, status, "Page 1 of 3", "Manga · right to left", "B5 · 182 × 257 mm", "Saved", "Self-host · studio.local")
			}
		})
	}
}

var disabledSubmit = regexp.MustCompile(`<button[^>]*\bdisabled\b[^>]*data-export-submit|<button[^>]*data-export-submit[^>]*\bdisabled\b`)

func TestExportBackdropWithoutPagesAndLocalizedLayerNames(t *testing.T) {
	v := d1View("en")
	v.Pages, v.Page = nil, pagedoc.Page{}
	html := renderExport(t, "en", nil, v)
	shell := section(t, html, `class="mv-exportui__editor"`, `class="mv-exportui__scrim"`)
	if strings.Contains(shell, "<img src=") || strings.Contains(shell, `class="mv-exportui__layer`+" ") || strings.Contains(shell, "<figure") {
		t.Fatal("an empty chapter draws no proof, thumbnails or layers")
	}
	inOrder(t, shell, ">Layers<", "0 pages", "Manga · right to left", "B5 · 182 × 257 mm", "Self-host · localhost")
	if !disabledSubmit.MatchString(html) {
		t.Fatal("Export is disabled without pages")
	}
	// Stored default layer names follow the interface language; creator names do not.
	v = d1View("ja")
	v.Page.Panels[0].Layers = append(v.Page.Panels[0].Layers, pagedoc.Layer{ID: "fx2", Kind: pagedoc.LayerFX, Name: "Effects", Visible: true})
	html = renderExport(t, "ja", nil, v)
	for _, name := range []string{">背景<", ">効果<", ">文字<", ">Rin Asagiri<", ">FX · speed lines<"} {
		if !strings.Contains(html, name) {
			t.Fatalf("missing layer name %s", name)
		}
	}
	if strings.Contains(html, ">Effects<") || strings.Contains(html, ">Background<") {
		t.Fatal("default layer names must be localized")
	}
}

var currentLabel = regexp.MustCompile(`<span class="mv-exportui__go is-current" data-export-label="([a-z]*)">([^<]*)</span>`)

func TestExportButtonNamesTheFormatWithoutJavaScript(t *testing.T) {
	for _, lang := range []string{"en", "ja"} {
		ctx := i18n.WithLocale(context.Background(), lang)
		for _, format := range []string{FormatPNG, FormatPDF, FormatCBZ, "epub"} {
			v := d1View(lang)
			v.Request.Format = format
			html := renderExport(t, lang, nil, v)
			if !strings.Contains(html, `name="format" value="`+format+`" checked`) {
				t.Fatalf("%s/%s: format radio not checked", lang, format)
			}
			button := section(t, html, `data-export-submit="true"`, `</button>`)
			current := currentLabel.FindAllStringSubmatch(button, -1)
			want, wantKey := format, "exportui.export_as."+format
			if format == "epub" {
				want, wantKey = "", "exportui.export"
			}
			if len(current) != 1 || current[0][1] != want || current[0][2] != i18n.T(ctx, wantKey) {
				t.Fatalf("%s/%s: current label %q", lang, format, current)
			}
			for _, f := range exportableFormats {
				if !strings.Contains(button, `data-export-label="`+f+`">`+i18n.T(ctx, "exportui.export_as."+f)+`</span>`) {
					t.Fatalf("%s: label for %s missing", lang, f)
				}
			}
		}
	}
	if got := i18n.T(i18n.WithLocale(context.Background(), "en"), "exportui.export_as.pdf"); got != "Export PDF" {
		t.Fatalf("design copy: %q", got)
	}
	// Without JavaScript, CSS :has() switches the label when another format is chosen.
	css, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "styles", "exportui.css"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range exportableFormats {
		show := `.mv-exportui__form:has([name=format][value=` + f + `]:checked) .mv-exportui__go[data-export-label=` + f + `]`
		hide := `.mv-exportui__form:has([name=format][value=` + f + `]:checked) .mv-exportui__go:not([data-export-label=` + f + `])`
		if !strings.Contains(string(css), show) || !strings.Contains(string(css), hide) {
			t.Fatalf("exportui.css lacks the no-JS label rules for %s", f)
		}
	}
}

func TestExportModalUsesTheSharedControls(t *testing.T) {
	for _, lang := range []string{"en", "ja"} {
		t.Run(lang, func(t *testing.T) {
			ctx := i18n.WithLocale(context.Background(), lang)
			html := renderExport(t, lang, nil, d1View(lang))
			modal := html[strings.Index(html, `id="chapter-export"`):]
			soon := `<span class="mv-soon">` + i18n.T(ctx, "ui.soon") + `</span>`
			cards := strings.Split(section(t, modal, `class="mv-exportui__formats"`, `</fieldset>`), `<label class="mv-radio mv-exportui__format`)[1:]
			if len(cards) != len(exportFormats) {
				t.Fatalf("format cards: %d", len(cards))
			}
			icons := map[string]string{"png": "ti-file-type-png", "pdf": "ti-file-type-pdf", "epub": "ti-book", "video": "ti-movie", "cbz": "ti-archive"}
			for i, format := range exportFormats {
				card := cards[i]
				inOrder(t, card, `name="format" value="`+format+`"`, `<span class="mv-radio__box" aria-hidden="true"></span>`, icons[format], i18n.T(ctx, "exportui.format."+format))
				if formatSoon(format) != strings.Contains(card, soon) || formatSoon(format) != strings.Contains(card, "disabled") {
					t.Fatalf("%s: Soon tag or disabled state wrong", format)
				}
			}
			if strings.Contains(modal, "· Soon") || strings.Contains(modal, "・準備中") || strings.Contains(modal, `mv-exportui__soon`) {
				t.Fatal("Soon must use the shared tag, not plain text")
			}
			inOrder(t, modal, `class="mv-seg mv-seg--full mv-exportui__segments" role="radiogroup" aria-label="`+i18n.T(ctx, "exportui.pages")+`"`,
				`name="pages" value="all" checked`, `class="mv-seg__item">`+i18n.T(ctx, "exportui.pages.all"),
				`name="pages" value="range"`, i18n.T(ctx, "exportui.pages.range"), `name="pages" value="current"`, i18n.T(ctx, "exportui.pages.current"),
				`name="range"`)
			inOrder(t, modal, `class="mv-check"`, i18n.T(ctx, "exportui.credits"), `name="living_loops"`, i18n.T(ctx, "exportui.loops"), soon)
			if !strings.Contains(modal, `<a class="mv-exportui__close mv-close" href="/editor" aria-label="`+i18n.T(ctx, "ui.close")+`">`) {
				t.Fatal("close link")
			}
			if !strings.Contains(modal, `<a href="/editor" class="mv-btn mv-btn--ghost">`+i18n.T(ctx, "ui.cancel")+`</a>`) {
				t.Fatal("Cancel is the borderless ghost button")
			}
		})
	}
}

// contentLanguageOptions labels every manga content language the way the
// export handler does, with the first marked as the original.
func contentLanguageOptions(t *testing.T, ctx context.Context) []ui.Option {
	t.Helper()
	seen := map[string]string{}
	var options []ui.Option
	for _, tag := range pagedoc.ContentLanguages {
		key := "exportui.language." + tag
		label := i18n.T(ctx, key)
		if label == key || strings.TrimSpace(label) == "" {
			t.Fatalf("no label for %s", tag)
		}
		if other, dup := seen[label]; dup {
			t.Fatalf("%s and %s share the label %q", other, tag, label)
		}
		seen[label] = tag
		options = append(options, ui.Option{Value: tag, Label: label})
	}
	options[0].Label = i18n.T(ctx, "exportui.original", "language", options[0].Label)
	return options
}

func TestExportContentLanguageLabels(t *testing.T) {
	for _, lang := range []string{"en", "ja"} {
		contentLanguageOptions(t, i18n.WithLocale(context.Background(), lang))
	}
	for lang, want := range map[string][]string{
		"en": {"English (original)", "Japanese", "Korean", "Chinese (Simplified)", "Chinese (Traditional)", "Spanish", "French", "German", "Portuguese", "Hindi"},
		"ja": {"英語（原文）", "日本語", "韓国語", "中国語（簡体字）", "中国語（繁体字）", "スペイン語", "フランス語", "ドイツ語", "ポルトガル語", "ヒンディー語"},
	} {
		var got []string
		for _, o := range contentLanguageOptions(t, i18n.WithLocale(context.Background(), lang)) {
			got = append(got, o.Label)
		}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("%s labels %q, want %q", lang, got, want)
		}
	}
}

func TestExportLanguageOptionsAndTranslationNote(t *testing.T) {
	for _, lang := range []string{"en", "ja"} {
		ctx := i18n.WithLocale(context.Background(), lang)
		options := contentLanguageOptions(t, ctx)
		v := d1View(lang)
		v.Languages = options
		html := renderExport(t, lang, nil, v)
		field := section(t, html, `for="export-language"`, `</select>`)
		for _, o := range options {
			if !strings.Contains(field, `<option value="`+o.Value+`"`) || !strings.Contains(field, ">"+o.Label+"</option>") {
				t.Fatalf("%s: option %s/%q missing", lang, o.Value, o.Label)
			}
		}
		if !strings.Contains(field, `<span class="mv-field__aside">`+i18n.T(ctx, "exportui.translations", "count", 9)+`</span>`) {
			t.Fatalf("%s: translation note for nine translations missing", lang)
		}
		v.Languages = options[:1]
		html = renderExport(t, lang, nil, v)
		if strings.Contains(section(t, html, `for="export-language"`, `</select>`), "mv-field__aside") {
			t.Fatalf("%s: no translation note when the original is the only language", lang)
		}
	}
	for lang, want := range map[string][]string{"en": {"+1 translation", "+2 translations"}, "ja": {"翻訳 +1", "翻訳 +2"}} {
		ctx := i18n.WithLocale(context.Background(), lang)
		got := []string{i18n.T(ctx, "exportui.translations", "count", 1), i18n.T(ctx, "exportui.translations", "count", 2)}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("%s notes %q, want %q", lang, got, want)
		}
	}
	if html := renderExport(t, "en", nil, d1View("en")); !strings.Contains(html, `<span class="mv-field__aside">+1 translation</span>`) {
		t.Fatal("the designed +1 translation note is missing")
	}
}

func TestExportCataloguesMatch(t *testing.T) {
	keys := func(lang string) map[string]string {
		raw, err := fs.ReadFile(i18n.Embedded(), lang+"/exportui.json")
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	en, ja := keys("en"), keys("ja")
	for k := range en {
		if _, ok := ja[k]; !ok {
			t.Errorf("ja/exportui.json lacks %s", k)
		}
	}
	for k := range ja {
		if _, ok := en[k]; !ok {
			t.Errorf("en/exportui.json lacks %s", k)
		}
	}
}
