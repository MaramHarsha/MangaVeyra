package export_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"image/color"
	"image/png"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	chapterexport "github.com/MaramHarsha/MangaVeyra/internal/export"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

func TestUnicodeTitleExportLanguagesAndEmptyPanels(t *testing.T) {
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			ta := testutil.NewTestApp(t, testutil.WithDialect(dialect))
			ctx := context.Background()
			ss, ps := core.MustUse[studio.Service](ta.Deps), core.MustUse[pagestore.Service](ta.Deps)
			actor := studio.Actor{UserID: ta.Admin.ID}
			project, err := ss.CreateProject(ctx, actor, studio.NewProject{Title: "潮騒の配達人", PageWidth: 200, PageHeight: 300, Language: "ja"})
			if err != nil {
				t.Fatal(err)
			}
			ch, err := ss.CreateChapter(ctx, actor, project.ID, "灯台")
			if err != nil {
				t.Fatal(err)
			}
			page := func(n int, texts map[string]pagedoc.BubbleText) pagedoc.Page {
				p := pagedoc.New(db.NewID(), project.ID, ch.ID, n, 200, 300, "rtl")
				p.Panels = []pagedoc.Panel{{ID: db.NewID(), PromptSource: "user", Points: pagedoc.RectPoints(pagedoc.Rect{X: 10, Y: 10, W: 180, H: 280}),
					Layers: []pagedoc.Layer{{ID: db.NewID(), Kind: "background", Visible: true, Opacity: 1, Provenance: "human"}}}}
				if texts != nil {
					p.Bubbles = []pagedoc.Bubble{{ID: db.NewID(), PanelID: p.Panels[0].ID, Kind: "speech", Shape: "ellipse", Box: pagedoc.Rect{X: 30, Y: 30, W: 140, H: 100},
						Style: pagedoc.BubbleStyle{Size: 24, AutoFit: true}, Texts: texts}}
				}
				return p
			}
			pages := []pagedoc.Page{
				page(1, map[string]pagedoc.BubbleText{"ja": {Text: "海へ行こう", Source: "human"}}),
				page(2, map[string]pagedoc.BubbleText{"ja": {Text: "灯台だ", Source: "human"}, "ko": {Text: "등대다", Source: "human"}, "fr": {Text: "  "}}),
				page(3, nil),
			}
			if err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
				_, err := ps.CreatePagesTx(ctx, tx, ch.ID, pages, pagestore.Change{ActorKind: "user", ActorID: actor.UserID, Action: "create"})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			client := ta.Login(t, ta.Admin.Username)
			base := "/studio/p/" + project.ID + "/chapters/" + strconv.Itoa(ch.Number) + "/export"

			// Language options come from every page; blank texts do not count.
			screen := client.Get(base + "?page=" + pages[0].ID)
			body := string(screen.Body)
			if screen.StatusCode != http.StatusOK || !strings.Contains(body, `value="ko"`) || !strings.Contains(body, `value="ja"`) || strings.Contains(body, `value="fr"`) {
				t.Fatalf("language options: %d", screen.StatusCode)
			}
			if got := client.Get(base + "/preview/" + pages[0].ID + "?lang=ko"); got.StatusCode != http.StatusOK {
				t.Fatalf("a chapter language previews on any page: %d", got.StatusCode)
			}
			if got := client.Get(base + "/preview/" + pages[0].ID + "?lang=fr"); got.StatusCode != http.StatusBadRequest {
				t.Fatalf("a language without text must be rejected: %d", got.StatusCode)
			}

			// An empty panel previews as a white storyboard area with a border.
			preview := client.Get(base + "/preview/" + pages[2].ID)
			im, err := png.Decode(bytes.NewReader(preview.Body))
			if err != nil {
				t.Fatal(err)
			}
			for y := 20; y < 280; y += 5 {
				for x := 20; x < 180; x += 5 {
					if g := color.GrayModel.Convert(im.At(x, y)).(color.Gray); g.Y < 250 {
						t.Fatalf("empty panel preview is not clean at %d,%d: %d", x, y, g.Y)
					}
				}
			}

			form := url.Values{"format": {"cbz"}, "trim_id": {"a5"}, "dpi": {"72"}, "lang": {"ko"}, "pages": {"range"}, "range": {"1-2"}, "credits_page": {"on"}}
			resp := client.Do(http.MethodPost, base+"?page="+pages[0].ID, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
			loc, err := url.Parse(resp.Header.Get("Location"))
			if err != nil || loc.Query().Get("job") == "" {
				t.Fatalf("export submit: %d %s", resp.StatusCode, resp.Body)
			}
			id := loc.Query().Get("job")
			if ran, err := ta.Deps.Executor.RunOnce(ctx); err != nil || !ran {
				t.Fatal("export executor", ran, err)
			}
			j, err := ta.Deps.Jobs.Get(ctx, id)
			if err != nil || j.State != jobs.StateDone {
				t.Fatalf("job: %+v %v", j, err)
			}
			var result chapterexport.Result
			if err = json.Unmarshal(j.Output, &result); err != nil {
				t.Fatal(err)
			}
			if result.Filename != "潮騒の配達人-ch-001.cbz" || result.ASCIIFilename != "mangaveyra-ch-001.cbz" {
				t.Fatalf("filenames %q %q", result.Filename, result.ASCIIFilename)
			}
			data, _, err := assets.From(ta.Deps).Read(ctx, result.AssetID, 0)
			if err != nil {
				t.Fatal(err)
			}
			z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			var info struct {
				Series, LanguageISO, Manga string
				PageCount                  int
			}
			for _, f := range z.File {
				if f.Name == "ComicInfo.xml" {
					r, _ := f.Open()
					raw, _ := io.ReadAll(r)
					if err := xml.Unmarshal(raw, &info); err != nil {
						t.Fatal(err)
					}
				}
			}
			if info.Series != "潮騒の配達人" || info.LanguageISO != "ko" || info.Manga != "YesAndRightToLeft" || info.PageCount != 3 {
				t.Fatalf("ComicInfo %+v", info)
			}

			// The browser download offers the Unicode name with an ASCII fallback.
			download := client.Get(base + "/jobs/" + id + "/download")
			header := download.Header.Get("Content-Disposition")
			if !strings.Contains(header, `filename="mangaveyra-ch-001.cbz"`) || !strings.Contains(header, "filename*=UTF-8''%E6%BD%AE%E9%A8%92") {
				t.Fatalf("Content-Disposition %q", header)
			}
			if _, params, err := mime.ParseMediaType(header); err != nil || params["filename"] != result.Filename {
				t.Fatalf("parsed disposition %v %v", params, err)
			}

			// The finished job's settings stay in the form instead of resetting to PDF.
			ready := string(client.Get(base + "?page=" + pages[0].ID + "&job=" + id).Body)
			if !regexp.MustCompile(`value="cbz"[^>]*checked`).MatchString(ready) || !strings.Contains(ready, `value="1-2"`) {
				t.Fatal("job settings were not restored in the form")
			}
		})
	}
}
