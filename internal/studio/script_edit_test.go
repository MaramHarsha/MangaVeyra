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

type editableScriptFixture struct {
	svc       studio.Service
	actor     studio.Actor
	project   studio.Project
	chapter   studio.Chapter
	character studio.Character
	doc       studio.ScriptDoc
}

func newEditableScriptFixture(t *testing.T, ta *testutil.TestApp, title string) editableScriptFixture {
	t.Helper()
	ctx := context.Background()
	svc := studioService(ta)
	_, actor, project := createProject(t, ta, title)
	character, err := svc.CreateCharacter(ctx, actor, studio.NewCharacter{
		ProjectID: project.ID, Name: "Rin", Role: "courier", Look: "Rain cape", Personality: "Steadfast", Category: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	chapter, err := svc.CreateChapter(ctx, actor, project.ID, "The Closing Gate")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ta.Deps.DB.Exec(ctx, `UPDATE chapters SET settings = ? WHERE id = ?`,
		`{"future":{"keep":true}}`, chapter.ID); err != nil {
		t.Fatal(err)
	}
	doc := validScript(character.ID)
	second := doc.Scenes[0].Panels[0]
	second.Key = "panel-2"
	second.Action = "Rin reaches the closing gate."
	doc.Scenes[0].Panels = append(doc.Scenes[0].Panels, second)
	if err := ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
		_, err := svc.ReplaceGeneratedScriptTx(ctx, tx, studio.ReplaceScriptInput{
			Actor: actor, ProjectID: project.ID, ChapterID: chapter.ID, Doc: doc,
			JobID: "job-editable", ProviderID: "placeholder", ModelID: "script-v1", Provenance: "ai",
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	chapter, err = svc.GetChapter(ctx, chapter.ID)
	if err != nil {
		t.Fatal(err)
	}
	doc, err = svc.GetScript(ctx, chapter.ID)
	if err != nil {
		t.Fatal(err)
	}
	return editableScriptFixture{svc: svc, actor: actor, project: project, chapter: chapter, character: character, doc: doc}
}

func cloneScript(t *testing.T, doc studio.ScriptDoc) studio.ScriptDoc {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var out studio.ScriptDoc
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestEditScriptPreservesStableIDsAndUsesOptimisticRevision(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		fixture := newEditableScriptFixture(t, ta, "Human Edit")
		before := cloneScript(t, fixture.doc)
		var settingsBefore string
		if err := ta.Deps.DB.Get(ctx, &settingsBefore, `SELECT settings FROM chapters WHERE id = ?`, fixture.chapter.ID); err != nil {
			t.Fatal(err)
		}

		edited := cloneScript(t, fixture.doc)
		edited.ChapterSummary = "Rin reaches the gate before the tide turns."
		edited.Scenes[0].Title = "Gate at Dusk"
		edited.Scenes[0].Summary = "The final crossing."
		edited.Scenes[0].Panels[0].PageNumber = 2
		edited.Scenes[0].Panels[0].Camera = "close-up"
		edited.Scenes[0].Panels[0].Action = "Rin grips the brass wheel."
		edited.Scenes[0].Panels[0].Dialogue[0].Text = "Open the gate!"
		edited.Scenes[0].Panels[1].PageNumber = 2
		edited.Scenes[0].Panels[1].Notes = "Hold on the rain."
		updated, err := fixture.svc.EditScript(ctx, studio.EditScriptInput{
			Actor: fixture.actor, ProjectID: fixture.project.ID, ChapterID: fixture.chapter.ID,
			Doc: edited, ExpectedUpdatedAt: fixture.chapter.UpdatedAt,
		})
		if err != nil {
			t.Fatalf("EditScript: %v", err)
		}
		if !updated.UpdatedAt.After(fixture.chapter.UpdatedAt) || updated.AILabel["script"] != "ai_assisted" ||
			updated.Summary != edited.ChapterSummary {
			t.Fatalf("updated chapter = %#v", updated)
		}
		got, err := fixture.svc.GetScript(ctx, fixture.chapter.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Scenes[0].ID != before.Scenes[0].ID || got.Scenes[0].Panels[0].ID != before.Scenes[0].Panels[0].ID ||
			got.Scenes[0].Panels[1].ID != before.Scenes[0].Panels[1].ID {
			t.Fatalf("stable IDs changed: before=%#v after=%#v", before, got)
		}
		if got.Scenes[0].Title != edited.Scenes[0].Title || got.Scenes[0].Panels[0].Action != edited.Scenes[0].Panels[0].Action ||
			got.Scenes[0].Panels[0].Label != "2.1" || got.Scenes[0].Panels[1].Label != "2.2" {
			t.Fatalf("edited script = %#v", got)
		}
		var settingsAfter string
		if err := ta.Deps.DB.Get(ctx, &settingsAfter, `SELECT settings FROM chapters WHERE id = ?`, fixture.chapter.ID); err != nil {
			t.Fatal(err)
		}
		if settingsAfter != settingsBefore {
			t.Fatalf("chapter settings changed: before=%s after=%s", settingsBefore, settingsAfter)
		}

		stale := cloneScript(t, got)
		stale.ChapterSummary = "This stale edit must not land."
		_, err = fixture.svc.EditScript(ctx, studio.EditScriptInput{
			Actor: fixture.actor, ProjectID: fixture.project.ID, ChapterID: fixture.chapter.ID,
			Doc: stale, ExpectedUpdatedAt: fixture.chapter.UpdatedAt,
		})
		if !errors.Is(err, studio.ErrConflict) {
			t.Fatalf("stale revision error = %v, want ErrConflict", err)
		}
		afterStale, err := fixture.svc.GetScript(ctx, fixture.chapter.ID)
		if err != nil || !reflect.DeepEqual(afterStale, got) {
			t.Fatalf("stale edit mutated script: got=%#v want=%#v err=%v", afterStale, got, err)
		}
	})
}

func TestEditScriptTransactionalGuards(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		t.Run("locked", func(t *testing.T) {
			fixture := newEditableScriptFixture(t, ta, "Locked Edit")
			if _, err := ta.Deps.DB.Exec(ctx, `UPDATE chapters SET locked = TRUE WHERE id = ?`, fixture.chapter.ID); err != nil {
				t.Fatal(err)
			}
			_, err := fixture.svc.EditScript(ctx, studio.EditScriptInput{Actor: fixture.actor, ProjectID: fixture.project.ID,
				ChapterID: fixture.chapter.ID, Doc: fixture.doc, ExpectedUpdatedAt: fixture.chapter.UpdatedAt})
			if !errors.Is(err, studio.ErrLocked) {
				t.Fatalf("locked error = %v", err)
			}
		})

		t.Run("cross project", func(t *testing.T) {
			fixture := newEditableScriptFixture(t, ta, "Cross Project Edit")
			other, err := fixture.svc.CreateProject(ctx, fixture.actor, newProjectInput("Other Edit Project", ""))
			if err != nil {
				t.Fatal(err)
			}
			_, err = fixture.svc.EditScript(ctx, studio.EditScriptInput{Actor: fixture.actor, ProjectID: other.ID,
				ChapterID: fixture.chapter.ID, Doc: fixture.doc, ExpectedUpdatedAt: fixture.chapter.UpdatedAt})
			if !errors.Is(err, studio.ErrForbidden) {
				t.Fatalf("cross-project error = %v", err)
			}
		})

		t.Run("invalid character", func(t *testing.T) {
			fixture := newEditableScriptFixture(t, ta, "Character Guard Edit")
			other, err := fixture.svc.CreateProject(ctx, fixture.actor, newProjectInput("Foreign Character Project", ""))
			if err != nil {
				t.Fatal(err)
			}
			foreign, err := fixture.svc.CreateCharacter(ctx, fixture.actor, studio.NewCharacter{
				ProjectID: other.ID, Name: "Sora", Category: "supporting",
			})
			if err != nil {
				t.Fatal(err)
			}
			edited := cloneScript(t, fixture.doc)
			edited.Scenes[0].Panels[0].CharacterIDs = []string{foreign.ID}
			edited.Scenes[0].Panels[0].Dialogue[0].CharacterID = foreign.ID
			_, err = fixture.svc.EditScript(ctx, studio.EditScriptInput{Actor: fixture.actor, ProjectID: fixture.project.ID,
				ChapterID: fixture.chapter.ID, Doc: edited, ExpectedUpdatedAt: fixture.chapter.UpdatedAt})
			if !errors.Is(err, studio.ErrInvalid) {
				t.Fatalf("foreign-character error = %v", err)
			}
		})

		t.Run("stable structure", func(t *testing.T) {
			fixture := newEditableScriptFixture(t, ta, "Structure Guard Edit")
			cases := map[string]func(*studio.ScriptDoc){
				"missing scene": func(doc *studio.ScriptDoc) { doc.Scenes = nil },
				"missing ID":    func(doc *studio.ScriptDoc) { doc.Scenes[0].Panels[0].ID = "" },
				"arbitrary ID":  func(doc *studio.ScriptDoc) { doc.Scenes[0].ID = db.NewID() },
				"duplicate ID": func(doc *studio.ScriptDoc) {
					doc.Scenes[0].Panels[1].ID = doc.Scenes[0].Panels[0].ID
				},
				"reordered": func(doc *studio.ScriptDoc) {
					doc.Scenes[0].Panels[0], doc.Scenes[0].Panels[1] = doc.Scenes[0].Panels[1], doc.Scenes[0].Panels[0]
				},
			}
			for name, mutate := range cases {
				t.Run(name, func(t *testing.T) {
					edited := cloneScript(t, fixture.doc)
					mutate(&edited)
					_, err := fixture.svc.EditScript(ctx, studio.EditScriptInput{Actor: fixture.actor,
						ProjectID: fixture.project.ID, ChapterID: fixture.chapter.ID, Doc: edited,
						ExpectedUpdatedAt: fixture.chapter.UpdatedAt})
					if !errors.Is(err, studio.ErrConflict) {
						t.Fatalf("structure error = %v, want ErrConflict", err)
					}
				})
			}
		})

		t.Run("unauthorized", func(t *testing.T) {
			fixture := newEditableScriptFixture(t, ta, "Unauthorized Edit")
			viewer := ta.CreateUser(t, "", auth.RoleCreator)
			if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role, created_at)
				VALUES (?, ?, ?, ?)`, fixture.project.ID, viewer.ID, studio.RoleViewer, db.Now()); err != nil {
				t.Fatal(err)
			}
			viewerActor, err := fixture.svc.ActorFromUser(ctx, viewer.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = fixture.svc.EditScript(ctx, studio.EditScriptInput{Actor: viewerActor, ProjectID: fixture.project.ID,
				ChapterID: fixture.chapter.ID, Doc: fixture.doc, ExpectedUpdatedAt: fixture.chapter.UpdatedAt})
			if !errors.Is(err, studio.ErrForbidden) {
				t.Fatalf("viewer edit error = %v", err)
			}
		})
	})
}
