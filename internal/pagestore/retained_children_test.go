package pagestore_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestSaveKeepsPanelAnchorsAndRetainedChildMetadata(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx, f := context.Background(), newFixture(t, ta)
		p := f.create(t, 1)[0]
		panelID, layerID, bubbleID := p.Panels[0].ID, p.Panels[0].Layers[0].ID, p.Bubbles[0].ID
		issue := db.NewID()
		_, err := ta.Deps.DB.Exec(ctx, `INSERT INTO continuity_issues (id, project_id, chapter_id, page_id, panel_id, kind, message)
			VALUES (?, ?, ?, ?, ?, 'prop', 'Keep the original courier satchel')`, issue, p.ProjectID, p.ChapterID, p.ID, panelID)
		if err != nil {
			t.Fatal(err)
		}
		stamp := db.Now().Add(-24 * time.Hour)
		for _, child := range []struct{ table, id string }{{"panels", panelID}, {"layers", layerID}, {"bubbles", bubbleID}} {
			if _, err := ta.Deps.DB.Exec(ctx, `UPDATE `+child.table+` SET created_at = ? WHERE id = ?`, stamp, child.id); err != nil {
				t.Fatal(err)
			}
		}
		_, err = ta.Deps.DB.Exec(ctx, `UPDATE panels SET review_status = 'approved', living = ? WHERE id = ?`, `{"note":"keep me"}`, panelID)
		if err != nil {
			t.Fatal(err)
		}
		for _, edit := range []string{"note", "lettering"} {
			if edit == "note" {
				p.Note = "An unrelated production note"
			} else {
				p.Bubbles[0].Texts["ja"] = pagedoc.BubbleText{Text: "海へ行こう！", Status: "reviewed", Source: "human"}
			}
			p, err = f.pages.SavePage(ctx, p, p.Version, f.change)
			if err != nil {
				t.Fatal(err)
			}
			var anchor sql.NullString
			if err := ta.Deps.DB.Get(ctx, &anchor, `SELECT panel_id FROM continuity_issues WHERE id = ?`, issue); err != nil || anchor.String != panelID {
				t.Fatalf("%s detached original panel anchor: %+v %v", edit, anchor, err)
			}
			for _, child := range []struct{ table, id string }{{"panels", panelID}, {"layers", layerID}, {"bubbles", bubbleID}} {
				var created time.Time
				if err := ta.Deps.DB.Get(ctx, &created, `SELECT created_at FROM `+child.table+` WHERE id = ?`, child.id); err != nil || !created.Equal(stamp) {
					t.Fatalf("%s was recreated: %v %v", child.table, created, err)
				}
			}
			var status, living string
			_ = ta.Deps.DB.Get(ctx, &status, `SELECT review_status FROM panels WHERE id = ?`, panelID)
			_ = ta.Deps.DB.Get(ctx, &living, `SELECT living FROM panels WHERE id = ?`, panelID)
			if status != "approved" || living != `{"note":"keep me"}` {
				t.Fatal("save erased metadata outside the document")
			}
		}
		// Removing the panel still deletes it and intentionally detaches anchors.
		p.Panels, p.Bubbles = nil, nil
		if _, err = f.pages.SavePage(ctx, p, p.Version, f.change); err != nil {
			t.Fatal(err)
		}
		var anchor sql.NullString
		if err := ta.Deps.DB.Get(ctx, &anchor, `SELECT panel_id FROM continuity_issues WHERE id = ?`, issue); err != nil || anchor.Valid {
			t.Fatalf("removed panel did not detach anchor: %+v %v", anchor, err)
		}
	})
}

func TestSavePrunesRemovedChildrenAndRejectsForeignIDs(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx, f := context.Background(), newFixture(t, ta)
		p := f.create(t, 1)[0]
		other := newFixture(t, ta).create(t, 1)[0]
		for _, child := range []string{"panel", "layer", "bubble"} {
			doc, err := f.pages.GetPage(ctx, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch child {
			case "panel":
				doc.Panels[0].ID = other.Panels[0].ID
				doc.Bubbles[0].PanelID = doc.Panels[0].ID
			case "layer":
				doc.Panels[0].Layers[0].ID = other.Panels[0].Layers[0].ID
			case "bubble":
				doc.Bubbles[0].ID = other.Bubbles[0].ID
			}
			if _, err = f.pages.SavePage(ctx, doc, doc.Version, f.change); !errors.Is(err, pagestore.ErrInvalid) {
				t.Fatalf("foreign %s accepted: %v", child, err)
			}
		}
		removedLayer, removedBubble := p.Panels[0].Layers[0].ID, p.Bubbles[0].ID
		p.Panels[0].Layers = p.Panels[0].Layers[1:]
		p.Bubbles = nil
		if _, err := f.pages.SavePage(ctx, p, p.Version, f.change); err != nil {
			t.Fatal(err)
		}
		for _, child := range []struct{ table, id string }{{"layers", removedLayer}, {"bubbles", removedBubble}} {
			var n int
			if err := ta.Deps.DB.Get(ctx, &n, `SELECT COUNT(*) FROM `+child.table+` WHERE id = ?`, child.id); err != nil || n != 0 {
				t.Fatalf("removed %s still present: %d %v", child.table, n, err)
			}
		}
	})
}
