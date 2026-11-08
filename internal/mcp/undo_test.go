package mcp_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// TestAgentChangesCanBeUndone covers script, character and page-art undo,
// including a refusal when a person changed the same thing afterwards.
func TestAgentChangesCanBeUndone(t *testing.T) {
	forEachDialect(t, func(t *testing.T, f *fixture) {
		raw, info := f.token("Local agent · Ollama", scopesGenerate, nil, nil)
		s := f.connect(raw)
		before := f.script()
		panel := before.Scenes[0].Panels[2]
		out := mustCall(t, s, "edit_script", map[string]any{"project_id": f.project.ID, "chapter_id": f.chapter.ID, "revision": f.revision(),
			"panels": []any{map[string]any{"panel_id": panel.ID, "action": "Wren slips the parcel into Rin's satchel without a word.", "emotion": "quiet"}}})
		scriptAction := str(out["action_id"])
		if got := f.script().Scenes[0].Panels[2]; got.Action == panel.Action || got.Emotion != "quiet" {
			t.Fatalf("edit not stored: %+v", got)
		}
		if !strings.Contains(string(f.browser.Get("/agents").Body), "Edited panel 1.3") {
			t.Fatal("G13 lacks the script edit")
		}
		if r := form(f.browser, "/agents/actions/"+scriptAction+"/undo", nil); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("undo %d", r.StatusCode)
		}
		if got := f.script().Scenes[0].Panels[2]; got.Action != panel.Action || got.Emotion != panel.Emotion {
			t.Fatalf("undo did not restore: %+v", got)
		}
		if f.actionStatus(scriptAction) != "undone" {
			t.Fatal("undo not recorded")
		}
		if !strings.Contains(string(f.browser.Get("/agents").Body), "· undone") {
			t.Fatal("G13 does not show undone")
		}
		if n := f.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'agent.undo' AND actor_kind = 'user' AND actor_id = ?`, f.owner.ID); n != 1 {
			t.Fatalf("undo audit %d", n)
		}

		// A later human change to the same field blocks the undo.
		out = mustCall(t, s, "edit_script", map[string]any{"project_id": f.project.ID, "chapter_id": f.chapter.ID, "revision": f.revision(),
			"panels": []any{map[string]any{"panel_id": panel.ID, "notes": "Agent note"}}})
		blocked := str(out["action_id"])
		doc := f.script()
		doc.Scenes[0].Panels[2].Notes = "Person's note"
		c, _ := f.studio.GetChapter(f.ctx, f.chapter.ID)
		if _, err := f.studio.EditScript(f.ctx, studio.EditScriptInput{Actor: studio.Actor{UserID: f.owner.ID}, ProjectID: f.project.ID,
			ChapterID: f.chapter.ID, Doc: doc, ExpectedUpdatedAt: c.UpdatedAt}); err != nil {
			t.Fatal(err)
		}
		if r := form(f.browser, "/agents/actions/"+blocked+"/undo", nil); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("blocked undo %d", r.StatusCode)
		}
		if f.actionStatus(blocked) != "done" || f.script().Scenes[0].Panels[2].Notes != "Person's note" {
			t.Fatal("undo overwrote a later person's change")
		}

		// Character edit and undo.
		character := f.firstCharacter()
		edited := mustCall(t, s, "edit_character", map[string]any{"project_id": f.project.ID, "character_id": character.ID,
			"revision": character.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), "personality": "Restless and kind"})
		if r := form(f.browser, "/agents/actions/"+str(edited["action_id"])+"/undo", nil); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("character undo %d", r.StatusCode)
		}
		if got, _ := f.studio.GetCharacter(f.ctx, character.ID); got.Personality != character.Personality {
			t.Fatalf("character not restored: %q", got.Personality)
		}
		created := mustCall(t, s, "create_character", map[string]any{"project_id": f.project.ID, "name": "Mara Quill", "category": "extra"})
		if f.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'agent.character.create' AND token_id = ?`, info.ID) != 1 || str(created["action_id"]) == "" {
			t.Fatal("character creation not labelled")
		}

		// Page art: a region edit applies one variation as a labelled version;
		// undo reverts the page to the version before it.
		pages := mustCall(t, s, "get_pages", map[string]any{"project_id": f.project.ID, "chapter_id": f.chapter.ID})["pages"].([]any)
		first := pages[0].(map[string]any)
		layerID := ""
		for _, pn := range first["panels"].([]any) {
			for _, l := range pn.(map[string]any)["layers"].([]any) {
				lm := l.(map[string]any)
				if str(lm["kind"]) == "background" && lm["has_art"] == true && layerID == "" {
					layerID = str(lm["id"])
				}
			}
		}
		pageID := str(first["id"])
		original, err := f.pages.GetPage(f.ctx, pageID)
		if err != nil || layerID == "" {
			t.Fatalf("page %v layer %q", err, layerID)
		}
		queued := mustCall(t, s, "queue_generation", map[string]any{"project_id": f.project.ID, "kind": "region", "page_id": pageID,
			"version": original.Version, "layer_id": layerID, "prompt": "Storm clouds break over the quay",
			"region": map[string]any{"x": 0.1, "y": 0.1, "width": 0.4, "height": 0.3}})
		if str(queued["status"]) != "queued" {
			t.Fatalf("region %v", queued)
		}
		job := f.waitJob(str(queued["job"].(map[string]any)["id"]))
		if job.State != "done" {
			t.Fatalf("region job %s %+v", job.State, job.Error)
		}
		changed, _ := f.pages.GetPage(f.ctx, pageID)
		if changed.Version == original.Version {
			t.Fatal("region edit did not change the page")
		}
		if n := f.count(`SELECT COUNT(*) FROM versions WHERE page_id = ? AND job_id = ? AND actor_kind = 'agent' AND token_id = ?`, pageID, job.ID, info.ID); n == 0 {
			t.Fatal("region version not labelled with the token")
		}
		if !strings.Contains(string(f.browser.Get("/agents").Body), "Repainted a region of panel 1.") {
			t.Fatal("G13 lacks the region edit")
		}
		if r := form(f.browser, "/agents/actions/"+str(queued["action_id"])+"/undo", nil); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("region undo %d", r.StatusCode)
		}
		reverted, _ := f.pages.GetPage(f.ctx, pageID)
		if reverted.Version != changed.Version+1 || layerAsset(reverted, layerID) != layerAsset(original, layerID) {
			t.Fatalf("revert v%d asset %q, want asset %q", reverted.Version, layerAsset(reverted, layerID), layerAsset(original, layerID))
		}
		if f.actionStatus(str(queued["action_id"])) != "undone" {
			t.Fatal("art undo not recorded")
		}
	})
}

func layerAsset(p pagedoc.Page, id string) string {
	for i := range p.Panels {
		if l := p.Panels[i].Layer(id); l != nil && l.Asset != nil {
			return l.Asset.ID
		}
	}
	return ""
}
