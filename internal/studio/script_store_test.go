package studio_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestReplaceGeneratedScriptPersistsAndPreservesChapterData(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		svc := studioService(ta)
		_, actor, project := createProject(t, ta, "Persisted Script")
		character, err := svc.CreateCharacter(ctx, actor, studio.NewCharacter{ProjectID: project.ID, Name: "Old Wren", Category: "supporting"})
		if err != nil {
			t.Fatal(err)
		}
		chapter, err := svc.CreateChapter(ctx, actor, project.ID, "The Original Title")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE chapters SET settings = ? WHERE id = ?`, `{"future":{"keep":true}}`, chapter.ID); err != nil {
			t.Fatal(err)
		}

		doc := validScript(character.ID)
		second := doc.Scenes[0].Panels[0]
		second.Key = "panel-2"
		second.Action = "The courier reaches the gate."
		doc.Scenes[0].Panels = append(doc.Scenes[0].Panels, second)
		var result studio.ReplaceScriptResult
		if err := ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
			var err error
			result, err = svc.ReplaceGeneratedScriptTx(ctx, tx, studio.ReplaceScriptInput{Actor: actor, ProjectID: project.ID,
				ChapterID: chapter.ID, Doc: doc, JobID: "job-1", ProviderID: "placeholder", ModelID: "script-v1", Provenance: "ai"})
			return err
		}); err != nil {
			t.Fatalf("ReplaceGeneratedScriptTx: %v", err)
		}
		if result.Scenes != 1 || result.Panels != 2 || result.Pages != 1 {
			t.Fatalf("replacement result = %#v", result)
		}
		got, err := svc.GetScript(ctx, chapter.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.ChapterID != chapter.ID || got.ChapterSummary != doc.ChapterSummary || len(got.Scenes) != 1 || len(got.Scenes[0].Panels) != 2 {
			t.Fatalf("GetScript = %#v", got)
		}
		if got.Scenes[0].ID == "" || got.Scenes[0].Key != got.Scenes[0].ID || got.Scenes[0].Key == doc.Scenes[0].Key {
			t.Fatalf("scene did not get a server identity: %#v", got.Scenes[0])
		}
		if got.Scenes[0].Panels[0].ID == "" || got.Scenes[0].Panels[0].Key != got.Scenes[0].Panels[0].ID ||
			got.Scenes[0].Panels[0].Label != "1.1" || got.Scenes[0].Panels[1].Label != "1.2" {
			t.Fatalf("server panel identity/labels = %#v", got.Scenes[0].Panels)
		}
		gotChapter, err := svc.GetChapter(ctx, chapter.ID)
		if err != nil || gotChapter.Title != "The Original Title" || gotChapter.Status != "scripting" || gotChapter.AILabel["script"] != "ai" {
			t.Fatalf("chapter after replacement = %#v, %v", gotChapter, err)
		}
		var rawSettings string
		if err := ta.Deps.DB.Get(ctx, &rawSettings, `SELECT settings FROM chapters WHERE id = ?`, chapter.ID); err != nil {
			t.Fatal(err)
		}
		var settings map[string]json.RawMessage
		if err := json.Unmarshal([]byte(rawSettings), &settings); err != nil {
			t.Fatal(err)
		}
		if _, ok := settings["future"]; !ok {
			t.Fatalf("unrelated chapter setting was lost: %s", rawSettings)
		}
		if _, ok := settings["script_provenance"]; !ok {
			t.Fatalf("script provenance missing: %s", rawSettings)
		}
	})
}

func TestReplaceGeneratedScriptRollbackAndTransactionalRechecks(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		svc := studioService(ta)
		owner, actor, project := createProject(t, ta, "Rollback Script")
		chapter, err := svc.CreateChapter(ctx, actor, project.ID, "Stable")
		if err != nil {
			t.Fatal(err)
		}
		base := validScript("")
		replace := func(tx *db.Tx, doc studio.ScriptDoc, job string) error {
			_, err := svc.ReplaceGeneratedScriptTx(ctx, tx, studio.ReplaceScriptInput{Actor: actor, ProjectID: project.ID,
				ChapterID: chapter.ID, Doc: doc, JobID: job, ProviderID: "placeholder", ModelID: "script-v1", Provenance: "ai"})
			return err
		}
		if err := ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error { return replace(tx, base, "job-base") }); err != nil {
			t.Fatal(err)
		}
		before, err := svc.GetScript(ctx, chapter.ID)
		if err != nil {
			t.Fatal(err)
		}
		err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
			_, err := svc.ReplaceGeneratedScriptTx(ctx, tx, studio.ReplaceScriptInput{Actor: actor, ProjectID: project.ID,
				ChapterID: chapter.ID, Doc: validScript(""), ExpectedUpdatedAt: chapter.UpdatedAt, JobID: "job-raced",
				ProviderID: "placeholder", ModelID: "script-v1", Provenance: "ai"})
			return err
		})
		if !errors.Is(err, studio.ErrConflict) {
			t.Fatalf("stale generated replacement error = %v, want ErrConflict", err)
		}
		unchanged, err := svc.GetScript(ctx, chapter.ID)
		if err != nil || !reflect.DeepEqual(unchanged, before) {
			t.Fatalf("stale generated conflict changed script: before=%#v after=%#v err=%v", before, unchanged, err)
		}

		sentinel := errors.New("force outer rollback")
		changed := validScript("")
		changed.ChapterSummary = "This must roll back."
		err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
			if err := replace(tx, changed, "job-rollback"); err != nil {
				return err
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("rollback transaction error = %v", err)
		}
		after, err := svc.GetScript(ctx, chapter.ID)
		if err != nil || !reflect.DeepEqual(after, before) {
			t.Fatalf("script changed after rollback: before=%#v after=%#v err=%v", before, after, err)
		}

		err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE chapters SET locked = TRUE WHERE id = ?`, chapter.ID); err != nil {
				return err
			}
			return replace(tx, changed, "job-locked")
		})
		if !errors.Is(err, studio.ErrLocked) {
			t.Fatalf("transactional chapter lock error = %v, want ErrLocked", err)
		}
		err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
			if _, err := tx.Exec(ctx, `DELETE FROM project_members WHERE project_id = ? AND user_id = ?`, project.ID, owner.ID); err != nil {
				return err
			}
			return replace(tx, changed, "job-no-member")
		})
		if !errors.Is(err, studio.ErrForbidden) {
			t.Fatalf("transactional membership recheck error = %v, want ErrForbidden", err)
		}
		_, token, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: owner.ID, Kind: auth.TokenAgent,
			Name: "script worker", Scopes: []string{"projects:write", "generate"}, ProjectIDs: []string{project.ID}})
		if err != nil {
			t.Fatal(err)
		}
		tokenActor := actor
		tokenActor.TokenID = token.ID
		err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE api_tokens SET revoked_at = ? WHERE id = ?`, db.Now(), token.ID); err != nil {
				return err
			}
			_, err := svc.ReplaceGeneratedScriptTx(ctx, tx, studio.ReplaceScriptInput{Actor: tokenActor, ProjectID: project.ID,
				ChapterID: chapter.ID, Doc: changed, JobID: "job-revoked", ProviderID: "placeholder", ModelID: "script-v1", Provenance: "ai"})
			return err
		})
		if !errors.Is(err, studio.ErrForbidden) {
			t.Fatalf("transactional token recheck error = %v, want ErrForbidden", err)
		}
		foreign := validScript("")
		foreign.Scenes[0].Panels[0].CharacterIDs = []string{"foreign-character"}
		err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error { return replace(tx, foreign, "job-foreign") })
		if !errors.Is(err, studio.ErrInvalid) {
			t.Fatalf("transactional allow-list error = %v, want ErrInvalid", err)
		}
		final, err := svc.GetScript(ctx, chapter.ID)
		if err != nil || !reflect.DeepEqual(final, before) {
			t.Fatalf("failed replacement changed script: before=%#v final=%#v err=%v", before, final, err)
		}
	})
}
