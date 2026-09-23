package studio_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestPlaceholderScriptIsDeterministicAndWellFormed(t *testing.T) {
	in := studio.OutlineInput{Premise: "A gull steals one letter a day from the harbour post office.", Genres: []string{"comedy"}, Chapters: 12}
	a, b := studio.PlaceholderScript(in), studio.PlaceholderScript(in)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("placeholder script is not deterministic")
	}
	if !a.Valid() || len(a.Characters) < 4 || len(a.Settings) < 3 || len(a.Chapter1.Scenes) != 3 {
		t.Fatalf("draft: %+v", a)
	}
	n := 0
	for _, arc := range a.Arcs {
		if arc.Title == "" || strings.HasPrefix(arc.Title, "Arc ") {
			t.Fatalf("arc title: %q", arc.Title)
		}
		for _, c := range arc.Chapters {
			n++
			if c.Number != n || c.Title == "" || c.Summary == "" {
				t.Fatalf("chapter %d: %+v", n, c)
			}
		}
	}
	if n != 12 {
		t.Fatalf("chapters: %d", n)
	}
	for _, sc := range a.Chapter1.Scenes {
		if sc.Title == "" || sc.Location == "" || len(sc.Panels) < 4 {
			t.Fatalf("scene: %+v", sc)
		}
		for _, p := range sc.Panels {
			if p.Label == "" || p.Camera == "" || p.Action == "" {
				t.Fatalf("panel: %+v", p)
			}
		}
	}
	// A story template sets the structure.
	st := studio.PlaceholderScript(studio.OutlineInput{Premise: "x", Structure: []studio.StoryArcSpec{{Title: "Setup", Chapters: 2}, {Title: "Finale", Chapters: 1}}})
	if len(st.Arcs) != 2 || st.Arcs[0].Title != "Setup" || len(st.Arcs[1].Chapters) != 1 || st.Arcs[1].Chapters[0].Number != 3 {
		t.Fatalf("structured: %+v", st.Arcs)
	}
	// The output contract round-trips through JSON as the job output.
	raw, _ := json.Marshal(a)
	var back studio.ScriptDraft
	if err := json.Unmarshal(raw, &back); err != nil || !reflect.DeepEqual(a, back) {
		t.Fatalf("json round trip: %v", err)
	}
}

func TestApplyScriptDraftAndPagesFromScript(t *testing.T) { testApplyScriptDraftAndPagesFromScript(t) }

// TestApplyScriptDraftAndPagesFromScriptPostgres runs on Postgres (skipped unless MV_TEST_POSTGRES_URL is set).
func TestApplyScriptDraftAndPagesFromScriptPostgres(t *testing.T) {
	testApplyScriptDraftAndPagesFromScript(t, testutil.WithPostgres())
}

