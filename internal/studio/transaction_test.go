package studio_test

import (
	"context"
	"errors"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestTransactionalStudioBoundariesUseCurrentRows(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx, svc := context.Background(), studioService(ta)
		_, actor, project := createProject(t, ta, "Fenced References")
		chapter, err := svc.CreateChapter(ctx, actor, project.ID, "Tide Gate")
		if err != nil {
			t.Fatal(err)
		}
		character, err := svc.CreateCharacter(ctx, actor, studio.NewCharacter{ProjectID: project.ID, Name: "Rin", Category: "main"})
		if err != nil {
			t.Fatal(err)
		}
		if character.UpdatedAt.IsZero() {
			t.Fatal("character has no revision")
		}
		err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
			if err := svc.AuthorizeTx(ctx, tx, actor, project.ID, studio.ActGenerate); err != nil {
				return err
			}
			locked, err := svc.GetCharacterTx(ctx, tx, character.ID)
			if err != nil {
				return err
			}
			if !locked.UpdatedAt.Equal(character.UpdatedAt) {
				t.Fatal("transaction lost character revision")
			}
			if _, err := tx.Exec(ctx, `UPDATE chapters SET locked = TRUE WHERE id = ?`, chapter.ID); err != nil {
				return err
			}
			if err := svc.CheckScopeWritableTx(ctx, tx, jobs.Scope{Kind: jobs.ScopeChapter, ProjectID: project.ID, ChapterID: chapter.ID}); !errors.Is(err, studio.ErrLocked) {
				t.Fatalf("transaction ignored current lock: %v", err)
			}
			if _, err := tx.Exec(ctx, `DELETE FROM project_members WHERE project_id = ? AND user_id = ?`, project.ID, actor.UserID); err != nil {
				return err
			}
			if err := svc.AuthorizeTx(ctx, tx, actor, project.ID, studio.ActGenerate); !errors.Is(err, studio.ErrForbidden) {
				t.Fatalf("transaction ignored revoked membership: %v", err)
			}
			return context.Canceled
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if err := svc.AuthorizeTx(ctx, nil, actor, project.ID, studio.ActRead); !errors.Is(err, studio.ErrInvalid) {
			t.Fatalf("nil authorization tx: %v", err)
		}
		if err := svc.CheckScopeWritableTx(ctx, nil, jobs.Scope{Kind: jobs.ScopeNone}); !errors.Is(err, studio.ErrInvalid) {
			t.Fatalf("nil scope tx: %v", err)
		}
		if _, err := svc.GetCharacterTx(ctx, nil, character.ID); !errors.Is(err, studio.ErrInvalid) {
			t.Fatalf("nil character tx: %v", err)
		}
	})
}
