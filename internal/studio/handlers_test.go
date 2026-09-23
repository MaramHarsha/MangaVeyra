package studio_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestDashboardAndProjectsPages(t *testing.T) {
	e := newEnv(t)
	// Empty states first.
	r := e.c.Get("/studio")
	status(t, r, http.StatusOK)
	contains(t, r, "No projects yet", "Import CBZ / PDF", "This week", "Remote spend")
	r = e.c.Get("/studio/projects")
	contains(t, r, "No projects yet", "Search projects")

	p := e.project(t, "Tidebreak Courier")
	a := e.access(t, userCtx(e.owner), p.ID)
	ch, _ := e.svc.CreateChapter(e.ctx, a, "", "The Lighthouse Debt", "")
	sc, _ := e.svc.CreateScene(e.ctx, a, ch.ID, "Rooftop chase")
	_, _ = e.svc.CreateScriptPanel(e.ctx, a, sc.ID, "", studio.ScriptInput{PageNumber: 1, Action: "Salt City at dusk."})
	if _, err := e.svc.MakePagesFromScript(userCtx(e.owner), studio.UserActor(e.owner.ID), ch.ID); err != nil {
		t.Fatal(err)
	}
	webtoon, _ := e.svc.CreateProject(e.ctx, e.owner.ID, studio.NewProject{Title: "Low Tide Radio", Format: studio.FormatWebtoon})
	jun := e.ta.CreateUser(t, "jun", auth.RoleCreator)
	shared, _ := e.svc.CreateProject(e.ctx, jun.ID, studio.NewProject{Title: "Paper Crows"})
	sa := e.access(t, userCtx(jun), shared.ID)
	if _, err := e.svc.AddMember(e.ctx, sa, e.owner.Username, studio.RoleEditor, studio.MemberScope{}); err != nil {
		t.Fatal(err)
	}

	r = e.c.Get("/studio")
	status(t, r, http.StatusOK)
	contains(t, r, "mv-continue__title", "Tidebreak Courier", "Open editor", "/studio/p/"+p.ID+"/chapters/1/edit?page=1",
		"is 1 page from done", "Recent projects", "Low Tide Radio", "Shared by", "Editor", "Webtoon")

	r = e.c.Get("/studio/projects")
	contains(t, r, "3 projects · 1 shared with you", "Paper Crows", "Status: Any", "Sort: Last edited")
	r = e.c.Get("/studio/projects?status=webtoon")
	contains(t, r, "Low Tide Radio")
	if strings.Contains(string(r.Body), ">Tidebreak Courier<") {
		t.Fatal("filter ignored")
	}
	r = e.c.Get("/studio/projects?status=shared&view=list")
	contains(t, r, "Paper Crows", "<table")
	r = e.c.Get("/studio/projects?q=zzz")
	contains(t, r, "Nothing matches", "Clear filters")
	// HTMX search swaps only the results.
	e.c.Header.Set("HX-Target", "mv-projects-results")
	r = htmxGet(e.c, "/studio/projects?q=tide")
	e.c.Header.Del("HX-Target")
	contains(t, r, `id="mv-projects-results"`, "Low Tide Radio")
	if strings.Contains(string(r.Body), "<html") {
		t.Fatal("full page for an HTMX search")
	}
	_ = webtoon

	// Anonymous visitors go to log in; readers see no create actions.
	anon := e.ta.NewClient()
	if r := anon.Get("/studio/projects"); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("anonymous: %d", r.StatusCode)
	}
	_, reader := e.ta.UserClient(t, auth.RoleReader)
	r = reader.Get("/studio")
	status(t, r, http.StatusOK)
	if strings.Contains(string(r.Body), `href="/studio/import"`) {
		t.Fatal("reader sees import")
	}
}

func TestWizardEndToEnd(t *testing.T) { testWizard(t) }

// TestWizardEndToEndPostgres runs the wizard on Postgres (skipped unless MV_TEST_POSTGRES_URL is set).
func TestWizardEndToEndPostgres(t *testing.T) { testWizard(t, testutil.WithPostgres()) }

