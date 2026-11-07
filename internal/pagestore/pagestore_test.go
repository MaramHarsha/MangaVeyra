package pagestore_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

func forEach(t *testing.T, fn func(*testing.T, *testutil.TestApp)) {
	t.Helper()
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) { fn(t, testutil.NewTestApp(t, testutil.WithDialect(dialect))) })
	}
}

type fixture struct {
	ta      *testutil.TestApp
	studio  studio.Service
	pages   pagestore.Service
	actor   studio.Actor
	project studio.Project
	chapter studio.Chapter
	change  pagestore.Change
}

func newFixture(t *testing.T, ta *testutil.TestApp) fixture {
	t.Helper()
	ctx := context.Background()
	s := core.MustUse[studio.Service](ta.Deps)
	actor, err := s.ActorFromUser(ctx, ta.Admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.CreateProject(ctx, actor, studio.NewProject{Title: "Tidebreak Courier", Premise: "A courier crosses a flooded city.",
		Style: "bw_screentone", Format: "manga_rtl", PageSize: "b5", Language: "ja", PageWidth: 400, PageHeight: 600})
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.CreateChapter(ctx, actor, p.ID, "The Tide Gate")
	if err != nil {
		t.Fatal(err)
	}
	return fixture{ta: ta, studio: s, pages: core.MustUse[pagestore.Service](ta.Deps), actor: actor, project: p, chapter: c,
		change: pagestore.Change{ActorKind: "user", ActorID: actor.UserID, Action: "save", Summary: "Page changes"}}
}

func (f fixture) doc() pagedoc.Page {
	p := pagedoc.New("new-page", f.project.ID, f.chapter.ID, 1, 400, 600, pagedoc.DirRTL)
	p.Panels = []pagedoc.Panel{{ID: "new-panel", Points: pagedoc.RectPoints(pagedoc.Rect{X: 20, Y: 30, W: 360, H: 540}), Prompt: "Rain over the tide gate", PromptSource: "script",
		Layers: []pagedoc.Layer{{ID: "new-background", Kind: pagedoc.LayerBackground, Visible: true, Opacity: 1, Provenance: pagedoc.ProvenanceAI},
			{ID: "new-lettering", Kind: pagedoc.LayerLettering, Visible: true, Opacity: 1, Provenance: pagedoc.ProvenanceHuman}}}}
	p.Bubbles = []pagedoc.Bubble{{ID: "new-bubble", PanelID: "new-panel", Kind: pagedoc.BubbleSpeech, Shape: pagedoc.ShapeEllipse,
		Box: pagedoc.Rect{X: 220, Y: 40, W: 140, H: 100}, Tail: pagedoc.Tail{Enabled: true, Tip: pagedoc.Point{X: 290, Y: 200}}, Style: pagedoc.BubbleStyle{AutoFit: true}, Vertical: true,
		Texts: map[string]pagedoc.BubbleText{"ja": {Text: "海へ行こう", Status: "draft", Source: "human", Ruby: []pagedoc.Ruby{{Start: 0, End: 1, Text: "うみ"}}}}}}
	return p
}

func (f fixture) create(t *testing.T, n int) []pagedoc.Page {
	t.Helper()
	var out []pagedoc.Page
	err := f.ta.Deps.DB.WithTx(context.Background(), func(tx *db.Tx) error {
		docs := make([]pagedoc.Page, n)
		for i := range docs {
			docs[i] = f.doc()
		}
		var err error
		out, err = f.pages.CreatePagesTx(context.Background(), tx, f.chapter.ID, docs, f.change)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f fixture) image(t *testing.T, projectID string) assets.Asset {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 32, 48))
	img.Set(5, 5, color.RGBA{R: 70, G: 90, B: 120, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	a, err := assets.From(f.ta.Deps).Save(context.Background(), assets.SaveParams{OwnerID: f.actor.UserID, ProjectID: projectID, Kind: "layer", Data: b.Bytes()})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestPageRoundTripVersionsRubyAndSignedURLs(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx, f := context.Background(), newFixture(t, ta)
		p := f.create(t, 1)[0]
		if !db.ValidID(p.ID) || !db.ValidID(p.Panels[0].ID) || p.Bubbles[0].PanelID != p.Panels[0].ID {
			t.Fatal("temporary IDs or panel ancestry escaped normalization")
		}
		loaded, err := f.pages.GetPage(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Direction != pagedoc.DirRTL || loaded.Version != 1 || loaded.Bubbles[0].Shape != pagedoc.ShapeEllipse || loaded.Bubbles[0].Texts["ja"].Ruby[0].Text != "うみ" {
			t.Fatalf("page lost data: %+v", loaded)
		}
		if loaded.Panels[0].Points[0].X != 20 || loaded.Bubbles[0].Box.X != 220 {
			t.Fatal("database percentage mapping changed pixel geometry")
		}
		a := f.image(t, f.project.ID)
		loaded.Panels[0].Layers[0].Asset = &pagedoc.AssetRef{ID: a.ID, URL: "https://never-persist.invalid/private"}
		saved, err := f.pages.SavePage(ctx, loaded, loaded.Version, f.change)
		if err != nil {
			t.Fatal(err)
		}
		if saved.Version != 2 || saved.Panels[0].Layers[0].Asset.URL != "" {
			t.Fatal("bad revision or persisted response URL")
		}
		f.pages.WithURLs(ctx, &saved, 0)
		if !strings.HasPrefix(saved.Panels[0].Layers[0].Asset.URL, "/media/") {
			t.Fatal("stored art has no signed URL")
		}
		versions, err := f.pages.Versions(ctx, p.ID, 10)
		if err != nil || len(versions) != 2 || versions[0].Number != 2 {
			t.Fatalf("history=%+v %v", versions, err)
		}
		snap, err := f.pages.VersionDoc(ctx, versions[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(snap)
		if bytes.Contains(encoded, []byte("/media/")) || bytes.Contains(encoded, []byte("never-persist")) {
			t.Fatal("snapshot contains expiring URL")
		}
		if _, err := f.pages.SavePage(ctx, loaded, 1, f.change); !errors.Is(err, pagestore.ErrVersion) {
			t.Fatalf("stale save=%v", err)
		}
		first, err := f.pages.Revert(ctx, p.ID, versions[1].ID, f.change)
		if err != nil || first.Version != 3 || first.Panels[0].Layers[0].Asset != nil {
			t.Fatalf("revert=%+v %v", first, err)
		}
		if first.Bubbles[0].Texts["ja"].Text != "海へ行こう" {
			t.Fatal("revert lost Japanese text")
		}
	})
}

func TestLockedDescendantsAndHumanOnlyUnlock(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx, f := context.Background(), newFixture(t, ta)
		p := f.create(t, 1)[0]
		pn := p.Panels[0]
		if err := f.pages.SetLock(ctx, p.ID, pn.ID, pn.Layers[0].ID, true, f.change); err != nil {
			t.Fatal(err)
		}
		p, _ = f.pages.GetPage(ctx, p.ID)
		p.Panels[0].Layers[0].Prompt = "replace locked art"
		if _, err := f.pages.SavePage(ctx, p, p.Version, f.change); !errors.Is(err, pagestore.ErrLocked) {
			t.Fatalf("locked layer changed: %v", err)
		}
		p, _ = f.pages.GetPage(ctx, p.ID)
		p.Note = "An unrelated production note"
		if _, err := f.pages.SavePage(ctx, p, p.Version, f.change); err != nil {
			t.Fatalf("unrelated edit blocked: %v", err)
		}
		agent := f.change
		agent.ActorKind = "agent"
		if err := f.pages.SetLock(ctx, p.ID, pn.ID, pn.Layers[0].ID, false, agent); !errors.Is(err, studio.ErrForbidden) {
			t.Fatal("agent unlocked art")
		}
		if err := f.pages.SetLock(ctx, p.ID, pn.ID, pn.Layers[0].ID, false, f.change); err != nil {
			t.Fatal(err)
		}
		if err := f.pages.SetLock(ctx, p.ID, pn.ID, pn.Layers[1].ID, true, f.change); err != nil {
			t.Fatal(err)
		}
		p, _ = f.pages.GetPage(ctx, p.ID)
		err := ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
			b := p.Bubbles
			b[0].Texts["ja"] = pagedoc.BubbleText{Text: "変更", Source: "human", Status: "draft"}
			return f.pages.SetBubblesTx(ctx, tx, p.ID, b, f.change)
		})
		if !errors.Is(err, pagestore.ErrLocked) {
			t.Fatalf("locked lettering changed: %v", err)
		}
		if err := f.pages.SetLock(ctx, p.ID, pn.ID, "", true, f.change); err != nil {
			t.Fatal(err)
		}
		p, _ = f.pages.GetPage(ctx, p.ID)
		p.Panels[0].Locked = false
		p.Panels[0].Prompt = "try unlock through save"
		if _, err := f.pages.SavePage(ctx, p, p.Version, f.change); !errors.Is(err, pagestore.ErrLocked) {
			t.Fatal("save bypassed panel lock")
		}
		if err := f.pages.DeletePage(ctx, p.ID, f.change); !errors.Is(err, pagestore.ErrLocked) {
			t.Fatal("delete bypassed descendant lock")
		}
		if err := f.pages.SetLock(ctx, p.ID, "", "", true, f.change); err != nil {
			t.Fatal(err)
		}
		if err := f.pages.IsWritable(ctx, p.ID, "", ""); !errors.Is(err, pagestore.ErrLocked) {
			t.Fatal("page lock not enforced")
		}
	})
}

func TestPageAuthorizationForeignAssetsAndAtomicReplacement(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx, f := context.Background(), newFixture(t, ta)
		p := f.create(t, 1)[0]
		other := ta.CreateUser(t, "", auth.RoleCreator)
		ch := f.change
		ch.ActorID = other.ID
		if _, err := f.pages.SavePage(ctx, p, p.Version, ch); !errors.Is(err, studio.ErrForbidden) {
			t.Fatalf("foreign save=%v", err)
		}
		foreign := newFixture(t, ta)
		art := foreign.image(t, foreign.project.ID)
		p.Panels[0].Layers[0].Asset = &pagedoc.AssetRef{ID: art.ID}
		if _, err := f.pages.SavePage(ctx, p, p.Version, f.change); !errors.Is(err, pagestore.ErrInvalid) {
			t.Fatalf("foreign image=%v", err)
		}
		err := ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
			bad := f.doc()
			bad.Bubbles[0].Box.W = -1
			_, err := f.pages.ReplaceChapterPagesTx(ctx, tx, f.chapter.ID, []pagedoc.Page{bad}, f.change)
			return err
		})
		if !errors.Is(err, pagestore.ErrInvalid) {
			t.Fatalf("bad replacement=%v", err)
		}
		still, err := f.pages.ChapterPages(ctx, f.chapter.ID)
		if err != nil || len(still) != 1 || still[0].ID != p.ID {
			t.Fatal("failed replacement erased previous pages")
		}
		if err := f.pages.SetLock(ctx, p.ID, p.Panels[0].ID, "", true, f.change); err != nil {
			t.Fatal(err)
		}
		err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
			_, err := f.pages.ReplaceChapterPagesTx(ctx, tx, f.chapter.ID, []pagedoc.Page{f.doc()}, f.change)
			return err
		})
		if !errors.Is(err, pagestore.ErrLocked) {
			t.Fatalf("replacement ignored descendant lock=%v", err)
		}
	})
}

