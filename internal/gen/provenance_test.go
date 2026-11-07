package gen_test

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestGeneratedArtRetainsHumanContributionOnOtherPages(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f, ps := prepareArt(t, ta, 1)
		gen.SetImageServiceForTest(f.gen, &fakeImages{})
		first := createArtPages(t, f, ps, false)[0]
		second := pagedoc.New(db.NewID(), f.project.ID, f.chapter.ID, 0, 1600, 2400, pagedoc.DirRTL)
		second.Panels = []pagedoc.Panel{{ID: db.NewID(), Points: pagedoc.RectPoints(pagedoc.Rect{X: 10, Y: 10, W: 100, H: 100}),
			Layers: []pagedoc.Layer{{ID: db.NewID(), Kind: pagedoc.LayerBackground, Visible: true, Opacity: 1, Provenance: pagedoc.ProvenanceHuman}}}}
		change := pagestore.Change{ActorKind: "user", ActorID: f.owner.ID, Action: "create"}
		err := ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
			pages, err := ps.CreatePagesTx(ctx, tx, f.chapter.ID, []pagedoc.Page{second}, change)
			if err == nil {
				second = pages[0]
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		if err := png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 100, 100))); err != nil {
			t.Fatal(err)
		}
		a, err := assets.From(ta.Deps).Save(ctx, assets.SaveParams{OwnerID: f.owner.ID, ProjectID: f.project.ID, Data: b.Bytes(), Source: assets.SourceUpload})
		if err != nil {
			t.Fatal(err)
		}
		err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
			_, err := ps.SetLayerAssetTx(ctx, tx, pagestore.SetLayerAsset{PageID: second.ID, PanelID: second.Panels[0].ID,
				LayerID: second.Panels[0].Layers[0].ID, Kind: pagedoc.LayerBackground, AssetID: a.ID, Provenance: pagedoc.ProvenanceHuman}, change)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			j, err := f.gen.EnqueuePanel(ctx, f.actor, gen.PanelRequest{ProjectID: f.project.ID, PageID: first.ID,
				PanelID: first.Panels[0].ID, Variations: 1, ExpectedVersion: first.Version})
			if err != nil {
				t.Fatal(err)
			}
			done := runArtJob(t, ta, j.ID)
			if done.State != jobs.StateDone {
				t.Fatalf("job failed: %+v", done.Error)
			}
			chapter, err := f.studio.GetChapter(ctx, f.chapter.ID)
			if err != nil || chapter.AILabel["art"] != "ai_assisted" || chapter.AILabel["lettering"] != "ai" {
				t.Fatalf("mixed-page labels = %+v, %v", chapter.AILabel, err)
			}
			first, err = ps.GetPage(ctx, first.ID)
			if err != nil {
				t.Fatal(err)
			}
		}
	})
}

func TestAutoLetteringWithoutDialogueDoesNotInventProvenance(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f := newGenFixture(t, ta, "", 0)
		doc := studioScript("", "The silent tide gate opens.")
		doc.ChapterID = f.chapter.ID
		err := ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
			_, err := f.studio.ReplaceGeneratedScriptTx(ctx, tx, studio.ReplaceScriptInput{Actor: f.actor,
				ProjectID: f.project.ID, ChapterID: f.chapter.ID, Doc: doc, Provenance: "ai", JobID: db.NewID(),
				ProviderID: "placeholder", ModelID: "placeholder-text"})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		j, err := f.gen.EnqueuePagesFromScript(ctx, f.actor, gen.PagesFromScriptRequest{ProjectID: f.project.ID,
			ChapterID: f.chapter.ID, Letter: true})
		if err != nil {
			t.Fatal(err)
		}
		if done := runArtJob(t, ta, j.ID); done.State != jobs.StateDone {
			t.Fatalf("page job: %+v", done.Error)
		}
		chapter, err := f.studio.GetChapter(ctx, f.chapter.ID)
		if err != nil || chapter.AILabel["lettering"] != "" || chapter.AILabel["art"] != "" || chapter.AILabel["script"] != "ai" {
			t.Fatalf("silent generated chapter provenance = %+v, %v", chapter.AILabel, err)
		}
	})
}