func testWizard(t *testing.T, opts ...testutil.Option) {
	e := newEnv(t, opts...)
	loc := redirect(t, e.c.Get("/studio/new?template=builtin:twelve_chapter"))
	if loc != "/studio/new/premise" {
		t.Fatalf("start: %s", loc)
	}
	// Later steps need a premise first.
	if loc := redirect(t, e.c.Get("/studio/new/genre")); loc != "/studio/new/premise" {
		t.Fatalf("genre without premise: %s", loc)
	}
	r := e.c.Get("/studio/new/premise")
	contains(t, r, "What’s the story?", "A gull steals one letter a day", "Autosaves as you type", "YOUR MANGA SO FAR")
	status(t, post(e.c, "/studio/new/premise", url.Values{"premise": {"short"}}), http.StatusUnprocessableEntity)
	// Example chips fill the premise.
	redirect(t, post(e.c, "/studio/new/premise", url.Values{"example": {"studio.wizard.example.2"}}))
	contains(t, e.c.Get("/studio/new/premise"), "The lighthouse keeper owes the city")
	// Autosave (HTMX) keeps the draft and answers with the indicator and side panel.
	r = htmxPost(e.c, "/studio/new/premise", url.Values{"autosave": {"1"}, "premise": {"A courier who can't read maps carries the letters nobody else will deliver."}, "title": {"Tidebreak Courier"}})
	status(t, r, http.StatusOK)
	contains(t, r, "Draft saved", `hx-swap-oob="true"`, "Tidebreak Courier")
	if loc := redirect(t, post(e.c, "/studio/new/premise", url.Values{"premise": {"A courier who can't read maps carries the letters nobody else will deliver."}, "title": {"Tidebreak Courier"}})); loc != "/studio/new/genre" {
		t.Fatalf("next: %s", loc)
	}
	status(t, post(e.c, "/studio/new/genre", url.Values{}), http.StatusUnprocessableEntity)
	status(t, post(e.c, "/studio/new/genre", url.Values{"genres": {"action", "mystery", "drama", "comedy"}}), http.StatusUnprocessableEntity)
	redirect(t, post(e.c, "/studio/new/genre", url.Values{"genres": {"action", "mystery"}}))
	contains(t, e.c.Get("/studio/new/style"), "How should it look?", `value="seinen_bw"`)
	redirect(t, post(e.c, "/studio/new/style", url.Values{"style": {"seinen_bw"}}))
	r = e.c.Get("/studio/new/format")
	contains(t, r, "How will it be read?", "Manga · right to left", "Vertical webtoon", "B5 · 182 × 257 mm", `value="12"`)
	status(t, post(e.c, "/studio/new/format", url.Values{"format": {"manga_rtl"}, "chapters": {"0"}}), http.StatusUnprocessableEntity)
	redirect(t, post(e.c, "/studio/new/format", url.Values{"format": {"manga_rtl"}, "page_size": {"b5"}, "chapters": {"12"}, "pages_per_chapter": {"30-36"}}))
	r = e.c.Get("/studio/new/generate")
	contains(t, r, "What Generate will make", "12 chapter summaries", "Built-in placeholder", "12-chapter arc", "Generate outline")

	loc = redirect(t, post(e.c, "/studio/new/generate", url.Values{"title": {"Tidebreak Courier"}, "template_id": {"builtin:twelve_chapter"}}))
	if !strings.HasPrefix(loc, "/studio/new/status?draft=") {
		t.Fatalf("generate: %s", loc)
	}
	r = e.c.Get(loc)
	status(t, r, http.StatusOK)
	contains(t, r, "mv-generate", "job-id")
	e.runJobs(t)
	// HTMX poll now sends the browser to the story editor.
	r = htmxGet(e.c, loc)
	story := r.Header.Get("HX-Redirect")
	if r.StatusCode != http.StatusNoContent || !strings.HasSuffix(story, "/story") {
		t.Fatalf("done poll: %d %q", r.StatusCode, story)
	}
	r = e.c.Get(story)
	status(t, r, http.StatusOK)
	contains(t, r, "Make pages from script", "Script assistant", "Ch. 1", "Sc. 1")
	pid := regexp.MustCompile(`/studio/p/([0-9a-z]+)/story`).FindStringSubmatch(story)[1]
	chs, _ := e.svc.Chapters(e.ctx, pid)
	arcs, _ := e.svc.Arcs(e.ctx, pid)
	if len(chs) != 12 || len(arcs) != 4 || arcs[0].Title != "The hook" {
		t.Fatalf("outline: %d chapters, %+v", len(chs), arcs)
	}
	// A new wizard starts from a fresh draft.
	redirect(t, e.c.Get("/studio/new"))
	contains(t, e.c.Get("/studio/new/premise"), `name="premise"`)
}