func testApplyScriptDraftAndPagesFromScript(t *testing.T, opts ...testutil.Option) {
	e := newEnv(t, opts...)
	p := e.project(t, "Tidebreak Courier")
	d := studio.PlaceholderScript(studio.OutlineInput{Premise: p.Premise, Chapters: 6})
	if err := e.svc.ApplyScriptDraft(e.ctx, p.ID, d); err != nil {
		t.Fatal(err)
	}
	// Applying twice changes nothing.
	if err := e.svc.ApplyScriptDraft(e.ctx, p.ID, d); err != nil {
		t.Fatal(err)
	}
	chs, _ := e.svc.Chapters(e.ctx, p.ID)
	chars, _ := e.svc.Characters(e.ctx, p.ID, "")
	locs, _ := e.svc.SettingItems(e.ctx, p.ID, studio.SettingLocation)
	arcs, _ := e.svc.Arcs(e.ctx, p.ID)
	if len(chs) != 6 || len(chars) != len(d.Characters) || len(locs) != 3 || len(arcs) != len(d.Arcs) {
		t.Fatalf("applied: %d chapters, %d characters, %d locations, %d arcs", len(chs), len(chars), len(locs), len(arcs))
	}
	if chs[0].Status != studio.ChapterScripting || chs[0].AILabel.V.Script != studio.LabelAI {
		t.Fatalf("chapter 1: %+v", chs[0])
	}
	scenes, _ := e.svc.Scenes(e.ctx, chs[0].ID)
	script, _ := e.svc.ScriptPanels(e.ctx, chs[0].ID)
	if len(scenes) != 3 || len(script) < 12 || scenes[0].LocationID == "" || scenes[0].Prose == "" {
		t.Fatalf("script: %d scenes, %d panels", len(scenes), len(script))
	}
	withSpeaker := 0
	for _, sp := range script {
		for _, l := range sp.Dialogue.V {
			if l.CharacterID != "" {
				withSpeaker++
			}
		}
	}
	if withSpeaker == 0 {
		t.Fatal("dialogue not linked to characters")
	}
	pr, _ := e.svc.Project(e.ctx, p.ID)
	if pr.ChaptersPlanned < 6 {
		t.Fatalf("chapters planned: %d", pr.ChaptersPlanned)
	}

	// Make pages from the script.
	res, err := e.svc.MakePagesFromScript(userCtx(e.owner), studio.UserActor(e.owner.ID), chs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Created) == 0 {
		t.Fatalf("no pages: %+v", res)
	}
	pages, _ := e.svc.Pages(e.ctx, chs[0].ID)
	total := 0
	for _, pg := range pages {
		doc, _ := e.svc.LoadPageDoc(e.ctx, pg.ID)
		total += len(doc.Panels)
		for _, pn := range doc.Panels {
			if pn.ScriptPanelID == "" || pn.Prompt == "" || len(pn.Layers) < 2 || pn.Layers[0].Kind != studio.LayerBackground {
				t.Fatalf("panel from script: %+v", pn)
			}
		}
		if vs, _ := e.svc.Versions(e.ctx, pg.ID, "", 5); len(vs) != 1 || vs[0].Action != "script" {
			t.Fatalf("page version: %+v", vs)
		}
		for _, b := range doc.Bubbles {
			if len(b.Texts) != 1 || b.Texts[0].Text == "" {
				t.Fatalf("bubble: %+v", b)
			}
		}
	}
	if total != len(script) {
		t.Fatalf("panels %d, script %d", total, len(script))
	}
	// Running it again leaves the pages alone.
	res2, _ := e.svc.MakePagesFromScript(userCtx(e.owner), studio.UserActor(e.owner.ID), chs[0].ID)
	if len(res2.Created)+len(res2.Filled) != 0 || len(res2.Skipped) != len(pages) {
		t.Fatalf("second run: %+v", res2)
	}
}

func TestScriptDiffApply(t *testing.T) { testScriptDiffApply(t) }

// TestScriptDiffApplyPostgres runs on Postgres (skipped unless MV_TEST_POSTGRES_URL is set).
func TestScriptDiffApplyPostgres(t *testing.T) { testScriptDiffApply(t, testutil.WithPostgres()) }

