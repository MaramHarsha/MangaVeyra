package mcp_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

var readTools = []string{"get_job", "get_pages", "get_project", "get_script", "list_characters", "list_projects", "list_recent_actions", "request_human_action"}

// projectArgs are arguments for every project tool, pointed at projectID.
func (f *fixture) projectArgs(projectID, chapterID, pageID, characterID string) map[string]map[string]any {
	return map[string]map[string]any{
		"get_project":          {"project_id": projectID},
		"get_script":           {"project_id": projectID, "chapter_id": chapterID},
		"list_characters":      {"project_id": projectID},
		"get_pages":            {"project_id": projectID, "chapter_id": chapterID},
		"edit_script":          {"project_id": projectID, "chapter_id": chapterID, "revision": f.revision(), "panels": []any{map[string]any{"panel_id": db.NewID(), "notes": "x"}}},
		"create_character":     {"project_id": projectID, "name": "Mara Quill"},
		"edit_character":       {"project_id": projectID, "character_id": characterID, "revision": "2026-01-01T00:00:00Z", "look": "x"},
		"queue_generation":     {"project_id": projectID, "kind": "page", "page_id": pageID, "version": 1},
		"request_human_action": {"project_id": projectID, "action": "publish", "chapter_id": chapterID},
	}
}

func (f *fixture) firstPage() (string, int) {
	f.t.Helper()
	pages, err := f.pages.ListPages(f.ctx, f.chapter.ID)
	if err != nil || len(pages) == 0 {
		f.t.Fatalf("pages: %v", err)
	}
	return pages[0].ID, pages[0].Version
}

func (f *fixture) firstCharacter() studio.Character {
	f.t.Helper()
	list, err := f.studio.ListCharacters(f.ctx, f.project.ID)
	if err != nil || len(list) == 0 {
		f.t.Fatalf("characters: %v", err)
	}
	return list[0]
}

// TestToolScopesAndProjectListAreEnforcedOnEveryTool covers the scope sets
// (tools a token lacks are neither listed nor callable) and the project
// allow-list for every project tool.
func TestToolScopesAndProjectListAreEnforcedOnEveryTool(t *testing.T) {
	forEachDialect(t, func(t *testing.T, f *fixture) {
		read, _ := f.token("reader", scopesRead, nil, nil)
		rs := f.connect(read)
		if got := toolNames(t, rs); strings.Join(got, ",") != strings.Join(readTools, ",") {
			t.Fatalf("read-only tools %v", got)
		}
		pageID, version := f.firstPage()
		character := f.firstCharacter()
		args := f.projectArgs(f.project.ID, f.chapter.ID, pageID, character.ID)
		for _, name := range []string{"edit_script", "create_character", "edit_character", "queue_generation"} {
			if r := call(t, rs, name, args[name]); !r.Err {
				t.Fatalf("read-only token called %s: %+v", name, r)
			}
		}
		edit, editInfo := f.token("editor", scopesEdit, nil, nil)
		es := f.connect(edit)
		if names := strings.Join(toolNames(t, es), ","); strings.Contains(names, "queue_generation") || !strings.Contains(names, "edit_script") {
			t.Fatalf("edit tools %s", names)
		}
		if r := call(t, es, "queue_generation", args["queue_generation"]); !r.Err {
			t.Fatalf("edit token generated: %+v", r)
		}
		// Scopes are re-read for every request, so a narrowed token loses tools at once.
		if _, err := f.ta.Deps.DB.Exec(f.ctx, `UPDATE api_tokens SET scopes = ? WHERE id = ?`, `["projects:read"]`, editInfo.ID); err != nil {
			t.Fatal(err)
		}
		if r := call(t, es, "create_character", args["create_character"]); !r.Err {
			t.Fatalf("narrowed token still edits: %+v", r)
		}

		// A second project of the owner outside the token's list, and a project
		// of someone else that the token names but its owner cannot use.
		other, err := f.studio.CreateProject(f.ctx, studio.Actor{UserID: f.owner.ID, SiteRole: auth.RoleCreator}, studio.NewProject{Title: "Glass Orchard", Format: "manga_rtl"})
		if err != nil {
			t.Fatal(err)
		}
		otherChapter, err := f.studio.CreateChapter(f.ctx, studio.Actor{UserID: f.owner.ID, SiteRole: auth.RoleCreator}, other.ID, "Seedlings")
		if err != nil {
			t.Fatal(err)
		}
		stranger := f.ta.CreateUser(t, "", auth.RoleCreator)
		foreign, err := f.studio.CreateProject(f.ctx, studio.Actor{UserID: stranger.ID, SiteRole: auth.RoleCreator}, studio.NewProject{Title: "Ninth Bell", Format: "manga_rtl"})
		if err != nil {
			t.Fatal(err)
		}
		scoped, _ := f.token("one project", scopesGenerate, []string{f.project.ID, foreign.ID}, nil)
		ss := f.connect(scoped)
		listed := mustCall(t, ss, "list_projects", nil)["projects"].([]any)
		if len(listed) != 1 || str(listed[0].(map[string]any)["id"]) != f.project.ID {
			t.Fatalf("scoped token lists %v", listed)
		}
		for _, projectID := range []string{other.ID, foreign.ID} {
			for name, a := range f.projectArgs(projectID, otherChapter.ID, pageID, character.ID) {
				if r := call(t, ss, name, a); !r.Err {
					t.Fatalf("%s reached project %s: %+v", name, projectID, r)
				}
			}
		}
		// Jobs of a project outside the list are not readable either.
		j, err := f.ta.Deps.Jobs.Enqueue(f.ctx, jobs.EnqueueParams{Type: "mcp.fixture", UserID: f.owner.ID,
			Scope: jobs.Scope{Kind: jobs.ScopeProject, ProjectID: other.ID}})
		if err != nil {
			t.Fatal(err)
		}
		if r := call(t, ss, "get_job", map[string]any{"job_id": j.ID}); !r.Err {
			t.Fatalf("job of another project readable: %+v", r)
		}
		// In its own project the same token works.
		if r := call(t, ss, "get_pages", map[string]any{"project_id": f.project.ID, "chapter_id": f.chapter.ID}); r.Err {
			t.Fatalf("own project: %s", r.Text)
		}
		_ = version

		// The owner's project role still applies: a viewer's agent cannot edit.
		if _, err := f.ta.Deps.DB.Exec(f.ctx, `INSERT INTO project_members (project_id, user_id, role) VALUES (?, ?, 'viewer')`, foreign.ID, f.owner.ID); err != nil {
			t.Fatal(err)
		}
		viewer, _ := f.token("viewer agent", scopesGenerate, nil, nil)
		vs := f.connect(viewer)
		if r := call(t, vs, "create_character", map[string]any{"project_id": foreign.ID, "name": "Sora Vell"}); !r.Err {
			t.Fatalf("viewer's agent edited: %+v", r)
		}
		if r := call(t, vs, "get_project", map[string]any{"project_id": foreign.ID}); r.Err {
			t.Fatalf("viewer's agent cannot read: %s", r.Text)
		}
	})
}

