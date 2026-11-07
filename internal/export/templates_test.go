package export

import (
	"bytes"
	"context"
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
