package export_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
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

func TestRealPagesToQueuedExportAndDownload(t *testing.T) {
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			ta := testutil.NewTestApp(t, testutil.WithDialect(dialect))
			ctx := context.Background()
			ss := core.MustUse[studio.Service](ta.Deps)
			ps := core.MustUse[pagestore.Service](ta.Deps)
			actor := studio.Actor{UserID: ta.Admin.ID}
			project, err := ss.CreateProject(ctx, actor, studio.NewProject{Title: "Tidebreak Courier", PageWidth: 200, PageHeight: 300})
			if err != nil {
				t.Fatal(err)
			}
			ch, err := ss.CreateChapter(ctx, actor, project.ID, "The Lighthouse Debt")
			if err != nil {
				t.Fatal(err)
			}
			page := pagedoc.New(db.NewID(), project.ID, ch.ID, 1, 200, 300, "rtl")
			page.Panels = []pagedoc.Panel{{ID: db.NewID(), Order: 0, PromptSource: "user", Points: pagedoc.RectPoints(pagedoc.Rect{X: 10, Y: 10, W: 180, H: 280}), Layers: []pagedoc.Layer{}}}
			page.Bubbles = []pagedoc.Bubble{{ID: db.NewID(), PanelID: page.Panels[0].ID, Kind: "speech", Shape: "ellipse", Box: pagedoc.Rect{X: 30, Y: 30, W: 140, H: 100}, Style: pagedoc.BubbleStyle{Size: 24, AutoFit: true}, Texts: map[string]pagedoc.BubbleText{"en": {Text: "The ferry awaits.", Status: "approved", Source: "human"}}}}
			err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
				_, err := ps.CreatePagesTx(ctx, tx, ch.ID, []pagedoc.Page{page}, pagestore.Change{ActorKind: "user", ActorID: actor.UserID, Action: "create"})
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			// Export is a read action: approved, locked pages remain exportable.
			if err = ps.SetLock(ctx, page.ID, "", "", true, pagestore.Change{ActorKind: "user", ActorID: actor.UserID, Action: "lock"}); err != nil {
				t.Fatal(err)
			}
			client := ta.Login(t, ta.Admin.Username)
			base := "/studio/p/" + project.ID + "/chapters/" + strconv.Itoa(ch.Number) + "/export"
			resp := client.Get(base)
			if resp.StatusCode != http.StatusOK || !bytes.Contains(resp.Body, []byte("CBZ comic archive")) {
				t.Fatalf("E2 GET: %d %s", resp.StatusCode, resp.Body)
			}
			preview := client.Get(base + "/preview/" + page.ID)
			if preview.StatusCode != http.StatusOK || preview.Header.Get("Content-Type") != "image/png" {
				t.Fatalf("preview: %d %s", preview.StatusCode, preview.Body)
			}
			form := url.Values{"format": {"cbz"}, "trim_id": {"b5"}, "dpi": {"72"}, "lang": {"en"}, "pages": {"all"}, "credits_page": {"on"}}
			resp = client.Do(http.MethodPost, base, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
			if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusFound {
				t.Fatalf("export submit: %d %s", resp.StatusCode, resp.Body)
			}
			loc, err := url.Parse(resp.Header.Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
			id := loc.Query().Get("job")
			if id == "" {
				t.Fatal("job redirect missing")
			}
			if ran, err := ta.Deps.Executor.RunOnce(ctx); err != nil || !ran {
				t.Fatal("export executor", ran, err)
			}
			j, err := ta.Deps.Jobs.Get(ctx, id)
			if err != nil || j.State != jobs.StateDone {
				t.Fatalf("job result: %+v %v", j, err)
			}
			var result chapterexport.Result
			if err = json.Unmarshal(j.Output, &result); err != nil {
				t.Fatal(err)
			}
			a := assets.From(ta.Deps)
			data, _, err := a.Read(ctx, result.AssetID, 0)
			if err != nil {
				t.Fatal(err)
			}
			z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil || len(z.File) != 3 {
				t.Fatalf("CBZ pages+credits+metadata: %v %v", z, err)
			}
			resp = client.Get(base + "?job=" + id)
			if resp.StatusCode != http.StatusOK || !bytes.Contains(resp.Body, []byte("Download export")) {
				t.Fatalf("ready page: %d %s", resp.StatusCode, resp.Body)
			}
			download := client.Get(result.URL)
			if download.StatusCode != http.StatusOK || !bytes.Equal(download.Body, data) {
				t.Fatal("signed download failed", download.StatusCode)
			}
			downloadPath := base + "/jobs/" + id + "/download"
			if !bytes.Contains(resp.Body, []byte(downloadPath)) {
				t.Fatal("browser download must use the named export endpoint")
			}
			download = client.Get(downloadPath)
			disposition, params, err := mime.ParseMediaType(download.Header.Get("Content-Disposition"))
			if err != nil || disposition != "attachment" || params["filename"] != result.Filename || !strings.HasSuffix(params["filename"], ".cbz") {
				t.Fatalf("export filename = %q, %v", download.Header.Get("Content-Disposition"), err)
			}
			if download.StatusCode != http.StatusOK || download.Header.Get("Content-Type") != result.ContentType || !bytes.Equal(download.Body, data) {
				t.Fatal("named download failed", download.StatusCode)
			}
			other, outsider := ta.UserClient(t, "creator")
			_ = other
			if got := outsider.Get(base); got.StatusCode != http.StatusNotFound {
				t.Fatalf("cross-project E2 leak: %d", got.StatusCode)
			}
			if got := outsider.Get(downloadPath); got.StatusCode != http.StatusNotFound {
				t.Fatalf("cross-project download leak: %d", got.StatusCode)
			}
		})
	}
}