func TestStoryEditorHandlers(t *testing.T) { testStoryEditorHandlers(t) }

// TestStoryEditorHandlersPostgres runs on Postgres (skipped unless MV_TEST_POSTGRES_URL is set).
func TestStoryEditorHandlersPostgres(t *testing.T) {
	testStoryEditorHandlers(t, testutil.WithPostgres())
}

func testStoryEditorHandlers(t *testing.T, opts ...testutil.Option) {
	e := newEnv(t, opts...)
	p := e.project(t, "Ninth Bell")
	base := "/studio/p/" + p.ID
	r := e.c.Get(base + "/story")
	status(t, r, http.StatusOK)
	contains(t, r, "No chapters yet", "Add chapter")
	loc := redirect(t, post(e.c, base+"/chapters", url.Values{"title": {"The First Toll"}}))
	if loc != base+"/story/1" {
		t.Fatalf("chapter: %s", loc)
	}
	redirect(t, post(e.c, base+"/arcs", url.Values{"title": {"The Salt Road"}, "next": {base + "/story/1"}}))
	ch, _ := e.svc.Chapter(e.ctx, p.ID, 1)
	loc = redirect(t, post(e.c, base+"/scenes", url.Values{"chapter_id": {ch.ID}, "title": {"Harbour at dusk"}}))
	if loc != base+"/story/1/1" {
		t.Fatalf("scene: %s", loc)
	}
	sc, _ := e.svc.Scenes(e.ctx, ch.ID)
	loc = redirect(t, post(e.c, base+"/script", url.Values{"scene_id": {sc[0].ID}, "next": {base + "/story/1/1"}}))
	if !strings.Contains(loc, "edit=") {
		t.Fatalf("new panel: %s", loc)
	}
	panels, _ := e.svc.SceneScript(e.ctx, sc[0].ID)
	sp := panels[0]
	c, _ := e.svc.CreateCharacter(e.ctx, e.access(t, userCtx(e.owner), p.ID), studio.CharacterInput{Name: "Kaito Mbeki"})
	// Inline edit through HTMX.
	r = htmxGet(e.c, base+"/script/"+sp.ID+"/edit")
	contains(t, r, `name="line_speaker"`, "Kaito Mbeki", `name="camera"`)
	r = htmxPost(e.c, base+"/script/"+sp.ID, url.Values{"page": {"7"}, "camera": {"close"}, "emotion": {"Annoyed"}, "action": {"Kaito checks a pocket watch."},
		"line_speaker": {c.ID, "", ""}, "line_kind": {"speech", "caption", "speech"}, "line_text": {"You're late. Again.", "The ferry won't wait.", ""}, "sfx": {"tick, tock"}})
	status(t, r, http.StatusOK)
	contains(t, r, "7.1", ">close<", "Kaito Mbeki:", "You&#39;re late. Again.", "Caption:", "TICK", "TOCK")
	upd, _ := e.svc.ScriptPanel(e.ctx, sp.ID)
	if len(upd.Dialogue.V) != 2 || upd.Dialogue.V[0].CharacterID != c.ID || upd.CharacterIDs.V[0] != c.ID {
		t.Fatalf("saved panel: %+v", upd)
	}
	// Prose autosave.
	r = htmxPost(e.c, base+"/scenes/"+sc[0].ID, url.Values{"autosave": {"1"}, "prose": {"Salt wind over the roofs."}, "time_of_day": {"dusk"}})
	contains(t, r, "Draft saved")
	contains(t, e.c.Get(base+"/story/1/1?tab=prose"), "Salt wind over the roofs.", "Dusk")
	// Rename through the tree menu.
	redirect(t, post(e.c, base+"/scenes/"+sc[0].ID, url.Values{"title": {"Rooftop chase"}, "next": {base + "/story/1/1"}}))
	contains(t, e.c.Get(base+"/story/1/1?rename="+sc[0].ID), `id="rename-input"`)
	// Tree move with query parameters (the dropdown's POST menu items).
	redirect(t, post(e.c, base+"/chapters/"+ch.ID+"?next="+url.QueryEscape(base+"/story/1")+"&locked=on", url.Values{}))
	if c, _ := e.svc.ChapterByID(e.ctx, ch.ID); !c.Locked {
		t.Fatal("lock via menu")
	}
	contains(t, e.c.Get(base+"/story/1/1"), "This chapter is locked")
	redirect(t, post(e.c, base+"/chapters/"+ch.ID+"?locked=off", url.Values{}))

	// Script assistant: request (HTMX), job, apply.
	r = htmxPost(e.c, base+"/assist", url.Values{"chapter_id": {ch.ID}, "scene_id": {sc[0].ID}, "request": {"Make 7.1 a full-width splash"}})
	status(t, r, http.StatusOK)
	contains(t, r, "Make 7.1 a full-width splash", `hx-swap-oob="true"`, "Thinking")
	e.runJobs(t)
	as, _ := e.svc.Assists(e.ctx, ch.ID)
	r = htmxGet(e.c, base+"/assist/"+as[0].ID)
	contains(t, r, "Proposed:", "Apply", "Show diff", "Changes 1 panel")
	r = htmxPost(e.c, base+"/assist/"+as[0].ID+"/apply", url.Values{})
	if r.StatusCode != http.StatusNoContent || r.Header.Get("HX-Refresh") != "true" {
		t.Fatalf("apply: %d", r.StatusCode)
	}
	if sp2, _ := e.svc.ScriptPanel(e.ctx, sp.ID); sp2.Camera != "wide" {
		t.Fatalf("assist not applied: %+v", sp2)
	}

	// Make pages from script.
	loc = redirect(t, post(e.c, base+"/chapters/"+ch.ID+"/pages", url.Values{"next": {base + "/story/1/1"}}))
	if loc != base+"/story/1/1" {
		t.Fatalf("pages: %s", loc)
	}
	pages, _ := e.svc.Pages(e.ctx, ch.ID)
	if len(pages) != 1 || pages[0].Number != 7 {
		t.Fatalf("pages: %+v", pages)
	}
	// Delete the panel and the scene.
	redirect(t, post(e.c, base+"/script/"+sp.ID+"/delete", url.Values{"next": {base + "/story/1/1"}}))
	redirect(t, post(e.c, base+"/scenes/"+sc[0].ID+"/delete", url.Values{}))
	contains(t, e.c.Get(base+"/story/1"), "This chapter has no scenes")

	// A viewer reads but cannot write.
	viewer, vc := e.ta.UserClient(t, auth.RoleReader)
	if _, err := e.svc.AddMember(e.ctx, e.access(t, userCtx(e.owner), p.ID), viewer.Username, studio.RoleViewer, studio.MemberScope{}); err != nil {
		t.Fatal(err)
	}
	r = vc.Get(base + "/story/1")
	status(t, r, http.StatusOK)
	if strings.Contains(string(r.Body), "Add arc, chapter or scene") {
		t.Fatal("viewer sees add forms")
	}
	status(t, post(vc, base+"/chapters", url.Values{"title": {"Nope"}}), http.StatusForbidden)
	// Strangers get 404 for everything in the project.
	_, sc2 := e.ta.UserClient(t, auth.RoleCreator)
	status(t, sc2.Get(base+"/story"), http.StatusNotFound)
	status(t, sc2.Get("/studio/p/not-an-id/story"), http.StatusNotFound)
}

