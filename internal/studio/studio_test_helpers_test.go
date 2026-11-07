package studio_test

import (
	"context"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

func forEachDialect(t *testing.T, fn func(*testing.T, *testutil.TestApp)) {
	t.Helper()
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			t.Parallel()
			fn(t, testutil.NewTestApp(t, testutil.WithDialect(dialect)))
		})
	}
}

func studioService(ta *testutil.TestApp) studio.Service {
	return core.MustUse[studio.Service](ta.Deps)
}

func newProjectInput(title, source string) studio.NewProject {
	return studio.NewProject{Title: title, Premise: "A courier crosses a flooded city.", Logline: "One last tide run.",
		Style: "bw_screentone", Format: "manga_rtl", PageSize: "b5", Language: "en", PageWidth: 1600,
		PageHeight: 2400, Genres: []string{"adventure"}, Tags: []string{"original"}, SourceText: source}
}

func createProject(t *testing.T, ta *testutil.TestApp, title string) (*auth.User, studio.Actor, studio.Project) {
	t.Helper()
	owner := ta.CreateUser(t, "", auth.RoleCreator)
	svc := studioService(ta)
	actor, err := svc.ActorFromUser(context.Background(), owner.ID)
	if err != nil {
		t.Fatalf("ActorFromUser: %v", err)
	}
	project, err := svc.CreateProject(context.Background(), actor, newProjectInput(title, ""))
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return owner, actor, project
}

func validScript(characterID string) studio.ScriptDoc {
	panel := studio.ScriptPanel{Key: "panel-1", PageNumber: 1, Camera: "wide", Shot: "establishing",
		Emotion: "urgent", Action: "Rin runs along the seawall.", Dialogue: []studio.ScriptDialogue{},
		SFX: []studio.ScriptSFX{{Text: "SPLASH", Note: "distant"}}, CharacterIDs: []string{}, Notes: ""}
	if characterID != "" {
		panel.Dialogue = []studio.ScriptDialogue{{Speaker: "Rin", CharacterID: characterID, Kind: "speech", Text: "Keep moving!"}}
		panel.CharacterIDs = []string{characterID}
	}
	return studio.ScriptDoc{Schema: "mangaveyra.script.v1", ChapterSummary: "The delivery begins.", Scenes: []studio.ScriptScene{{
		Key: "scene-1", Title: "Sea Gate", Summary: "A storm closes in.", TimeOfDay: "dawn", Panels: []studio.ScriptPanel{panel},
	}}}
}
