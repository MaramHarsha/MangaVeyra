package export_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/compose"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

func TestExportPreviewUsesProjectAndRequestedLanguages(t *testing.T) {
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			ta := testutil.NewTestApp(t, testutil.WithDialect(dialect))
			ctx := context.Background()
			ss, ps := core.MustUse[studio.Service](ta.Deps), core.MustUse[pagestore.Service](ta.Deps)
			actor := studio.Actor{UserID: ta.Admin.ID}
			project, err := ss.CreateProject(ctx, actor, studio.NewProject{Title: "Tidebreak Courier", PageWidth: 200, PageHeight: 300, Language: "ja"})
			if err != nil {
				t.Fatal(err)
			}
			chapter, err := ss.CreateChapter(ctx, actor, project.ID, "The crossing")
			if err != nil {
				t.Fatal(err)
			}
			p := pagedoc.New(db.NewID(), project.ID, chapter.ID, 1, 200, 300, "rtl")
			p.Panels = []pagedoc.Panel{{ID: db.NewID(), Points: pagedoc.RectPoints(pagedoc.Rect{X: 10, Y: 10, W: 180, H: 280}), PromptSource: "user"}}
			p.Bubbles = []pagedoc.Bubble{{ID: db.NewID(), PanelID: p.Panels[0].ID, Kind: "speech", Shape: "ellipse", Box: pagedoc.Rect{X: 30, Y: 40, W: 140, H: 100},
				Style: pagedoc.BubbleStyle{Size: 24, AutoFit: true}, Texts: map[string]pagedoc.BubbleText{"en": {Text: "The ferry awaits.", Source: "human", Status: "approved"}, "ja": {Text: "海へ行こう", Source: "human", Status: "approved"}}}}
			err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
				_, err := ps.CreatePagesTx(ctx, tx, chapter.ID, []pagedoc.Page{p}, pagestore.Change{ActorKind: "user", ActorID: actor.UserID, Action: "create"})
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			p, err = ps.GetPage(ctx, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			base := fmt.Sprintf("/studio/p/%s/chapters/%d/export", project.ID, chapter.Number)
			client := ta.Login(t, ta.Admin.Username)
			renderer := core.MustUse[compose.Renderer](ta.Deps)
			for _, lang := range []string{"", "ja", "en"} {
				want := lang
				if want == "" {
					want = "ja"
				}
				response := client.Get(base + "/preview/" + p.ID + "?lang=" + lang)
				expected, err := renderer.PNG(ctx, p, compose.Options{Lang: want, Scale: 1})
				if err != nil || response.StatusCode != http.StatusOK || !bytes.Equal(response.Body, expected) {
					t.Fatalf("preview lang=%q: %d %v", lang, response.StatusCode, err)
				}
			}
			if got := client.Get(base + "/preview/" + p.ID + "?lang=fr"); got.StatusCode != http.StatusBadRequest {
				t.Fatalf("unsupported preview language: %d", got.StatusCode)
			}
			screen := client.Get(base + "?lang=en")
			if screen.StatusCode != http.StatusOK || !strings.Contains(string(screen.Body), "data-export-preview") ||
				!strings.Contains(string(screen.Body), "data-export-language=\"true\"") || !strings.Contains(string(screen.Body), "/preview/"+p.ID+"?lang=en") {
				t.Fatalf("language controls are not bound to preview images: %d %s", screen.StatusCode, screen.Body)
			}
		})
	}
}
