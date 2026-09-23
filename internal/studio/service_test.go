package studio_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestProjectsMembersAndRoles(t *testing.T) { testProjectsMembers(t) }

// TestProjectsMembersAndRolesPostgres runs on Postgres (skipped unless MV_TEST_POSTGRES_URL is set).
func TestProjectsMembersAndRolesPostgres(t *testing.T) {
	testProjectsMembers(t, testutil.WithPostgres())
}

func testProjectsMembers(t *testing.T, opts ...testutil.Option) {
	e := newEnv(t, opts...)
	p := e.project(t, "Tidebreak Courier")
	if p.Slug != "tidebreak-courier" || p.Format != studio.FormatMangaRTL || p.Visibility != studio.VisibilityPrivate {
		t.Fatalf("project: %+v", p)
	}
	p2 := e.project(t, "Tidebreak Courier")
	if p2.Slug != "tidebreak-courier-2" {
		t.Fatalf("slug: %s", p2.Slug)
	}
	if _, err := e.svc.CreateProject(e.ctx, e.owner.ID, studio.NewProject{Title: " "}); err == nil {
		t.Fatal("empty title accepted")
	}

	owner := e.access(t, userCtx(e.owner), p.ID)
	if !owner.IsOwner() || !owner.Can(studio.EditMembers) || !owner.Can(studio.EditDanger) {
		t.Fatalf("owner access: %+v", owner)
	}
	jun := e.ta.CreateUser(t, "jun", auth.RoleCreator)
	stranger := e.ta.CreateUser(t, "", auth.RoleCreator)
	if _, err := e.svc.Access(userCtx(stranger), studio.ActorFrom(userCtx(stranger)), p.ID); err == nil {
		t.Fatal("stranger has access")
	}
	if _, err := e.svc.AddMember(e.ctx, owner, "nobody", studio.RoleWriter, studio.MemberScope{}); err == nil {
		t.Fatal("unknown user added")
	}
	if _, err := e.svc.AddMember(e.ctx, owner, "@jun", studio.RoleLetterer, studio.MemberScope{Pages: "1-20", Layers: []string{"lettering", "bogus"}}); err != nil {
		t.Fatalf("add member: %v", err)
	}
	if _, err := e.svc.AddMember(e.ctx, owner, "jun", studio.RoleWriter, studio.MemberScope{}); err == nil {
		t.Fatal("duplicate member")
	}
	if _, err := e.svc.AddMember(e.ctx, owner, stranger.Username, studio.RoleOwner, studio.MemberScope{}); err == nil {
		t.Fatal("second owner")
	}
	if _, err := e.svc.AddMember(e.ctx, owner, stranger.Username, studio.RoleViewer, studio.MemberScope{Pages: "x-1"}); err == nil {
		t.Fatal("bad page range accepted")
	}
	ja := e.access(t, userCtx(jun), p.ID)
	if ja.Role != studio.RoleLetterer || ja.Can(studio.EditScript) || !ja.Can(studio.EditLettering) ||
		!ja.CanEditPageNumber(20) || ja.CanEditPageNumber(21) || ja.CanEditLayerKind(studio.LayerBackground) || !ja.CanEditLayerKind(studio.LayerLettering) {
		t.Fatalf("letterer access: %+v", ja)
	}
	if ja.Can(studio.EditMembers) {
		t.Fatal("letterer manages members")
	}
	if err := e.svc.UpdateMember(e.ctx, owner, jun.ID, studio.RoleWriter, studio.MemberScope{}); err != nil {
		t.Fatal(err)
	}
	if !e.svc.CanEdit(e.ctx, &core.User{ID: jun.ID}, p.ID, studio.EditScript) {
		t.Fatal("writer cannot edit script")
	}
	if err := e.svc.RemoveMember(e.ctx, owner, e.owner.ID); err == nil {
		t.Fatal("owner removed")
	}
	members, _ := e.svc.Members(e.ctx, p.ID)
	if len(members) != 2 || members[0].Role != studio.RoleOwner {
		t.Fatalf("members: %+v", members)
	}
	if err := e.svc.RemoveMember(e.ctx, owner, jun.ID); err != nil {
		t.Fatal(err)
	}
	// Agents never manage members or the danger zone.
	agent := e.access(t, agentCtx(e.owner), p.ID)
	if agent.Can(studio.EditMembers) || agent.Can(studio.EditDanger) || !agent.Can(studio.EditArt) {
		t.Fatalf("agent access: %+v", agent)
	}
	if err := e.svc.ArchiveProject(e.ctx, agent, true); err == nil {
		t.Fatal("agent archived")
	}
	if err := e.svc.DeleteProject(e.ctx, owner, "wrong"); err == nil {
		t.Fatal("delete without confirmation")
	}

	// Listing and filters.
	shared := e.project(t, "Low Tide Radio")
	if _, err := e.svc.UpdateProject(e.ctx, e.access(t, userCtx(e.owner), shared.ID), studio.ProjectPatch{Format: strPtr(studio.FormatWebtoon)}); err != nil {
		t.Fatal(err)
	}
	list, err := e.svc.ListProjects(e.ctx, e.owner.ID, studio.ProjectFilter{})
	if err != nil || len(list) != 3 {
		t.Fatalf("list: %v %d", err, len(list))
	}
	list, _ = e.svc.ListProjects(e.ctx, e.owner.ID, studio.ProjectFilter{Badge: studio.BadgeWebtoon})
	if len(list) != 1 || list[0].Title != "Low Tide Radio" {
		t.Fatalf("webtoon filter: %+v", list)
	}
	list, _ = e.svc.ListProjects(e.ctx, e.owner.ID, studio.ProjectFilter{Query: "tide", Sort: "title"})
	if len(list) != 3 || list[0].Title != "Low Tide Radio" {
		t.Fatalf("search: %+v", list)
	}
	if err := e.svc.ArchiveProject(e.ctx, owner, true); err != nil {
		t.Fatal(err)
	}
	list, _ = e.svc.ListProjects(e.ctx, e.owner.ID, studio.ProjectFilter{})
	if len(list) != 2 {
		t.Fatalf("archived still listed: %d", len(list))
	}
	archived := e.access(t, userCtx(e.owner), p.ID)
	if archived.Can(studio.EditStory) {
		t.Fatal("archived project is editable")
	}
	if err := e.svc.DeleteProject(e.ctx, archived, "Tidebreak Courier"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Project(e.ctx, p.ID); err == nil {
		t.Fatal("project not deleted")
	}
}

func strPtr(s string) *string { return &s }

func TestStoryStructureAndScript(t *testing.T) { testStoryStructureAndScript(t) }

// TestStoryStructureAndScriptPostgres runs on Postgres (skipped unless MV_TEST_POSTGRES_URL is set).
func TestStoryStructureAndScriptPostgres(t *testing.T) {
	testStoryStructureAndScript(t, testutil.WithPostgres())
}

func testStoryStructureAndScript(t *testing.T, opts ...testutil.Option) {
	e := newEnv(t, opts...)
	p := e.project(t, "Tidebreak Courier")
	a := e.access(t, userCtx(e.owner), p.ID)
	arc, err := e.svc.CreateArc(e.ctx, a, "The Salt Road", "")
	if err != nil {
		t.Fatal(err)
	}
	arc2, _ := e.svc.CreateArc(e.ctx, a, "Lantern Tax", "")
	if err := e.svc.MoveArc(e.ctx, a, arc2.ID, -1); err != nil {
		t.Fatal(err)
	}
	arcs, _ := e.svc.Arcs(e.ctx, p.ID)
	if arcs[0].ID != arc2.ID {
		t.Fatalf("arc order: %+v", arcs)
	}
	c1, _ := e.svc.CreateChapter(e.ctx, a, arc.ID, "Paper Crows", "")
	c2, _ := e.svc.CreateChapter(e.ctx, a, arc.ID, "The Lighthouse Debt", "")
	c3, _ := e.svc.CreateChapter(e.ctx, a, "", "Low Water", "")
	if c1.Number != 1 || c2.Number != 2 || c3.Number != 3 {
		t.Fatalf("numbers: %d %d %d", c1.Number, c2.Number, c3.Number)
	}
	if err := e.svc.MoveChapter(e.ctx, a, c3.ID, -1); err != nil {
		t.Fatal(err)
	}
	if ch, _ := e.svc.Chapter(e.ctx, p.ID, 2); ch.ID != c3.ID {
		t.Fatalf("move chapter: %+v", ch)
	}
	if err := e.svc.DeleteChapter(e.ctx, a, c1.ID); err != nil {
		t.Fatal(err)
	}
	chs, _ := e.svc.Chapters(e.ctx, p.ID)
	if len(chs) != 2 || chs[0].ID != c3.ID || chs[0].Number != 1 || chs[1].Number != 2 {
		t.Fatalf("renumber: %+v", chs)
	}
	ch := chs[1]
	sc, err := e.svc.CreateScene(e.ctx, a, ch.ID, "Rooftop chase")
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := e.svc.CreateSettingItem(e.ctx, a, studio.SettingInput{Kind: studio.SettingLocation, Name: "Salt City rooftops", TrackTimeOfDay: true})
	prose, tod := "Rin runs.", "dusk"
	if _, err := e.svc.UpdateScene(e.ctx, a, sc.ID, studio.ScenePatch{Prose: &prose, TimeOfDay: &tod, LocationID: &loc.ID}); err != nil {
		t.Fatal(err)
	}
	sp1, err := e.svc.CreateScriptPanel(e.ctx, a, sc.ID, "", studio.ScriptInput{PageNumber: 7, Camera: "WIDE", Action: "Salt City at dusk.",
		Dialogue: []studio.DialogueLine{{Text: "The ferry won't wait."}, {Text: " "}}})
	if err != nil {
		t.Fatal(err)
	}
	if sp1.Label != "7.1" || sp1.Camera != "wide" || len(sp1.Dialogue.V) != 1 || sp1.Dialogue.V[0].Kind != studio.LineCaption {
		t.Fatalf("panel 1: %+v", sp1)
	}
	sp2, _ := e.svc.CreateScriptPanel(e.ctx, a, sc.ID, sp1.ID, studio.ScriptInput{Action: "Kaito checks his watch.",
		Dialogue: []studio.DialogueLine{{Speaker: "Kaito", Text: "You're late.", Kind: "speech"}}, SFX: []string{"TICK"}})
	sp15, _ := e.svc.CreateScriptPanel(e.ctx, a, sc.ID, sp1.ID, studio.ScriptInput{Action: "Rin looks up."})
	list, _ := e.svc.SceneScript(e.ctx, sc.ID)
	if len(list) != 3 || list[0].ID != sp1.ID || list[1].ID != sp15.ID || list[2].ID != sp2.ID || list[2].Label != "7.3" || list[1].PageNumber != 7 {
		t.Fatalf("insert order: %+v", list)
	}
	if err := e.svc.MoveScriptPanel(e.ctx, a, sp2.ID, -1); err != nil {
		t.Fatal(err)
	}
	list, _ = e.svc.SceneScript(e.ctx, sc.ID)
	if list[1].ID != sp2.ID || list[1].Label != "7.2" {
		t.Fatalf("move: %+v", list)
	}
	upd, err := e.svc.UpdateScriptPanel(e.ctx, a, sp15.ID, studio.ScriptInput{PageNumber: 8, Action: "Rin vaults the rail.", Camera: "low"})
	if err != nil || upd.Label != "8.1" {
		t.Fatalf("update: %v %+v", err, upd)
	}
	usage, _ := e.svc.SettingUsages(e.ctx, p.ID)
	if usage[loc.ID].Panels != 3 || len(usage[loc.ID].TimesOfDay) != 1 {
		t.Fatalf("usage: %+v", usage)
	}
	if err := e.svc.DeleteScriptPanel(e.ctx, a, sp1.ID); err != nil {
		t.Fatal(err)
	}
	// A writer may script but not delete the project; a viewer may do nothing.
	viewer := e.ta.CreateUser(t, "", auth.RoleReader)
	if _, err := e.svc.AddMember(e.ctx, a, viewer.Username, studio.RoleViewer, studio.MemberScope{}); err != nil {
		t.Fatal(err)
	}
	va := e.access(t, userCtx(viewer), p.ID)
	if _, err := e.svc.CreateScriptPanel(e.ctx, va, sc.ID, "", studio.ScriptInput{Action: "x"}); err == nil {
		t.Fatal("viewer wrote script")
	}
}

func TestScopeWritableAndLocks(t *testing.T) { testScopeWritableAndLocks(t) }

// TestScopeWritableAndLocksPostgres runs on Postgres (skipped unless MV_TEST_POSTGRES_URL is set).
func TestScopeWritableAndLocksPostgres(t *testing.T) {
	testScopeWritableAndLocks(t, testutil.WithPostgres())
}

func testScopeWritableAndLocks(t *testing.T, opts ...testutil.Option) {
	e := newEnv(t, opts...)
	p := e.project(t, "Iron Koi")
	a := e.access(t, userCtx(e.owner), p.ID)
	ch, _ := e.svc.CreateChapter(e.ctx, a, "", "One", "")
	pg, err := e.svc.CreatePage(e.ctx, a, ch.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ApplyLayout(userCtx(e.owner), studio.UserActor(e.owner.ID), pg.ID, "builtin:establishing", 0); err != nil {
		t.Fatal(err)
	}
	doc, _ := e.svc.LoadPageDoc(e.ctx, pg.ID)
	if len(doc.Panels) != 3 {
		t.Fatalf("panels: %d", len(doc.Panels))
	}
	panel := doc.Panels[0]
	layer := panel.Layers[0]
	owner := studio.ActorFrom(userCtx(e.owner))
	check := func(actor studio.Actor, s jobs.Scope) error { return e.svc.CheckScopeWritable(e.ctx, actor, s) }
	ok := []jobs.Scope{
		{Kind: jobs.ScopeProject, ProjectID: p.ID},
		{Kind: jobs.ScopeChapter, ProjectID: p.ID, ChapterID: ch.ID},
		{Kind: jobs.ScopePage, ProjectID: p.ID, PageID: pg.ID},
		{Kind: jobs.ScopePanel, ProjectID: p.ID, PageID: pg.ID, PanelID: panel.ID},
		{Kind: jobs.ScopeLayer, ProjectID: p.ID, PanelID: panel.ID, LayerID: layer.ID},
	}
	for _, s := range ok {
		if err := check(owner, s); err != nil {
			t.Fatalf("%s scope: %v", s.Kind, err)
		}
	}
	other := e.project(t, "Salt & Static")
	if err := check(owner, jobs.Scope{Kind: jobs.ScopePage, ProjectID: other.ID, PageID: pg.ID}); err == nil {
		t.Fatal("page of another project accepted")
	}
	stranger := e.ta.CreateUser(t, "", auth.RoleCreator)
	if err := check(studio.UserActor(stranger.ID), ok[2]); err == nil {
		t.Fatal("stranger scope accepted")
	}

	// Locks: every level blocks the AI actions below it.
	if err := e.svc.SetPanelLock(e.ctx, a, panel.ID, true); err != nil {
		t.Fatal(err)
	}
	var ae *core.APIError
	if err := check(owner, ok[3]); !errors.As(err, &ae) || ae.Status != http.StatusLocked || !errors.Is(err, studio.ErrLockedScope) {
		t.Fatalf("locked panel: %v", err)
	}
	if err := check(owner, ok[4]); err == nil {
		t.Fatal("layer of a locked panel accepted")
	}
	if err := check(owner, ok[2]); err != nil {
		t.Fatalf("page with a locked panel: %v", err)
	}
	pages, panels, _ := e.svc.LockedIn(e.ctx, ch.ID)
	if len(pages) != 0 || len(panels) != 1 {
		t.Fatalf("locked in: %v %v", pages, panels)
	}
	// Agents may lock but never unlock.
	agent := e.access(t, agentCtx(e.owner), p.ID)
	if err := e.svc.SetPanelLock(e.ctx, agent, panel.ID, false); err == nil {
		t.Fatal("agent unlocked a panel")
	}
	if err := e.svc.SetPageLock(e.ctx, agent, pg.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := check(owner, ok[2]); err == nil {
		t.Fatal("locked page accepted")
	}
	if err := e.svc.SetPageLock(e.ctx, agent, pg.ID, false); err == nil {
		t.Fatal("agent unlocked a page")
	}
	if err := e.svc.SetPageLock(e.ctx, a, pg.ID, false); err != nil {
		t.Fatal(err)
	}
	locked := true
	if _, err := e.svc.UpdateChapter(e.ctx, a, ch.ID, studio.ChapterPatch{Locked: &locked}); err != nil {
		t.Fatal(err)
	}
	if err := check(owner, ok[1]); err == nil {
		t.Fatal("locked chapter accepted")
	}
	unlocked := false
	if _, err := e.svc.UpdateChapter(e.ctx, agent, ch.ID, studio.ChapterPatch{Locked: &unlocked}); err == nil {
		t.Fatal("agent unlocked a chapter")
	}
	if _, err := e.svc.UpdateChapter(e.ctx, a, ch.ID, studio.ChapterPatch{Locked: &unlocked}); err != nil {
		t.Fatal(err)
	}

	// The same check runs as the jobs enqueue validator.
	_, err = e.ta.Deps.Jobs.Enqueue(userCtx(e.owner), jobs.EnqueueParams{Type: "image.generate", UserID: e.owner.ID, Scope: ok[3]})
	if !errors.As(err, &ae) || ae.Status != http.StatusLocked {
		t.Fatalf("enqueue on locked panel: %v", err)
	}
	if _, err := e.ta.Deps.Jobs.Enqueue(userCtx(e.owner), jobs.EnqueueParams{Type: "image.generate", UserID: e.owner.ID, Scope: ok[2]}); err != nil {
		t.Fatalf("enqueue page: %v", err)
	}
	if _, err := e.ta.Deps.Jobs.Enqueue(userCtx(stranger), jobs.EnqueueParams{Type: "image.generate", UserID: stranger.ID, Scope: ok[2]}); err == nil {
		t.Fatal("stranger enqueued into the project")
	}
	// Letterers with "lettering only" cannot run art jobs on a layer.
	letterer := e.ta.CreateUser(t, "", auth.RoleCreator)
	if _, err := e.svc.AddMember(e.ctx, a, letterer.Username, studio.RoleLetterer, studio.MemberScope{Layers: []string{"lettering"}}); err != nil {
		t.Fatal(err)
	}
	doc, _ = e.svc.LoadPageDoc(e.ctx, pg.ID)
	bg := doc.Panels[1].Layers[0]
	if err := check(studio.UserActor(letterer.ID), jobs.Scope{Kind: jobs.ScopeLayer, ProjectID: p.ID, PanelID: doc.Panels[1].ID, LayerID: bg.ID}); err == nil {
		t.Fatal("letterer ran art on a background layer")
	}
}

func TestPageDocVersionsAndBranches(t *testing.T) { testPageDocVersionsAndBranches(t) }

// TestPageDocVersionsAndBranchesPostgres runs on Postgres (skipped unless MV_TEST_POSTGRES_URL is set).
func TestPageDocVersionsAndBranchesPostgres(t *testing.T) {
	testPageDocVersionsAndBranches(t, testutil.WithPostgres())
}

func testPageDocVersionsAndBranches(t *testing.T, opts ...testutil.Option) {
	e := newEnv(t, opts...)
	p := e.project(t, "Glass Orchard")
	uctx := userCtx(e.owner)
	a := e.access(t, uctx, p.ID)
	user := studio.UserActor(e.owner.ID)
	ch, _ := e.svc.CreateChapter(e.ctx, a, "", "One", "")
	pg, _ := e.svc.CreatePage(e.ctx, a, ch.ID, 0)
	doc, err := e.svc.ApplyLayout(uctx, user, pg.ID, "builtin:talking_heads", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Panels) != 6 || doc.Page.LayoutID != "builtin:talking_heads" || doc.Page.Version != 2 {
		t.Fatalf("layout doc: %d panels, %s, v%d", len(doc.Panels), doc.Page.LayoutID, doc.Page.Version)
	}
	// Edit: add a bubble with texts in two languages and a prompt.
	doc.Panels[0].Prompt = "wide shot of the harbour"
	b := studio.BubbleDoc{Bubble: studio.Bubble{PanelID: doc.Panels[0].ID, Kind: studio.BubbleSpeech}}
	b.Geometry.V = studio.BubbleGeometry{X: 10, Y: 10, W: 20, H: 8}
	b.Texts = []studio.BubbleText{{Lang: "en", Text: "You're late."}, {Lang: "ja", Text: "遅い。"}}
	doc.Bubbles = append(doc.Bubbles, b)
	saved, v1, err := e.svc.SavePageDoc(uctx, user, doc, studio.SaveOptions{Summary: "Added a bubble", BaseVersion: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Bubbles) != 1 || len(saved.Bubbles[0].Texts) != 2 || saved.Panels[0].Prompt == "" || v1.ActorKind != studio.ActorUser || v1.Summary != "Added a bubble" {
		t.Fatalf("saved: %+v %+v", saved.Bubbles, v1)
	}
	if _, _, err := e.svc.SavePageDoc(uctx, user, doc, studio.SaveOptions{BaseVersion: 2}); err == nil {
		t.Fatal("stale save accepted")
	}
	// Invalid geometry is rejected.
	bad := *saved
	bad.Panels = append([]studio.PanelDoc{}, saved.Panels...)
	bad.Panels[0].Points.V = studio.Polygon{{0, 0}, {120, 0}, {0, 10}}
	if _, _, err := e.svc.SavePageDoc(uctx, user, &bad, studio.SaveOptions{}); err == nil {
		t.Fatal("out-of-page polygon accepted")
	}

	// A locked panel is safe from agents but humans may still edit.
	if err := e.svc.SetPanelLock(e.ctx, a, saved.Panels[0].ID, true); err != nil {
		t.Fatal(err)
	}
	cur, _ := e.svc.LoadPageDoc(e.ctx, pg.ID)
	agentDoc := *cur
	agentDoc.Panels = append([]studio.PanelDoc{}, cur.Panels...)
	agentDoc.Panels[0].Prompt = "agent rewrite"
	agent := studio.ActorFrom(agentCtx(e.owner))
	if _, _, err := e.svc.SavePageDoc(agentCtx(e.owner), agent, &agentDoc, studio.SaveOptions{}); !errors.Is(err, studio.ErrLockedScope) {
		t.Fatalf("agent changed a locked panel: %v", err)
	}
	agentDoc.Panels[0].Prompt = cur.Panels[0].Prompt
	agentDoc.Panels[0].Locked = false
	if _, _, err := e.svc.SavePageDoc(agentCtx(e.owner), agent, &agentDoc, studio.SaveOptions{}); err == nil {
		t.Fatal("agent unlocked through a save")
	}
	agentDoc.Panels[0].Locked = true
	agentDoc.Bubbles = nil
	if _, _, err := e.svc.SavePageDoc(agentCtx(e.owner), agent, &agentDoc, studio.SaveOptions{}); err == nil {
		t.Fatal("agent removed a bubble of a locked panel")
	}
	agentDoc.Bubbles = cur.Bubbles
	agentDoc.Panels[1].Prompt = "agent may change this one"
	_, va, err := e.svc.SavePageDoc(agentCtx(e.owner), agent, &agentDoc, studio.SaveOptions{Summary: "Agent tweak"})
	if err != nil || va.ActorKind != studio.ActorAgent {
		t.Fatalf("agent save of an unlocked panel: %v %+v", err, va)
	}

	// Undo the agent's change, then restore v1.
	undone, _, err := e.svc.UndoLast(uctx, user, pg.ID)
	if err != nil || undone.Panels[1].Prompt == "agent may change this one" {
		t.Fatalf("undo: %v", err)
	}
	restored, _, err := e.svc.RestoreVersion(uctx, user, v1.ID)
	if err != nil || len(restored.Bubbles) != 1 || !restored.Panels[0].Locked {
		t.Fatalf("restore: %v", err)
	}
	list, _ := e.svc.Versions(e.ctx, pg.ID, "", 50)
	if len(list) < 5 || list[0].Action != "restore" {
		t.Fatalf("versions: %+v", list)
	}

	// Branch, change, compare, merge back into main.
	br, err := e.svc.CreateBranch(e.ctx, user, pg.ID, "alt-splash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateBranch(e.ctx, user, pg.ID, "alt-splash"); err == nil {
		t.Fatal("duplicate branch")
	}
	onBranch, _ := e.svc.ApplyLayout(uctx, user, pg.ID, "builtin:reveal", 0)
	if len(onBranch.Panels) != 3 || onBranch.Page.BranchID != br.ID {
		t.Fatalf("branch layout: %d %s", len(onBranch.Panels), onBranch.Page.BranchID)
	}
	mainDoc, err := e.svc.SwitchBranch(e.ctx, user, pg.ID, "")
	if err != nil || len(mainDoc.Panels) != 6 || mainDoc.Page.BranchID != "" {
		t.Fatalf("switch to main: %v %d", err, len(mainDoc.Panels))
	}
	b2, _ := e.svc.Branch(e.ctx, br.ID)
	mainHead, _ := e.svc.Versions(e.ctx, pg.ID, "", 1)
	_, _, diff, err := e.svc.CompareVersions(e.ctx, mainHead[0].ID, b2.HeadVersionID)
	if err != nil || len(diff.PanelsRemoved) != 3 || diff.Empty() {
		t.Fatalf("compare: %v %+v", err, diff)
	}
	merged, err := e.svc.MergeBranch(e.ctx, user, pg.ID, br.ID, "")
	if err != nil || len(merged.Panels) != 3 {
		t.Fatalf("merge: %v", err)
	}
	b3, _ := e.svc.Branch(e.ctx, br.ID)
	if b3.MergedAt == nil {
		t.Fatal("branch not marked merged")
	}
}