func TestCharacterHandlers(t *testing.T) { testCharacterHandlers(t) }

// TestCharacterHandlersPostgres runs on Postgres (skipped unless MV_TEST_POSTGRES_URL is set).
func TestCharacterHandlersPostgres(t *testing.T) { testCharacterHandlers(t, testutil.WithPostgres()) }

func testCharacterHandlers(t *testing.T, opts ...testutil.Option) {
	e := newEnv(t, opts...)
	p := e.project(t, "Tidebreak Courier")
	base := "/studio/p/" + p.ID
	r := e.c.Get(base + "/characters")
	status(t, r, http.StatusOK)
	contains(t, r, "Characters · Tidebreak Courier", "Add a character", "Import pack")
	r = e.c.Get(base + "/characters/new")
	contains(t, r, "New character", "Describe &amp; generate", "Generate reference sheet", "Built-in placeholder", "SHEET 1")
	status(t, post(e.c, base+"/characters/new", url.Values{"mode": {"describe"}, "name": {""}}), http.StatusUnprocessableEntity)
	loc := redirect(t, post(e.c, base+"/characters/new?mode=describe", url.Values{"mode": {"describe"}, "name": {"Sora Vell"}, "role": {"antagonist"},
		"look": {"Silver bob, grey coat with brass lantern clasps."}, "personality": {"Never raises her voice."}, "style": {"series"}}))
	cid := regexp.MustCompile(`character=([0-9a-z]+)`).FindStringSubmatch(loc)[1]
	r = e.c.Get(loc)
	contains(t, r, "Sora Vell", `id="mv-sheets"`)
	e.runJobs(t)
	r = htmxGet(e.c, base+"/characters/"+cid+"/sheets")
	contains(t, r, "Pick a look (4 of 4)", `name="sheet"`, "Keep &amp; train Character Lock")
	sheet := regexp.MustCompile(`name="sheet" value="([0-9a-z]+)"`).FindStringSubmatch(string(r.Body))[1]
	redirect(t, post(e.c, base+"/characters/"+cid+"/pick", url.Values{"sheet": {sheet}, "train": {"1"}}))
	r = e.c.Get(base + "/characters/" + cid)
	contains(t, r, "Sora Vell", "Character Lock", "Training", "Export pack", "Use in panel")
	e.runJobs(t)
	r = e.c.Get(base + "/characters/" + cid)
	contains(t, r, "Trained on 1 reference", "%")

	// Upload references (multipart).
	r = multipartPost(t, e.c, base+"/characters/new?mode=upload", map[string]string{"mode": "upload", "name": "Pip", "role": "sidekick", "ref_kind": "front"}, "refs", pngBytes(t), pngBytes(t))
	loc = redirect(t, r)
	pip := regexp.MustCompile(`character=([0-9a-z]+)`).FindStringSubmatch(loc)[1]
	contains(t, e.c.Get(loc), "Your references (2)", "Train Character Lock")
	refs, _ := e.svc.CharacterRefs(e.ctx, pip)
	if len(refs) != 2 || refs[0].Kind != studio.RefFront {
		t.Fatalf("refs: %+v", refs)
	}
	r = multipartPost(t, e.c, base+"/characters/"+pip+"/refs", map[string]string{"kind": "expression", "label": "joy"}, "refs", []byte("not an image"))
	status(t, r, http.StatusUnprocessableEntity)
	// Outfits, relations, profile edit, delete.
	redirect(t, post(e.c, base+"/characters/"+cid+"/outfits", url.Values{"name": {"Courier coat"}, "is_default": {"on"}}))
	redirect(t, post(e.c, base+"/characters/"+cid+"/relations", url.Values{"other_id": {pip}, "kind": {"Hunting her"}}))
	status(t, post(e.c, base+"/characters/"+cid+"/relations", url.Values{"other_id": {cid}, "kind": {"Self"}}), http.StatusUnprocessableEntity)
	r = e.c.Get(base + "/characters/" + cid + "?tab=relations")
	contains(t, r, "COURIER COAT · DEFAULT", "Hunting her", "Pip")
	contains(t, e.c.Get(base+"/characters/"+cid+"/edit"), "Edit Sora Vell", `name="speech_style"`)
	redirect(t, post(e.c, base+"/characters/"+cid, url.Values{"name": {"Sora Vell"}, "role": {"antagonist"}, "age": {"28"}, "category": {"main"}, "voice": {"Soft, exact"}}))
	contains(t, e.c.Get(base+"/characters/"+cid), "Soft, exact", "28")
	contains(t, e.c.Get(base+"/characters?cat=main"), "Sora")
	contains(t, e.c.Get("/studio/characters"), "Tidebreak Courier", "Open bible")
	redirect(t, post(e.c, base+"/characters/"+pip+"/delete", url.Values{}))
	if _, err := e.svc.Character(e.ctx, p.ID, pip); err == nil {
		t.Fatal("character not deleted")
	}
}