func testScriptDiffApply(t *testing.T, opts ...testutil.Option) {
	e := newEnv(t, opts...)
	p := e.project(t, "Ninth Bell")
	a := e.access(t, userCtx(e.owner), p.ID)
	ch, _ := e.svc.CreateChapter(e.ctx, a, "", "One", "")
	sc, _ := e.svc.CreateScene(e.ctx, a, ch.ID, "Rooftops")
	p1, _ := e.svc.CreateScriptPanel(e.ctx, a, sc.ID, "", studio.ScriptInput{PageNumber: 7, Action: "Dusk."})
	p2, _ := e.svc.CreateScriptPanel(e.ctx, a, sc.ID, p1.ID, studio.ScriptInput{Action: "Kaito waits."})
	p3, _ := e.svc.CreateScriptPanel(e.ctx, a, sc.ID, p2.ID, studio.ScriptInput{Action: "Rin vaults the railing."})
	scene, _ := e.svc.SceneScript(e.ctx, sc.ID)
	diff := studio.PlaceholderAssist("Make 7.3 a full-width splash and add a beat before the jump.", scene, func(k string, args ...any) string { return k })
	if len(diff.Ops) != 2 || diff.Ops[0].Op != studio.OpUpdate || diff.Ops[0].PanelID != p3.ID || diff.Ops[1].Op != studio.OpAdd || diff.Affects == "" {
		t.Fatalf("diff: %+v", diff)
	}
	if err := e.svc.ApplyScriptDiff(e.ctx, a, ch.ID, diff); err != nil {
		t.Fatal(err)
	}
	after, _ := e.svc.SceneScript(e.ctx, sc.ID)
	if len(after) != 4 || after[2].Camera != "close" || after[3].ID != p3.ID || after[3].Camera != "wide" || after[3].Label != "7.4" {
		t.Fatalf("after: %+v", after)
	}
	// Remove and move, and stale ids are rejected.
	d2 := studio.ScriptDiff{Ops: []studio.ScriptOp{{Op: studio.OpRemove, PanelID: p2.ID}, {Op: studio.OpMove, PanelID: p1.ID, AfterID: p3.ID, PageNumber: 8}}}
	if err := e.svc.ApplyScriptDiff(e.ctx, a, ch.ID, d2); err != nil {
		t.Fatal(err)
	}
	after, _ = e.svc.SceneScript(e.ctx, sc.ID)
	if len(after) != 3 || after[2].ID != p1.ID || after[2].PageNumber != 8 || after[2].Label != "8.1" {
		t.Fatalf("after remove/move: %+v", after)
	}
	if err := e.svc.ApplyScriptDiff(e.ctx, a, ch.ID, studio.ScriptDiff{Ops: []studio.ScriptOp{{Op: studio.OpRemove, PanelID: p2.ID}}}); err == nil {
		t.Fatal("stale diff applied")
	}
}

func TestPlaceholderJobsEndToEnd(t *testing.T) { testPlaceholderJobsEndToEnd(t) }

// TestPlaceholderJobsEndToEndPostgres runs on Postgres (skipped unless MV_TEST_POSTGRES_URL is set).
func TestPlaceholderJobsEndToEndPostgres(t *testing.T) {
	testPlaceholderJobsEndToEnd(t, testutil.WithPostgres())
}

