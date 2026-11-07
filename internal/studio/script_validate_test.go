package studio_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestValidateScriptStrictSemanticMatrix(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		svc := studioService(ta)
		_, actor, project := createProject(t, ta, "Script Validation")
		character, err := svc.CreateCharacter(ctx, actor, studio.NewCharacter{ProjectID: project.ID, Name: "Sora Vell", Category: "main"})
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.ValidateScript(ctx, project.ID, validScript(character.ID)); err != nil {
			t.Fatalf("valid script: %v", err)
		}
		if err := svc.ValidateScript(ctx, "missing-project", validScript("")); !errors.Is(err, studio.ErrNotFound) {
			t.Fatalf("missing project error = %v, want ErrNotFound", err)
		}

		cases := map[string]func() studio.ScriptDoc{
			"schema": func() studio.ScriptDoc {
				d := validScript(character.ID)
				d.Schema = "mangaveyra.script.v2"
				return d
			},
			"no scenes": func() studio.ScriptDoc {
				d := validScript(character.ID)
				d.Scenes = nil
				return d
			},
			"duplicate scene key": func() studio.ScriptDoc {
				d := validScript(character.ID)
				s := d.Scenes[0]
				s.Panels = append([]studio.ScriptPanel(nil), s.Panels...)
				s.Panels[0].Key = "panel-2"
				d.Scenes = append(d.Scenes, s)
				return d
			},
			"duplicate panel key": func() studio.ScriptDoc {
				d := validScript(character.ID)
				d.Scenes[0].Panels = append(d.Scenes[0].Panels, d.Scenes[0].Panels[0])
				return d
			},
			"bad dialogue kind": func() studio.ScriptDoc {
				d := validScript(character.ID)
				d.Scenes[0].Panels[0].Dialogue[0].Kind = "whisper"
				return d
			},
			"page zero": func() studio.ScriptDoc {
				d := validScript(character.ID)
				d.Scenes[0].Panels[0].PageNumber = 0
				return d
			},
			"decreasing pages": func() studio.ScriptDoc {
				d := validScript(character.ID)
				d.Scenes[0].Panels[0].PageNumber = 2
				p := d.Scenes[0].Panels[0]
				p.Key, p.PageNumber = "panel-2", 1
				d.Scenes[0].Panels = append(d.Scenes[0].Panels, p)
				return d
			},
			"empty panel": func() studio.ScriptDoc {
				d := validScript(character.ID)
				p := &d.Scenes[0].Panels[0]
				p.Action, p.Dialogue, p.SFX = "", nil, nil
				return d
			},
			"foreign character": func() studio.ScriptDoc {
				d := validScript("")
				d.Scenes[0].Panels[0].CharacterIDs = []string{"not-in-project"}
				return d
			},
			"duplicate character": func() studio.ScriptDoc {
				d := validScript(character.ID)
				d.Scenes[0].Panels[0].CharacterIDs = []string{character.ID, character.ID}
				return d
			},
			"oversize action": func() studio.ScriptDoc {
				d := validScript(character.ID)
				d.Scenes[0].Panels[0].Action = strings.Repeat("界", 2001)
				return d
			},
			"invalid UTF-8": func() studio.ScriptDoc {
				d := validScript(character.ID)
				d.Scenes[0].Title = string([]byte{0xff})
				return d
			},
			"too many panels": func() studio.ScriptDoc {
				d := validScript("")
				d.Scenes[0].Panels = make([]studio.ScriptPanel, 257)
				for i := range d.Scenes[0].Panels {
					d.Scenes[0].Panels[i] = studio.ScriptPanel{Key: fmt.Sprintf("panel-%d", i), PageNumber: i + 1, Action: "Beat"}
				}
				return d
			},
		}
		for name, makeDoc := range cases {
			t.Run(name, func(t *testing.T) {
				if err := svc.ValidateScript(ctx, project.ID, makeDoc()); !errors.Is(err, studio.ErrInvalid) {
					t.Fatalf("ValidateScript error = %v, want ErrInvalid", err)
				}
			})
		}
	})
}