func TestSettingTemplatesAndProjectSettings(t *testing.T) { testSettingTemplatesAndProjectSettings(t) }

// TestSettingTemplatesAndProjectSettingsPostgres runs on Postgres (skipped unless MV_TEST_POSTGRES_URL is set).
func TestSettingTemplatesAndProjectSettingsPostgres(t *testing.T) {
	testSettingTemplatesAndProjectSettings(t, testutil.WithPostgres())
}

func testSettingTemplatesAndProjectSettings(t *testing.T, opts ...testutil.Option) {
	e := newEnv(t, opts...)
	p := e.project(t, "Tidebreak Courier")
	base := "/studio/p/" + p.ID
	// Setting (C5) with an image upload.
	contains(t, e.c.Get(base+"/setting"), "No locations yet", "Locations 0")
	r := multipartPost(t, e.c, base+"/setting", map[string]string{"kind": "location", "name": "The Lighthouse", "description": "Interior + exterior.", "track_time": "on"}, "image", pngBytes(t))
	redirect(t, r)
	r = e.c.Get(base + "/setting")
	contains(t, r, "The Lighthouse", "Time of day tracked", "Used in 0 panels", "/media/")
	items, _ := e.svc.SettingItems(e.ctx, p.ID, studio.SettingLocation)
	contains(t, e.c.Get(base+"/setting?edit="+items[0].ID), "Edit The Lighthouse", `name="remove_image"`)
	redirect(t, multipartPost(t, e.c, base+"/setting/"+items[0].ID, map[string]string{"name": "Wren's Lighthouse", "remove_image": "on"}, "image"))
	if it, _ := e.svc.SettingItem(e.ctx, p.ID, items[0].ID); it.Name != "Wren's Lighthouse" || it.AssetID != "" || it.Data.V.TrackTimeOfDay {
		t.Fatalf("updated item: %+v", it)
	}
	redirect(t, post(e.c, base+"/setting/"+items[0].ID+"/delete", url.Values{}))
	redirect(t, e.c.Get("/studio/setting"))

	// Templates (B4).
	r = e.c.Get("/studio/templates")
	contains(t, r, "Templates", "4-koma", "Chase", "Custom grid", "LAYOUT · 7 PANELS", "<polygon", "Import pack")
	contains(t, e.c.Get("/studio/templates?tab=story"), "Three-act arc", "Confession scene")
	contains(t, e.c.Get("/studio/templates?tab=characters"), "No packs installed yet")
	contains(t, e.c.Get("/studio/templates/builtin:grid_3x3?rows=2&cols=4"), "LAYOUT · GRID · 8 PANELS", "Apply to a page")
	status(t, e.c.Get("/studio/templates/nope"), http.StatusNotFound)
	// Apply a layout to a page from the template page.
	a := e.access(t, userCtx(e.owner), p.ID)
	ch, _ := e.svc.CreateChapter(e.ctx, a, "", "One", "")
	pg, _ := e.svc.CreatePage(e.ctx, a, ch.ID, 0)
	contains(t, e.c.Get("/studio/templates/builtin:chase"), pg.ID)
	loc := redirect(t, post(e.c, "/studio/templates/builtin:chase/apply", url.Values{"page": {pg.ID}}))
	if !strings.Contains(loc, "/chapters/1/edit?page=1") {
		t.Fatalf("apply: %s", loc)
	}
	if panels, _ := e.svc.Panels(e.ctx, pg.ID); len(panels) != 7 {
		t.Fatalf("chase applied: %d", len(panels))
	}

	// Project settings: general, members, danger zone.
	r = e.c.Get(base + "/settings")
	contains(t, r, "Settings · Tidebreak Courier", "Members &amp; roles", "Danger zone", "Server default", "licence guard")
	redirect(t, post(e.c, base+"/settings", url.Values{"title": {"Tidebreak Courier"}, "visibility": {"public"}, "routing_mode": {"local_only"}, "format": {"manga_rtl"}}))
	pr, _ := e.svc.Project(e.ctx, p.ID)
	if pr.Visibility != studio.VisibilityPublic || pr.RoutingMode != "local_only" {
		t.Fatalf("settings: %+v", pr)
	}
	status(t, post(e.c, base+"/settings", url.Values{"title": {""}}), http.StatusUnprocessableEntity)
	jun, jc := e.ta.UserClient(t, auth.RoleCreator)
	status(t, post(e.c, base+"/settings/members", url.Values{"username": {"ghost"}, "role": {"writer"}}), http.StatusUnprocessableEntity)
	redirect(t, post(e.c, base+"/settings/members", url.Values{"username": {jun.Username}, "role": {"letterer"}, "pages": {"1-20"}, "lettering_only": {"on"}}))
	contains(t, e.c.Get(base+"/settings"), "@"+jun.Username)
	// The new member sees the project and was notified; they cannot manage members.
	contains(t, jc.Get("/studio/projects"), "Tidebreak Courier")
	if n, _ := e.ta.Deps.Notify.UnreadCount(e.ctx, jun.ID); n != 1 {
		t.Fatalf("invite notification: %d", n)
	}
	status(t, post(jc, base+"/settings/members", url.Values{"username": {e.owner.Username}, "role": {"writer"}}), http.StatusForbidden)
	redirect(t, post(e.c, base+"/settings/members/"+jun.ID, url.Values{"role": {"artist"}, "pages": {""}}))
	// Members may leave.
	redirect(t, post(jc, base+"/settings/members/"+jun.ID+"/delete", url.Values{}))
	status(t, jc.Get(base+"/story"), http.StatusNotFound)
	// Archive, restore, delete (human, owner, confirmed).
	redirect(t, post(e.c, base+"/settings/archive", url.Values{"archive": {"1"}}))
	contains(t, e.c.Get(base+"/settings"), "archived and read-only")
	status(t, post(e.c, base+"/chapters", url.Values{"title": {"x"}}), http.StatusForbidden)
	redirect(t, post(e.c, base+"/settings/archive", url.Values{"archive": {"0"}}))
	status(t, post(e.c, base+"/settings/delete", url.Values{"confirm": {"nope"}}), http.StatusUnprocessableEntity)
	redirect(t, post(e.c, base+"/settings/delete", url.Values{"confirm": {"Tidebreak Courier"}}))
	status(t, e.c.Get(base+"/story"), http.StatusNotFound)
}

