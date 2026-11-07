package pagestore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func chapterLabels(t *testing.T, f fixture) map[string]string {
	t.Helper()
	chapter, err := f.studio.GetChapter(context.Background(), f.chapter.ID)
	if err != nil {
		t.Fatal(err)
	}
	return chapter.AILabel
}

func TestChapterArtProvenanceUsesAllPagesAndTracksDeletion(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f := newFixture(t, ta)
		pages := f.create(t, 2)
		if labels := chapterLabels(t, f); labels["art"] != "" || labels["lettering"] != "human" {
			t.Fatalf("empty art invented provenance: %+v", labels)
		}
		a := f.image(t, f.project.ID)
		for i, provenance := range []string{pagedoc.ProvenanceHuman, pagedoc.ProvenanceAI} {
			page := pages[i]
			err := ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
				if err := f.studio.SetChapterAILabelTx(ctx, tx, f.chapter.ID, "script", "ai"); err != nil {
					return err
				}
				_, err := f.pages.SetLayerAssetTx(ctx, tx, pagestore.SetLayerAsset{PageID: page.ID, PanelID: page.Panels[0].ID,
					LayerID: page.Panels[0].Layers[0].ID, Kind: pagedoc.LayerBackground, AssetID: a.ID, Provenance: provenance}, f.change)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		if labels := chapterLabels(t, f); labels["art"] != "ai_assisted" || labels["script"] != "ai" {
			t.Fatalf("mixed chapter labels: %+v", labels)
		}
		if err := f.pages.DeletePage(ctx, pages[1].ID, f.change); err != nil {
			t.Fatal(err)
		}
		if labels := chapterLabels(t, f); labels["art"] != "human" || labels["script"] != "ai" {
			t.Fatalf("deleted AI art still affects label: %+v", labels)
		}
		if err := f.pages.DeletePage(ctx, pages[0].ID, f.change); err != nil {
			t.Fatal(err)
		}
		if labels := chapterLabels(t, f); labels["art"] != "" || labels["lettering"] != "" || labels["script"] != "ai" {
			t.Fatalf("empty chapter labels: %+v", labels)
		}
	})
}

func TestHumanLetteringChangesRetainAIParticipationAndRevert(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f := newFixture(t, ta)
		doc := f.doc()
		text := doc.Bubbles[0].Texts["ja"]
		text.Source = pagedoc.ProvenanceAI
		doc.Bubbles[0].Texts["ja"] = text
		var page pagedoc.Page
		err := ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
			pages, err := f.pages.CreatePagesTx(ctx, tx, f.chapter.ID, []pagedoc.Page{doc}, f.change)
			if err == nil {
				page = pages[0]
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		versions, err := f.pages.Versions(ctx, page.ID, 10)
		if err != nil {
			t.Fatal(err)
		}
		original := versions[0].ID
		if labels := chapterLabels(t, f); labels["lettering"] != "ai" {
			t.Fatalf("generated lettering = %+v", labels)
		}
		// Geometry and styling are human contributions even with unchanged AI text.
		page.Bubbles[0].Box.X += 8
		page.Bubbles[0].Style.Rotate = 5
		page, err = f.pages.SavePage(ctx, page, page.Version, f.change)
		if err != nil {
			t.Fatal(err)
		}
		if labels := chapterLabels(t, f); labels["lettering"] != "ai_assisted" {
			t.Fatalf("human geometry contribution lost: %+v", labels)
		}
		// A complete rewrite changes text source, while retaining AI placement.
		text = page.Bubbles[0].Texts["ja"]
		text.Text, text.Source = "潮の門へ！", pagedoc.ProvenanceHuman
		page.Bubbles[0].Texts["ja"] = text
		page, err = f.pages.SavePage(ctx, page, page.Version, f.change)
		if err != nil {
			t.Fatal(err)
		}
		page.Panels[0].Prompt = "A later art-only change"
		page, err = f.pages.SavePage(ctx, page, page.Version, f.change)
		if err != nil {
			t.Fatal(err)
		}
		if labels := chapterLabels(t, f); labels["lettering"] != "ai_assisted" {
			t.Fatalf("mixed lettering evidence lost on later save: %+v", labels)
		}
		ch := f.change
		ch.Action = "revert"
		if _, err := f.pages.Revert(ctx, page.ID, original, ch); err != nil {
			t.Fatal(err)
		}
		if labels := chapterLabels(t, f); labels["lettering"] != "ai" {
			t.Fatalf("revert did not restore recorded provenance: %+v", labels)
		}
		page, err = f.pages.GetPage(ctx, page.ID)
		if err != nil {
			t.Fatal(err)
		}
		humanBubble := page.Bubbles[0]
		page.Bubbles = nil
		page, err = f.pages.SavePage(ctx, page, page.Version, f.change)
		if err != nil {
			t.Fatal(err)
		}
		humanBubble.ID = db.NewID()
		text = humanBubble.Texts["ja"]
		text.Text, text.Source = "人が新しく書いた台詞。", pagedoc.ProvenanceHuman
		humanBubble.Texts["ja"] = text
		page.Bubbles = []pagedoc.Bubble{humanBubble}
		if _, err := f.pages.SavePage(ctx, page, page.Version, f.change); err != nil {
			t.Fatal(err)
		}
		if labels := chapterLabels(t, f); labels["lettering"] != "human" {
			t.Fatalf("new human lettering inherited deleted AI layout: %+v", labels)
		}
	})
}

func TestChapterProvenanceIsAtomicAndIgnoresEmptyLettering(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f := newFixture(t, ta)
		page := f.create(t, 1)[0]
		rolledBack := errors.New("abort mutation")
		err := ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
			bubbles := page.Bubbles
			text := bubbles[0].Texts["ja"]
			text.Source = pagedoc.ProvenanceAI
			bubbles[0].Texts["ja"] = text
			if err := f.pages.SetBubblesTx(ctx, tx, page.ID, bubbles, pagestore.Change{ActorKind: "system", ActorID: f.actor.UserID, Action: "letter"}); err != nil {
				return err
			}
			return rolledBack
		})
		if !errors.Is(err, rolledBack) {
			t.Fatal(err)
		}
		if labels := chapterLabels(t, f); labels["lettering"] != "human" {
			t.Fatalf("rolled-back provenance committed: %+v", labels)
		}
		page, err = f.pages.GetPage(ctx, page.ID)
		if err != nil {
			t.Fatal(err)
		}
		text := page.Bubbles[0].Texts["ja"]
		text.Text = " "
		page.Bubbles[0].Texts["ja"] = text
		if _, err := f.pages.SavePage(ctx, page, page.Version, f.change); err != nil {
			t.Fatal(err)
		}
		if labels := chapterLabels(t, f); labels["lettering"] != "" {
			t.Fatalf("empty lettering invented a role: %+v", labels)
		}
	})
}