func TestPageOrderingHistoryAndOnlyHumanDelete(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx, f := context.Background(), newFixture(t, ta)
		pages := f.create(t, 3)
		order := []string{pages[2].ID, pages[0].ID, pages[1].ID}
		if err := f.pages.ReorderPages(ctx, f.chapter.ID, order, f.change); err != nil {
			t.Fatal(err)
		}
		list, err := f.pages.ListPages(ctx, f.chapter.ID)
		if err != nil || len(list) != 3 || list[0].ID != order[0] || list[0].Number != 1 || list[0].Version != 2 {
			t.Fatalf("order=%+v %v", list, err)
		}
		if err := f.pages.ReorderPages(ctx, f.chapter.ID, []string{order[0], order[0], order[1]}, f.change); !errors.Is(err, pagestore.ErrInvalid) {
			t.Fatal("duplicate reorder accepted")
		}
		agent := f.change
		agent.ActorKind = "agent"
		if err := f.pages.DeletePage(ctx, order[0], agent); !errors.Is(err, studio.ErrForbidden) {
			t.Fatal("agent deleted a page")
		}
		if err := f.pages.DeletePage(ctx, order[0], f.change); err != nil {
			t.Fatal(err)
		}
		list, err = f.pages.ListPages(ctx, f.chapter.ID)
		if err != nil || len(list) != 2 || list[0].Number != 1 || list[1].Number != 2 {
			t.Fatalf("renumbered=%+v %v", list, err)
		}
	})
}

func TestConcurrentPageSavesHaveOneWinner(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx, f := context.Background(), newFixture(t, ta)
		p := f.create(t, 1)[0]
		start := make(chan struct{})
		results := make(chan error, 8)
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				<-start
				copy := p
				copy.Note = "Concurrent edit"
				_, err := f.pages.SavePage(ctx, copy, p.Version, f.change)
				results <- err
			})
		}
		close(start)
		wg.Wait()
		close(results)
		wins, conflicts := 0, 0
		for err := range results {
			if err == nil {
				wins++
			} else if errors.Is(err, pagestore.ErrVersion) {
				conflicts++
			} else {
				t.Fatal(err)
			}
		}
		if wins != 1 || conflicts != 7 {
			t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
		}
		versions, err := f.pages.Versions(ctx, p.ID, 100)
		if err != nil || len(versions) != 2 {
			t.Fatalf("history=%+v %v", versions, err)
		}
	})
}