// TestHumanOnlyActionsAreNeverPerformedByAgents: they are not tools, crafted
// calls fail, and asking only records a request; approving opens the human
// screen and still changes nothing.
func TestHumanOnlyActionsAreNeverPerformedByAgents(t *testing.T) {
	forEachDialect(t, func(t *testing.T, f *fixture) {
		raw, info := f.token("Claude desktop · Jerry", scopesGenerate, nil, nil)
		s := f.connect(raw)
		for _, name := range toolNames(t, s) {
			for _, bad := range []string{"publish", "schedule", "delete", "share", "role", "unlock", "lock", "member"} {
				if strings.Contains(name, bad) {
					t.Fatalf("human-only tool %q offered", name)
				}
			}
		}
		pageID, _ := f.firstPage()
		for _, name := range []string{"publish_chapter", "schedule_chapter", "delete_page", "delete_project", "share_project", "change_role", "unlock_page", "set_lock"} {
			if r := call(t, s, name, map[string]any{"project_id": f.project.ID, "page_id": pageID, "chapter_id": f.chapter.ID}); !r.Err {
				t.Fatalf("crafted %s succeeded: %+v", name, r)
			}
		}
		// Raw JSON-RPC with a crafted tool name is refused the same way.
		body := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"publish_chapter","arguments":{"project_id":"` + f.project.ID + `"}}}`
		req, _ := http.NewRequest(http.MethodPost, f.ta.URL+"/mcp", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+raw)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Mcp-Protocol-Version", "2025-06-18")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw2, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(raw2), `"error"`) && !strings.Contains(string(raw2), `"isError":true`) {
			t.Fatalf("crafted raw call: %d %s", resp.StatusCode, raw2)
		}
		// Anything else is not a request a person has to take.
		if r := call(t, s, "request_human_action", map[string]any{"project_id": f.project.ID, "action": "edit_script"}); !r.Err {
			t.Fatalf("non-human action requested: %+v", r)
		}

		owner := studio.Actor{UserID: f.owner.ID, SiteRole: auth.RoleCreator}
		if err := f.pages.SetLock(f.ctx, pageID, "", "", true, pagestore.Change{ActorKind: "user", ActorID: owner.UserID, Action: "lock"}); err != nil {
			t.Fatal(err)
		}
		pubs := f.count(`SELECT COUNT(*) FROM chapter_publications`)
		jobsBefore := f.count(`SELECT COUNT(*) FROM jobs`)
		asks := map[string]map[string]any{
			"publish":        {"chapter_id": f.chapter.ID},
			"schedule":       {"chapter_id": f.chapter.ID},
			"delete_page":    {"page_id": pageID},
			"delete_project": {},
			"share":          {},
			"change_role":    {},
			"unlock":         {"page_id": pageID},
		}
		ids := map[string]string{}
		for action, extra := range asks {
			a := map[string]any{"project_id": f.project.ID, "action": action, "note": "Ready for readers"}
			for k, v := range extra {
				a[k] = v
			}
			out := mustCall(t, s, "request_human_action", a)
			if str(out["status"]) != "pending" {
				t.Fatalf("%s: %v", action, out)
			}
			ids[action] = str(out["request_id"])
		}
		// Asking twice reuses the waiting request.
		again := mustCall(t, s, "request_human_action", map[string]any{"project_id": f.project.ID, "action": "publish", "chapter_id": f.chapter.ID})
		if str(again["request_id"]) != ids["publish"] {
			t.Fatal("duplicate request created")
		}
		page, err := f.pages.GetPage(f.ctx, pageID)
		if err != nil || !page.Locked {
			t.Fatalf("page unlocked or gone after requests: %v", err)
		}
		if _, err := f.studio.GetProject(f.ctx, f.project.ID); err != nil {
			t.Fatal("project deleted after a request")
		}
		if f.count(`SELECT COUNT(*) FROM chapter_publications`) != pubs || f.count(`SELECT COUNT(*) FROM jobs`) != jobsBefore {
			t.Fatal("a request did work")
		}
		if n := f.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'agent.request' AND actor_kind = 'agent' AND token_id = ?`, info.ID); n != len(asks) {
			t.Fatalf("request audit rows %d", n)
		}
		if n := f.count(`SELECT COUNT(*) FROM notifications WHERE user_id = ? AND kind = 'agent_request' AND link = '/agents'`, f.owner.ID); n == 0 {
			t.Fatal("owner was not notified")
		}

		g13 := string(f.browser.Get("/agents").Body)
		for _, want := range []string{"Asked to publish ch. 1", "Blocked · waiting for you", "Asked to unlock page 1 of ch. 1", "Asked to delete the project"} {
			if !strings.Contains(g13, want) {
				t.Errorf("G13 lacks %q", want)
			}
		}
		// Approving a publish request opens E1; nothing is published by it.
		r := form(f.browser, "/agents/actions/"+ids["publish"]+"/approve", nil)
		if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/studio/p/"+f.project.ID+"/chapters/1/publish" {
			t.Fatalf("approve publish: %d %q", r.StatusCode, r.Header.Get("Location"))
		}
		if f.actionStatus(ids["publish"]) != "approved" || f.count(`SELECT COUNT(*) FROM chapter_publications`) != pubs {
			t.Fatal("approve published or did not record the decision")
		}
		// Approving an unlock opens the editor on that page; the page stays locked.
		r = form(f.browser, "/agents/actions/"+ids["unlock"]+"/approve", nil)
		if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/studio/p/"+f.project.ID+"/chapters/1/edit?page=1" {
			t.Fatalf("approve unlock: %d %q", r.StatusCode, r.Header.Get("Location"))
		}
		if page, _ := f.pages.GetPage(f.ctx, pageID); !page.Locked {
			t.Fatal("approving an unlock request unlocked the page")
		}
		// No screen deletes projects yet: approval is refused and the request waits.
		r = form(f.browser, "/agents/actions/"+ids["delete_project"]+"/approve", nil)
		if r.StatusCode != http.StatusSeeOther || f.actionStatus(ids["delete_project"]) != "pending" {
			t.Fatalf("approve without a screen: %d %s", r.StatusCode, f.actionStatus(ids["delete_project"]))
		}
		if r := form(f.browser, "/agents/actions/"+ids["delete_page"]+"/decline", nil); r.StatusCode != http.StatusSeeOther || f.actionStatus(ids["delete_page"]) != "declined" {
			t.Fatalf("decline: %d", r.StatusCode)
		}
		if _, err := f.pages.GetPage(f.ctx, pageID); err != nil {
			t.Fatal("declined delete removed the page")
		}
		// The agent sees the answers.
		recent := mustCall(t, s, "list_recent_actions", map[string]any{"limit": 20})["actions"].([]any)
		statuses := map[string]string{}
		for _, a := range recent {
			m := a.(map[string]any)
			statuses[str(m["id"])] = str(m["status"])
		}
		if statuses[ids["publish"]] != "approved" || statuses[ids["delete_page"]] != "declined" || statuses[ids["share"]] != "pending" {
			t.Fatalf("agent sees %v", statuses)
		}
	})
}

