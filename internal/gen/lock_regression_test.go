package gen_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

const dinoModel = "character-lock-dinov2-v1"

var dinoOutput = map[string]any{"scores": []float64{.95}, "outfit_scores": []float64{.93}, "method": "dinov2-small-v1:" + strings.Repeat("a", 64)}

// lockWorkerSetup enables remote/worker routes and registers one controlled
// worker fixture. Fixture scores are protocol data, not quality evidence.
func lockWorkerSetup(t *testing.T, ta *testutil.TestApp, reg jobs.Registration) *jobs.Worker {
	t.Helper()
	ctx := context.Background()
	if _, err := ta.Deps.Settings.UpdateInstance(ctx, ta.Admin.ID, func(inst *settings.Instance) error { inst.LocalOnly = false; return nil }); err != nil {
		t.Fatal(err)
	}
	w, err := ta.Deps.Jobs.RegisterWorker(ctx, "lock-fixture-"+db.NewID(), "127.0.0.1", reg)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// workChild runs fn for the first worker child of parent in the background.
func workChild(ctx context.Context, ta *testutil.TestApp, w *jobs.Worker, kind, model string, fn func(child *jobs.Job) error) chan error {
	done := make(chan error, 1)
	go func() {
		for ctx.Err() == nil {
			child, err := ta.Deps.Jobs.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorWorker, WorkerID: w.ID, Types: []string{kind}, Models: []string{model}, MaxRunning: 1})
			if err != nil {
				done <- err
				return
			}
			if child == nil {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			done <- fn(child)
			return
		}
		done <- ctx.Err()
	}()
	return done
}

func jobFailedNotices(t *testing.T, ta *testutil.TestApp, userID string) int {
	t.Helper()
	var n int
	if err := ta.Deps.DB.Get(context.Background(), &n, `SELECT COUNT(*) FROM notifications WHERE user_id=? AND kind='job_failed'`, userID); err != nil {
		t.Fatal(err)
	}
	return n
}

// Assessment is read-only for artwork (ADR 0011): approved, locked art can be
// re-assessed without unlocking it, while repair keeps refusing the lock.
func TestLockedApprovedArtCanBeReassessedButNotRepaired(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		f, ps, pages := controlPages(t, ta, 1)
		target := pages[0]
		l := target.Panels[0].Layers[1]
		if err := ps.SetLock(ctx, target.ID, target.Panels[0].ID, l.ID, true, pagestore.Change{ActorKind: "user", ActorID: f.actor.UserID, Action: "lock", ExpectedVersion: target.Version}); err != nil {
			t.Fatal(err)
		}
		if err := ps.SetLock(ctx, target.ID, "", "", true, pagestore.Change{ActorKind: "user", ActorID: f.actor.UserID, Action: "lock"}); err != nil {
			t.Fatal(err)
		}
		locked, err := ps.GetPage(ctx, target.ID)
		if err != nil {
			t.Fatal(err)
		}
		w := lockWorkerSetup(t, ta, jobs.Registration{Name: "Scoring fixture", Backends: []string{"vision"}, JobTypes: []string{"character.score"}, Models: []string{dinoModel}})
		control := core.MustUse[gen.ControlService](ta.Deps)
		c, err := f.studio.GetCharacter(ctx, f.characters[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		parent, err := control.EnqueueScoring(ctx, f.actor, f.project.ID, c.ID, c.UpdatedAt)
		if err != nil {
			t.Fatalf("locked art could not be assessed: %v", err)
		}
		worked := workChild(ctx, ta, w, "character.score", dinoModel, func(child *jobs.Job) error {
			_, err := ta.Deps.Jobs.Complete(ctx, child.ID, w.ID, dinoOutput, jobs.Cost{})
			return err
		})
		done := runArtJob(t, ta, parent.ID)
		if err := <-worked; err != nil {
			t.Fatal(err)
		}
		if done.State != jobs.StateDone {
			t.Fatalf("locked art assessment: %+v", done.Error)
		}
		after, err := ps.GetPage(ctx, target.ID)
		if err != nil || pageBytes(t, after) != pageBytes(t, locked) {
			t.Fatal("assessment changed the locked page")
		}
		status, err := control.LockStatus(ctx, f.actor, f.project.ID, c.ID)
		if err != nil || status.Score == nil || *status.Score != .93 {
			t.Fatalf("assessment evidence missing: %+v %v", status, err)
		}
		repair := gen.RepairRequest{ProjectID: f.project.ID, ChapterID: f.chapter.ID, CharacterID: c.ID, Targets: []gen.RepairTarget{{PageID: target.ID, PanelID: target.Panels[0].ID, LayerID: l.ID, Version: locked.Version}}}
		if _, err := control.EnqueueRepair(ctx, f.actor, repair); !errors.Is(err, studio.ErrLocked) && !errors.Is(err, pagestore.ErrLocked) {
			t.Fatalf("locked repair accepted: %v", err)
		}
	})
}
