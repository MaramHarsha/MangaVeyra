package studioui_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// seedLines gives the first panel a caption and a spoken line.
func seedLines(t *testing.T, f storyFixture) studio.ScriptDoc {
	t.Helper()
	ctx := context.Background()
	doc, err := f.studio.GetScript(ctx, f.chapterID)
	if err != nil {
		t.Fatal(err)
	}
	chapter, err := f.studio.GetChapter(ctx, f.chapterID)
	if err != nil {
		t.Fatal(err)
	}
	doc.Scenes[0].Panels[0].Dialogue = []studio.ScriptDialogue{
		{Kind: "caption", Text: "The ferry won't wait for a courier with no map."},
		{Kind: "speech", Speaker: "Kaito", Text: "You're late. Again."},
	}
	owner := studio.Actor{SiteRole: auth.RoleCreator}
	if err := f.ta.Deps.DB.Get(ctx, &owner.UserID, `SELECT owner_id FROM projects WHERE id = ?`, f.projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.studio.EditScript(ctx, studio.EditScriptInput{Actor: owner, ProjectID: f.projectID, ChapterID: f.chapterID,
		Doc: doc, ExpectedUpdatedAt: chapter.UpdatedAt}); err != nil {
		t.Fatalf("seed lines: %v", err)
	}
	doc, err = f.studio.GetScript(ctx, f.chapterID)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func lineTexts(t *testing.T, f storyFixture) []string {
	t.Helper()
	doc, err := f.studio.GetScript(context.Background(), f.chapterID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range doc.Scenes[0].Panels[0].Dialogue {
		out = append(out, line.Kind+"|"+line.Speaker+"|"+line.Text)
	}
	return out
}

// Without JavaScript the add and remove line buttons re-render the draft with
// the change (nothing saved), Save keeps it, and captions read "Caption:".
func TestStoryDialogueLinesWithoutJavaScript(t *testing.T) {
	forEachStory(t, func(t *testing.T, f storyFixture) {
		doc := seedLines(t, f)
		panel := doc.Scenes[0].Panels[0]
		page := string(f.client.Get(f.storyURL).Body)
		mustContain(t, page,
			`data-line="`+panel.ID+`.0" data-kind="caption"`, `<strong class="mv-c1__caption" aria-hidden="true">Caption</strong>`,
			`name="add_line" value="`+panel.ID+`"`, `name="remove_line" value="`+panel.ID+`.1"`,
			`<option value="caption" selected>Caption</option>`, "data-line-template",
			// Prose names the caption "Caption", never the empty speaker.
			`data-fallback="Caption">Caption</strong>`, `>Kaito</strong>`)

		// Add a line: the draft comes back with an empty, focused line.
		form := htmlForm(t, page, `data-view-panel="panels"`)
		form.Set("add_line", panel.ID)
		added := postForm(f.client, f.storyURL+"/script", form)
		body := string(added.Body)
		if added.StatusCode != http.StatusOK {
			t.Fatalf("add line = %d: %.600s", added.StatusCode, added.Body)
		}
		mustContain(t, body, `id="speaker-`+panel.ID+`-2"`, "autofocus", "Not saved yet.", `value="`+form.Get("revision")+`"`)
		if got := lineTexts(t, f); len(got) != 2 {
			t.Fatalf("add line saved early: %v", got)
		}
		form = htmlForm(t, body, `data-view-panel="panels"`)
		if _, ok := form["add_line"]; ok {
			t.Fatal("the intent leaked into the next submit")
		}
		form.Set("panel."+panel.ID+".dialogue.2.speaker", "Rin")
		form.Set("panel."+panel.ID+".dialogue.2.text", "Then I'll take the roof!")
		if saved := postForm(f.client, f.storyURL+"/script", form); saved.StatusCode != http.StatusSeeOther {
			t.Fatalf("save added line = %d: %.600s", saved.StatusCode, saved.Body)
		}
		want := []string{"caption||The ferry won't wait for a courier with no map.", "speech|Kaito|You're late. Again.", "speech|Rin|Then I'll take the roof!"}
		if got := lineTexts(t, f); strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("lines = %v", got)
		}

		// Remove the caption, then save.
		page = string(f.client.Get(f.storyURL).Body)
		form = htmlForm(t, page, `data-view-panel="panels"`)
		form.Set("remove_line", panel.ID+".0")
		removed := postForm(f.client, f.storyURL+"/script", form)
		if removed.StatusCode != http.StatusOK || strings.Contains(string(removed.Body), "The ferry won&#39;t wait") {
			t.Fatalf("remove line = %d", removed.StatusCode)
		}
		form = htmlForm(t, string(removed.Body), `data-view-panel="panels"`)
		if saved := postForm(f.client, f.storyURL+"/script", form); saved.StatusCode != http.StatusSeeOther {
			t.Fatalf("save removal = %d: %.600s", saved.StatusCode, saved.Body)
		}
		if got := lineTexts(t, f); strings.Join(got, "\n") != strings.Join(want[1:], "\n") {
			t.Fatalf("after removal = %v", got)
		}

		// A line added but left empty is not saved (no empty bubble to letter).
		page = string(f.client.Get(f.storyURL).Body)
		form = htmlForm(t, page, `data-view-panel="panels"`)
		form.Set("add_line", panel.ID)
		form = htmlForm(t, string(postForm(f.client, f.storyURL+"/script", form).Body), `data-view-panel="panels"`)
		if saved := postForm(f.client, f.storyURL+"/script", form); saved.StatusCode != http.StatusSeeOther {
			t.Fatalf("save with empty line = %d", saved.StatusCode)
		}
		if got := lineTexts(t, f); len(got) != 2 {
			t.Fatalf("empty line was saved: %v", got)
		}

		// A panel without dialogue can get its first line.
		if len(doc.Scenes[0].Panels) > 1 {
			second := doc.Scenes[0].Panels[1]
			page = string(f.client.Get(f.storyURL).Body)
			form = htmlForm(t, page, `data-view-panel="panels"`)
			form.Set("add_line", second.ID)
			body = string(postForm(f.client, f.storyURL+"/script", form).Body)
			mustContain(t, body, `id="speaker-`+second.ID+`-`)
		}
	})
}

type autosaveReply struct {
	State    string `json:"state"`
	Revision string `json:"revision"`
}

func autosave(t *testing.T, f storyFixture, form url.Values) (int, autosaveReply) {
	t.Helper()
	f.client.Header.Set("X-MV-Autosave", "1")
	defer f.client.Header.Del("X-MV-Autosave")
	r := postForm(f.client, f.storyURL+"/script", form)
	var reply autosaveReply
	if err := json.Unmarshal(r.Body, &reply); err != nil {
		t.Fatalf("autosave %d reply %q: %v", r.StatusCode, r.Body, err)
	}
	return r.StatusCode, reply
}

// The studio script's background save gets JSON: the new revision on success
// and an explicit state for conflicts, locks and invalid fields.
func TestStoryAutosaveRevisionsAndConflicts(t *testing.T) {
	forEachStory(t, func(t *testing.T, f storyFixture) {
		ctx := context.Background()
		doc := seedLines(t, f)
		panel := doc.Scenes[0].Panels[0]
		form := htmlForm(t, string(f.client.Get(f.storyURL).Body), `data-view-panel="panels"`)
		stale := form.Get("revision")

		// Lines posted with gaps (added and removed in the browser) keep order.
		form.Del("panel." + panel.ID + ".dialogue.0.speaker")
		form.Del("panel." + panel.ID + ".dialogue.0.text")
		form.Del("panel." + panel.ID + ".dialogue.0.kind")
		form.Set("panel."+panel.ID+".dialogue.5.speaker", "Rin")
		form.Set("panel."+panel.ID+".dialogue.5.kind", "thought")
		form.Set("panel."+panel.ID+".dialogue.5.text", "Too far?")
		form.Set("add_line", panel.ID) // ignored by a background save
		status, reply := autosave(t, f, form)
		chapter, err := f.studio.GetChapter(ctx, f.chapterID)
		if err != nil {
			t.Fatal(err)
		}
		if status != http.StatusOK || reply.State != "saved" || reply.Revision != chapter.UpdatedAt.Format(time.RFC3339Nano) {
			t.Fatalf("autosave = %d %+v (revision %s)", status, reply, chapter.UpdatedAt.Format(time.RFC3339Nano))
		}
		if got := lineTexts(t, f); strings.Join(got, "\n") != "speech|Kaito|You're late. Again.\nthought|Rin|Too far?" {
			t.Fatalf("lines = %v", got)
		}

		// The next background save carries the new revision; the old one conflicts.
		form.Set("revision", stale)
		if status, reply := autosave(t, f, form); status != http.StatusConflict || reply.State != "conflict" {
			t.Fatalf("stale autosave = %d %+v", status, reply)
		}
		form.Set("revision", reply.Revision)
		form.Set("panel."+panel.ID+".page_number", "0")
		if status, reply := autosave(t, f, form); status != http.StatusUnprocessableEntity || reply.State != "invalid" {
			t.Fatalf("invalid autosave = %d %+v", status, reply)
		}
		form.Set("panel."+panel.ID+".page_number", "1")
		if _, err := f.ta.Deps.DB.Exec(ctx, `UPDATE chapters SET locked = TRUE WHERE id = ?`, f.chapterID); err != nil {
			t.Fatal(err)
		}
		if status, reply := autosave(t, f, form); status != http.StatusLocked || reply.State != "locked" {
			t.Fatalf("locked autosave = %d %+v", status, reply)
		}
	})
}

// C1 shows one scene, addressed by its own URL, and a save only applies the
// scene its form carries.
func TestStorySceneRouteAndScopedSave(t *testing.T) {
	forEachStory(t, func(t *testing.T, f storyFixture) {
		ctx := context.Background()
		doc := seedLines(t, f)
		scene := doc.Scenes[0]
		page := f.client.Get(f.storyURL + "/" + scene.ID)
		if page.StatusCode != http.StatusOK {
			t.Fatalf("scene route = %d", page.StatusCode)
		}
		mustContain(t, string(page.Body), `href="`+f.storyURL+`/`+scene.ID+`" data-scene-link aria-current="page"`, "Ch. 1 · Scene 1")
		for _, bad := range []string{db.NewID(), "not-an-id"} {
			if got := f.client.Get(f.storyURL + "/" + bad); got.StatusCode != http.StatusNotFound {
				t.Fatalf("unknown scene %s = %d", bad, got.StatusCode)
			}
		}
		form := htmlForm(t, string(page.Body), `data-view-panel="panels"`)
		if form.Get("scene") != scene.ID {
			t.Fatalf("form scene = %q", form.Get("scene"))
		}
		form.Set("scene."+scene.ID+".title", "Not this scene")
		form.Set("scene", db.NewID()) // the form claims to carry another scene
		if saved := postForm(f.client, f.storyURL+"/script", form); saved.StatusCode != http.StatusSeeOther {
			t.Fatalf("save = %d", saved.StatusCode)
		}
		after, err := f.studio.GetScript(ctx, f.chapterID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Scenes[0].Title != scene.Title {
			t.Fatalf("a scene the form did not carry changed: %q", after.Scenes[0].Title)
		}
	})
}