func TestAPIAndAgents(t *testing.T) { testAPIAndAgents(t) }

// TestAPIAndAgentsPostgres runs on Postgres (skipped unless MV_TEST_POSTGRES_URL is set).
func TestAPIAndAgentsPostgres(t *testing.T) { testAPIAndAgents(t, testutil.WithPostgres()) }

func testAPIAndAgents(t *testing.T, opts ...testutil.Option) {
	e := newEnv(t, opts...)
	r := e.c.PostJSON("/api/v1/projects", map[string]any{"title": "Iron Koi", "format": "comic_ltr"})
	status(t, r, http.StatusCreated)
	var p studio.Project
	r.JSON(t, &p)
	a := e.access(t, userCtx(e.owner), p.ID)
	ch, _ := e.svc.CreateChapter(e.ctx, a, "", "One", "")
	pg, _ := e.svc.CreatePage(e.ctx, a, ch.ID, 0)
	base := "/api/v1/projects/" + p.ID
	status(t, e.c.Get("/api/v1/projects"), http.StatusOK)
	status(t, e.c.SendJSON(http.MethodPatch, base, map[string]any{"visibility": "public"}), http.StatusOK)
	status(t, e.c.PostJSON(base+"/pages/"+pg.ID+"/layout", map[string]any{"layout": "builtin:fight_beat"}), http.StatusOK)
	r = e.c.Get(base + "/pages/" + pg.ID + "/doc")
	status(t, r, http.StatusOK)
	var doc studio.PageDoc
	r.JSON(t, &doc)
	if len(doc.Panels) != 5 {
		t.Fatalf("doc: %d panels", len(doc.Panels))
	}
	// Comic LTR mirrors the right-to-left template.
	tpl, _ := studio.BuiltinLayout("builtin:fight_beat")
	if doc.Panels[1].Points.V[0][0] != studio.MirrorPolygon(tpl.Pages[0][1])[0][0] {
		t.Fatalf("not mirrored: %v", doc.Panels[1].Points.V)
	}
	doc.Panels[0].Prompt = "impact"
	r = e.c.SendJSON(http.MethodPut, base+"/pages/"+pg.ID+"/doc", map[string]any{"doc": doc, "summary": "Prompt", "base_version": doc.Page.Version})
	status(t, r, http.StatusOK)
	status(t, e.c.SendJSON(http.MethodPut, base+"/pages/"+pg.ID+"/doc", map[string]any{"doc": doc, "base_version": doc.Page.Version}), http.StatusConflict)
	status(t, e.c.PostJSON(base+"/pages/"+pg.ID+"/lock", map[string]any{"locked": true, "panel_id": doc.Panels[0].ID}), http.StatusOK)
	r = e.c.Get(base + "/pages/" + pg.ID + "/versions")
	var vs struct {
		Items []studio.Version `json:"items"`
	}
	r.JSON(t, &vs)
	if len(vs.Items) != 2 {
		t.Fatalf("versions: %+v", vs.Items)
	}
	status(t, e.c.PostJSON(base+"/pages/"+pg.ID+"/branches", map[string]any{"name": "alt"}), http.StatusCreated)
	status(t, e.c.PostJSON(base+"/pages/"+pg.ID+"/branches/main/switch", nil), http.StatusOK)
	status(t, e.c.PostJSON(base+"/pages/"+pg.ID+"/undo", nil), http.StatusOK)
	status(t, e.c.Get("/api/v1/templates"), http.StatusOK)

	// An agent token: may edit unlocked work, never unlock, never touch other projects.
	tok, _, err := e.ta.Deps.Auth.CreateToken(e.ctx, auth.TokenInput{UserID: e.owner.ID, Kind: httpx.TokenAgent, Name: "helper",
		Scopes: []string{"projects:read", "pages:write"}, ProjectIDs: []string{p.ID}})
	if err != nil {
		t.Fatal(err)
	}
	ag := e.ta.BearerClient(tok)
	status(t, ag.Get(base+"/pages/"+pg.ID+"/doc"), http.StatusOK)
	r = ag.PostJSON(base+"/pages/"+pg.ID+"/lock", map[string]any{"locked": false, "panel_id": doc.Panels[0].ID})
	status(t, r, http.StatusForbidden)
	status(t, ag.SendJSON(http.MethodPatch, base, map[string]any{"title": "x"}), http.StatusForbidden)
	other, _ := e.svc.CreateProject(e.ctx, e.owner.ID, studio.NewProject{Title: "Secret"})
	status(t, ag.Get("/api/v1/projects/"+other.ID), http.StatusNotFound)
	// The agent's page save is recorded as an agent version.
	r = ag.Get(base + "/pages/" + pg.ID + "/doc")
	var cur studio.PageDoc
	r.JSON(t, &cur)
	cur.Panels[2].Prompt = "agent pass"
	status(t, ag.SendJSON(http.MethodPut, base+"/pages/"+pg.ID+"/doc", map[string]any{"doc": cur, "summary": "Agent pass"}), http.StatusOK)
	list, _ := e.svc.Versions(e.ctx, pg.ID, "", 1)
	if list[0].ActorKind != studio.ActorAgent {
		t.Fatalf("agent version: %+v", list[0])
	}
}

func TestProjectTopicAuthorizer(t *testing.T) {
	e := newEnv(t)
	p := e.project(t, "Glass Orchard")
	member := httpx.Identity{User: &httpx.User{ID: e.owner.ID}}
	stranger := e.ta.CreateUser(t, "", auth.RoleCreator)
	if err := e.ta.Deps.Hub.Allowed(context.Background(), member, "project:"+p.ID, false); err != nil {
		t.Fatalf("member: %v", err)
	}
	if err := e.ta.Deps.Hub.Allowed(context.Background(), member, "project:"+p.ID, true); err == nil {
		t.Fatal("write allowed")
	}
	if err := e.ta.Deps.Hub.Allowed(context.Background(), httpx.Identity{User: &httpx.User{ID: stranger.ID}}, "project:"+p.ID, false); err == nil {
		t.Fatal("stranger subscribed")
	}
	// Events are JSON-serialisable.
	if _, err := json.Marshal(studio.Event{Kind: "page", ID: "x"}); err != nil {
		t.Fatal(err)
	}
}