// TestLocksAndRevisionFencesRefuseAgents keeps agents out of locked scopes and
// off stale revisions.
func TestLocksAndRevisionFencesRefuseAgents(t *testing.T) {
	forEachDialect(t, func(t *testing.T, f *fixture) {
		raw, _ := f.token("fenced", scopesGenerate, nil, nil)
		s := f.connect(raw)
		pageID, version := f.firstPage()
		jobsBefore := f.count(`SELECT COUNT(*) FROM jobs`)
		if r := call(t, s, "queue_generation", map[string]any{"project_id": f.project.ID, "kind": "page", "page_id": pageID, "version": version + 1}); !r.Err || !strings.Contains(r.Text, "changed since") {
			t.Fatalf("stale page version: %+v", r)
		}
		if err := f.pages.SetLock(f.ctx, pageID, "", "", true, pagestore.Change{ActorKind: "user", ActorID: f.owner.ID, Action: "lock"}); err != nil {
			t.Fatal(err)
		}
		_, version = f.firstPage()
		if r := call(t, s, "queue_generation", map[string]any{"project_id": f.project.ID, "kind": "page", "page_id": pageID, "version": version}); !r.Err || !strings.Contains(r.Text, "locked") {
			t.Fatalf("locked page generated: %+v", r)
		}
		if f.count(`SELECT COUNT(*) FROM jobs`) != jobsBefore {
			t.Fatal("a refused generation queued a job")
		}

		// A person edits the script after the agent read it.
		read := mustCall(t, s, "get_script", map[string]any{"project_id": f.project.ID, "chapter_id": f.chapter.ID})
		doc := f.script()
		doc.Scenes[0].Panels[1].Notes = "Wren's hands shake a little."
		c, _ := f.studio.GetChapter(f.ctx, f.chapter.ID)
		if _, err := f.studio.EditScript(f.ctx, studio.EditScriptInput{Actor: studio.Actor{UserID: f.owner.ID}, ProjectID: f.project.ID,
			ChapterID: f.chapter.ID, Doc: doc, ExpectedUpdatedAt: c.UpdatedAt}); err != nil {
			t.Fatal(err)
		}
		panelID := doc.Scenes[0].Panels[1].ID
		r := call(t, s, "edit_script", map[string]any{"project_id": f.project.ID, "chapter_id": f.chapter.ID, "revision": read["revision"],
			"panels": []any{map[string]any{"panel_id": panelID, "notes": "agent overwrite"}}})
		if !r.Err || !strings.Contains(r.Text, "changed since") {
			t.Fatalf("stale script edit: %+v", r)
		}
		if got := f.script().Scenes[0].Panels[1].Notes; got != "Wren's hands shake a little." {
			t.Fatalf("person's edit lost: %q", got)
		}
		if r := call(t, s, "queue_generation", map[string]any{"project_id": f.project.ID, "kind": "script", "chapter_id": f.chapter.ID, "revision": read["revision"]}); !r.Err {
			t.Fatalf("stale script generation: %+v", r)
		}
		character := f.firstCharacter()
		if r := call(t, s, "edit_character", map[string]any{"project_id": f.project.ID, "character_id": character.ID,
			"revision": "2001-01-01T00:00:00Z", "look": "agent look"}); !r.Err || !strings.Contains(r.Text, "changed since") {
			t.Fatalf("stale character edit: %+v", r)
		}
		// A locked chapter refuses script edits.
		if _, err := f.ta.Deps.DB.Exec(f.ctx, `UPDATE chapters SET locked = TRUE WHERE id = ?`, f.chapter.ID); err != nil {
			t.Fatal(err)
		}
		r = call(t, s, "edit_script", map[string]any{"project_id": f.project.ID, "chapter_id": f.chapter.ID, "revision": f.revision(),
			"panels": []any{map[string]any{"panel_id": panelID, "notes": "locked"}}})
		if !r.Err || !strings.Contains(r.Text, "locked") {
			t.Fatalf("locked chapter edited: %+v", r)
		}
	})
}