func testPlaceholderJobsEndToEnd(t *testing.T, opts ...testutil.Option) {
	e := newEnv(t, opts...)
	p := e.project(t, "Low Tide Radio")
	uctx := userCtx(e.owner)
	a := e.access(t, uctx, p.ID)

	j, err := e.svc.EnqueueOutline(uctx, a.Actor, p, studio.OutlineInput{Premise: p.Premise, Chapters: 4})
	if err != nil {
		t.Fatal(err)
	}
	e.runJobs(t)
	done, _ := e.ta.Deps.Jobs.Get(e.ctx, j.ID)
	if done.State != jobs.StateDone {
		t.Fatalf("outline job: %s %+v", done.State, done.Error)
	}
	chs, _ := e.svc.Chapters(e.ctx, p.ID)
	if len(chs) != 4 {
		t.Fatalf("outline not applied: %d chapters", len(chs))
	}

	// Script assistant.
	scenes, _ := e.svc.Scenes(e.ctx, chs[0].ID)
	as, err := e.svc.RequestAssist(uctx, a, chs[0].ID, scenes[0].ID, "add a beat before the jump")
	if err != nil {
		t.Fatal(err)
	}
	e.runJobs(t)
	as, _ = e.svc.Assist(e.ctx, p.ID, as.ID)
	if as.Status != "ready" || len(as.Diff.V.Ops) == 0 {
		t.Fatalf("assist: %+v", as)
	}
	before, _ := e.svc.SceneScript(e.ctx, scenes[0].ID)
	if err := e.svc.ApplyAssist(uctx, a, as.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := e.svc.SceneScript(e.ctx, scenes[0].ID)
	if len(after) != len(before)+1 {
		t.Fatalf("assist not applied: %d → %d", len(before), len(after))
	}
	if err := e.svc.ApplyAssist(uctx, a, as.ID); err == nil {
		t.Fatal("applied twice")
	}

	// Character sheet → pick → train.
	c, err := e.svc.CreateCharacter(e.ctx, a, studio.CharacterInput{Name: "Sora Vell", Role: "antagonist", Look: "Silver bob, grey coat"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Kanji == "" || c.Category != studio.CategorySupporting {
		t.Fatalf("character: %+v", c)
	}
	if _, err := e.svc.TrainLock(uctx, a, c, nil); err == nil {
		t.Fatal("training without references")
	}
	if _, err := e.svc.GenerateSheets(uctx, a, c, "series"); err != nil {
		t.Fatal(err)
	}
	e.runJobs(t)
	c, _ = e.svc.Character(e.ctx, p.ID, c.ID)
	sj, sheets, _ := e.svc.SheetCandidates(e.ctx, c)
	if sj == nil || sj.State != jobs.StateDone || len(sheets) != 4 || sheets[0].AssetID == "" {
		t.Fatalf("sheets: %+v %+v", sj, sheets)
	}
	if err := e.svc.PickSheet(e.ctx, a, c, sheets[2].AssetID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.PickSheet(e.ctx, a, c, "01bogus"); err == nil {
		t.Fatal("picked a foreign asset")
	}
	c, _ = e.svc.Character(e.ctx, p.ID, c.ID)
	if c.PortraitAssetID != sheets[2].AssetID {
		t.Fatal("portrait not set")
	}
	if _, err := e.svc.TrainLock(uctx, a, c, nil); err != nil {
		t.Fatal(err)
	}
	c, _ = e.svc.Character(e.ctx, p.ID, c.ID)
	if c.LockStatus != studio.LockTraining {
		t.Fatalf("status: %s", c.LockStatus)
	}
	e.runJobs(t)
	c, _ = e.svc.Character(e.ctx, p.ID, c.ID)
	if c.LockStatus != studio.LockReady || c.LockScore == nil || *c.LockScore < 0.9 || c.LockInfo.V.Refs != 1 || c.LockInfo.V.TrainedAt == nil {
		t.Fatalf("trained: %+v", c)
	}
	n, err := e.ta.Deps.Notify.UnreadCount(e.ctx, e.owner.ID)
	if err != nil || n == 0 {
		t.Fatalf("no training notification: %v %d", err, n)
	}
}

func TestLayoutsHaveRealGeometry(t *testing.T) {
	e := newEnv(t)
	for _, l := range studio.BuiltinLayouts(e.ctx) {
		if l.Name == "" || l.Badge == "" || len(l.Pages) == 0 {
			t.Fatalf("layout %s", l.ID)
		}
		for _, pg := range l.Pages {
			if len(pg) == 0 {
				t.Fatalf("layout %s has an empty page", l.ID)
			}
			for _, poly := range pg {
				if len(poly) < 3 {
					t.Fatalf("layout %s: %v", l.ID, poly)
				}
				for _, pt := range poly {
					if pt[0] < 0 || pt[0] > 100 || pt[1] < 0 || pt[1] > 100 {
						t.Fatalf("layout %s point %v outside the page", l.ID, pt)
					}
				}
			}
		}
	}
	if chase, _ := studio.BuiltinLayout("builtin:chase"); chase.PanelCount() != 7 {
		t.Fatalf("chase panels: %d", chase.PanelCount())
	}
	for n := 1; n <= 10; n++ {
		polys, _ := studio.SuggestLayout(n, studio.FormatMangaRTL)
		if len(polys) != n {
			t.Fatalf("suggest %d → %d", n, len(polys))
		}
	}
	if polys, id := studio.SuggestLayout(3, studio.FormatWebtoon); len(polys) != 3 || id != "builtin:webtoon_scroll" {
		t.Fatalf("webtoon suggestion: %d %s", len(polys), id)
	}
	if g := studio.GridLayout(3, 3); len(g) != 9 {
		t.Fatalf("grid: %d", len(g))
	}
	if st := studio.BuiltinStories(e.ctx); len(st) != 4 || st[0].Chapters() != 10 || st[0].Arcs[0].Title == "" {
		t.Fatalf("stories: %+v", st)
	}
}
