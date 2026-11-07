package export

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
